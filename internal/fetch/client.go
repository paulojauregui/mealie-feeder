package fetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"
)

// Typed errors distinguishing policy stops (never bypass/solve) from transient vs permanent HTTP failures.
var (
	// ErrChallengeDetected indicates Cloudflare Turnstile, CAPTCHA, or anti-bot challenge was returned.
	// Pre-conditions: Observed in HTTP response headers or body markers.
	// Post-conditions: Never retried; ingestion halts or marks URL skipped_paywall.
	ErrChallengeDetected = errors.New("anti-bot or proof-of-work challenge detected")

	// ErrPaywallDetected indicates subscriber authentication, paywall, or login gate was encountered.
	// Pre-conditions: Observed HTTP 402 or explicit paywall markup.
	// Post-conditions: Never retried; marked skipped_paywall.
	ErrPaywallDetected = errors.New("paywall or login gate detected")

	// ErrRetryableHTTP indicates transient server error (429, 5xx) eligible for retry.
	// Pre-conditions: HTTP status in [429, 500, 502, 503, 504].
	// Post-conditions: Retried according to RequestOptions.MaxRetries and backoff.
	ErrRetryableHTTP = errors.New("retryable upstream HTTP status")

	// ErrPermanentHTTP indicates fatal HTTP client or resource error (404, 410).
	// Pre-conditions: HTTP status in [404, 410] or terminal authorization failure.
	// Post-conditions: Never retried; marked failed_permanent.
	ErrPermanentHTTP = errors.New("permanent upstream HTTP error")

	// ErrBodyTooLarge indicates response payload exceeded RequestOptions.MaxBodyBytes.
	// Pre-conditions: Payload bytes exceed configured limit.
	// Post-conditions: Stream reading terminated early to protect memory.
	ErrBodyTooLarge = errors.New("response body exceeded MaxBodyBytes limit")
)

const (
	defaultTimeout      = 20 * time.Second
	defaultMaxBodyBytes = 16 * 1024 * 1024 // 16 MiB
	defaultMaxRetries   = 2
	maxRedirectHops     = 10
)

// Response represents a bounded HTTP response after redirect resolution.
// Pre-conditions: Body is capped at RequestOptions.MaxBodyBytes.
// Post-conditions: FinalURL records the resolved canonical URL after redirects.
type Response struct {
	FinalURL   string
	StatusCode int
	Header     http.Header
	Body       []byte
}

// RequestOptions controls TLS profile, timeouts, retry budgets, and conditional headers for one fetch call.
// Pre-conditions: TLSMode should be "chrome_auto", "safari_auto", or "plain" (defaults to "chrome_auto").
// Post-conditions: Zero-value fields take safe defaults (Timeout 20s, MaxBodyBytes 16 MiB, MaxRetries 2).
type RequestOptions struct {
	TLSMode      string
	MinInterval  time.Duration
	Timeout      time.Duration
	MaxRetries   int
	MaxBodyBytes int64
	Headers      map[string]string
}

// Fetcher defines the consumer-facing HTTP retrieval contract.
// Pre-conditions: rawURL must be an absolute http/https URL.
// Post-conditions: Returns *Response on 200 OK (or 304 Not Modified when conditional headers match), or typed error.
type Fetcher interface {
	Fetch(ctx context.Context, rawURL string, opts RequestOptions) (*Response, error)
}

// Client executes polite browser-TLS HTTP fetches with uTLS and automatic HTTP/2 ALPN negotiation.
// Pre-conditions: Constructed via NewClient.
// Post-conditions: Safe for concurrent calls across domains; never solves or retries anti-bot challenges.
type Client struct {
	limiter   *DomainLimiter
	clock     func() time.Time
	transport http.RoundTripper

	mu          sync.Mutex
	uTransports map[string]http.RoundTripper
}

// NewClient creates a Client backed by limiter and optional custom RoundTripper factory (nil uses production uTLS transport).
// Pre-conditions: limiter must be non-nil.
// Post-conditions: Ready to serve Fetch calls across chrome_auto, safari_auto, and plain profiles.
func NewClient(limiter *DomainLimiter, clock func() time.Time) *Client {
	if clock == nil {
		clock = time.Now
	}
	return &Client{
		limiter:     limiter,
		clock:       clock,
		uTransports: make(map[string]http.RoundTripper),
	}
}

// NewClientWithTransport creates a Client with an injected http.RoundTripper (primarily for offline httptest verification).
// Pre-conditions: limiter and rt must be non-nil.
// Post-conditions: Executes full header injection, rate limiting, retries, and challenge detection over rt.
func NewClientWithTransport(limiter *DomainLimiter, rt http.RoundTripper, clock func() time.Time) *Client {
	if clock == nil {
		clock = time.Now
	}
	return &Client{
		limiter:   limiter,
		clock:     clock,
		transport: rt,
	}
}

// BrowserHeaders returns coherent HTTP request headers matching the given TLSMode ("chrome_auto" or "safari_auto").
// Pre-conditions: None.
// Post-conditions: Returns non-empty User-Agent, Accept, Accept-Language, and Sec-Fetch-* headers matching the TLS profile.
func BrowserHeaders(tlsMode string) http.Header {
	h := make(http.Header)
	switch strings.ToLower(strings.TrimSpace(tlsMode)) {
	case "safari_auto":
		h.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_7_2) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.3 Safari/605.1.15")
		h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
		h.Set("Accept-Language", "en-US,en;q=0.9")
		h.Set("Sec-Fetch-Dest", "document")
		h.Set("Sec-Fetch-Mode", "navigate")
		h.Set("Sec-Fetch-Site", "none")
	case "chrome_auto", "plain", "":
		fallthrough
	default:
		h.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/133.0.0.0 Safari/537.36")
		h.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8")
		h.Set("Accept-Language", "en-US,en;q=0.9")
		h.Set("Sec-Ch-Ua", `"Not(A:Brand";v="99", "Google Chrome";v="133", "Chromium";v="133"`)
		h.Set("Sec-Ch-Ua-Mobile", "?0")
		h.Set("Sec-Ch-Ua-Platform", `"Linux"`)
		h.Set("Sec-Fetch-Dest", "document")
		h.Set("Sec-Fetch-Mode", "navigate")
		h.Set("Sec-Fetch-Site", "none")
		h.Set("Sec-Fetch-User", "?1")
		h.Set("Upgrade-Insecure-Requests", "1")
	}
	return h
}

// ParseRetryAfter parses HTTP Retry-After header values (integer seconds or RFC1123 HTTP-date), bounded by maxWait.
// Pre-conditions: now is reference UTC time; maxWait > 0.
// Post-conditions: Returns positive delay clamped to [0, maxWait] and true if valid, else (0, false).
func ParseRetryAfter(headerVal string, now time.Time, maxWait time.Duration) (time.Duration, bool) {
	trimmed := strings.TrimSpace(headerVal)
	if trimmed == "" {
		return 0, false
	}

	// Try integer seconds
	if sec, err := strconv.Atoi(trimmed); err == nil {
		if sec < 0 {
			return 0, false
		}
		delay := time.Duration(sec) * time.Second
		if maxWait > 0 && delay > maxWait {
			delay = maxWait
		}
		return delay, true
	}

	// Try HTTP-date
	if targetTime, err := http.ParseTime(trimmed); err == nil {
		delay := targetTime.Sub(now)
		if delay < 0 {
			delay = 0
		}
		if maxWait > 0 && delay > maxWait {
			delay = maxWait
		}
		return delay, true
	}

	return 0, false
}

// getTransport returns an http.RoundTripper configured for the requested TLS mode.
func (c *Client) getTransport(tlsMode string) http.RoundTripper {
	if c.transport != nil {
		return c.transport
	}

	mode := strings.ToLower(strings.TrimSpace(tlsMode))
	if mode == "" || mode == "chrome_auto" {
		mode = "chrome_auto"
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if rt, exists := c.uTransports[mode]; exists {
		return rt
	}

	var helloID utls.ClientHelloID
	switch mode {
	case "safari_auto":
		helloID = utls.HelloSafari_Auto
	case "chrome_auto":
		fallthrough
	default:
		helloID = utls.HelloChrome_Auto
	}

	if mode == "plain" {
		rt := &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          100,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
		}
		c.uTransports[mode] = rt
		return rt
	}

	// uTLS transport with HTTP/2 ALPN negotiation
	uTransport := &utlsTransport{
		helloID: helloID,
	}
	c.uTransports[mode] = uTransport
	return uTransport
}

// utlsTransport wraps net.Dialer with uTLS and handles HTTP/2 or HTTP/1.1 connections.
type utlsTransport struct {
	helloID utls.ClientHelloID
	mu      sync.Mutex
	h2Conns map[string]*http2.ClientConn
}

func (t *utlsTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "https" {
		// Non-HTTPS requests use default transport
		return http.DefaultTransport.RoundTrip(req)
	}

	host := req.URL.Hostname()
	port := req.URL.Port()
	if port == "" {
		port = "443"
	}
	addr := net.JoinHostPort(host, port)

	t.mu.Lock()
	if t.h2Conns == nil {
		t.h2Conns = make(map[string]*http2.ClientConn)
	}
	cc, hasH2 := t.h2Conns[addr]
	if hasH2 && cc.CanTakeNewRequest() {
		t.mu.Unlock()
		return cc.RoundTrip(req)
	}
	delete(t.h2Conns, addr)
	t.mu.Unlock()

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	rawConn, err := dialer.DialContext(req.Context(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("fetch: dial %s: %w", addr, err)
	}

	uConn := utls.UClient(rawConn, &utls.Config{ServerName: host}, t.helloID)
	if err := uConn.HandshakeContext(req.Context()); err != nil {
		_ = uConn.Close()
		return nil, fmt.Errorf("fetch: utls handshake with %s: %w", host, err)
	}

	state := uConn.ConnectionState()
	if state.NegotiatedProtocol == "h2" {
		t2 := &http2.Transport{}
		clientConn, err := t2.NewClientConn(uConn)
		if err != nil {
			_ = uConn.Close()
			return nil, fmt.Errorf("fetch: h2 new client conn: %w", err)
		}
		t.mu.Lock()
		t.h2Conns[addr] = clientConn
		t.mu.Unlock()
		return clientConn.RoundTrip(req)
	}

	// HTTP/1.1 fallback (single-connection transport; disable keep-alives so the ephemeral transport does not leak idle goroutines)
	h1Transport := &http.Transport{
		DisableKeepAlives: true,
		DialTLSContext: func(ctx context.Context, network, a string) (net.Conn, error) {
			return uConn, nil
		},
	}
	return h1Transport.RoundTrip(req)
}

type attemptOutcome struct {
	response    *Response
	cooldown    time.Duration
	hasCooldown bool
	retryable   bool
	err         error
}

// Fetch executes a polite HTTP fetch following redirects, rate limiting, and challenge verification.
// Pre-conditions: rawURL must be an absolute http or https URL.
// Post-conditions: Returns a valid Response or a typed error without solving or bypassing challenges.
func (c *Client) Fetch(ctx context.Context, rawURL string, opts RequestOptions) (*Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	parsedURL, err := url.Parse(strings.TrimSpace(rawURL))
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" {
		return nil, fmt.Errorf("fetch: invalid target URL %q: %w", rawURL, err)
	}

	// Apply option defaults
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	maxBodyBytes := opts.MaxBodyBytes
	if maxBodyBytes <= 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	maxRetries := opts.MaxRetries
	if maxRetries < 0 {
		maxRetries = defaultMaxRetries
	}

	domain := parsedURL.Hostname()
	rt := c.getTransport(opts.TLSMode)

	var lastErr error

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		// Rate limit spacing
		if c.limiter != nil {
			if err := c.limiter.Wait(ctx, domain, opts.MinInterval); err != nil {
				return nil, err
			}
		}

		out := c.fetchAttempt(ctx, rt, rawURL, domain, opts, timeout, maxBodyBytes)
		if out.err == nil {
			return out.response, nil
		}
		lastErr = out.err

		if !out.retryable {
			return nil, out.err
		}

		if attempt < maxRetries {
			if out.hasCooldown {
				if out.cooldown > 0 {
					select {
					case <-time.After(out.cooldown):
					case <-ctx.Done():
						return nil, ctx.Err()
					}
				}
			} else {
				c.backoffSleep(ctx, attempt)
			}
			continue
		}

		if out.hasCooldown {
			return nil, out.err
		}
		return nil, fmt.Errorf("fetch: failed after %d attempts: %w", attempt+1, lastErr)
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, ErrRetryableHTTP
}

// fetchAttempt keeps fetchCtx alive through reading and closing resp.Body before deferring cancel.
func (c *Client) fetchAttempt(ctx context.Context, rt http.RoundTripper, rawURL, domain string, opts RequestOptions, timeout time.Duration, maxBodyBytes int64) attemptOutcome {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	resp, finalURL, redirectErr := c.doWithRedirects(fetchCtx, rt, rawURL, opts, maxBodyBytes)
	if redirectErr != nil {
		nonRetryable := errors.Is(redirectErr, ErrChallengeDetected) ||
			errors.Is(redirectErr, ErrPaywallDetected) ||
			errors.Is(redirectErr, ErrPermanentHTTP) ||
			errors.Is(redirectErr, ErrBodyTooLarge)
		return attemptOutcome{retryable: !nonRetryable, err: redirectErr}
	}
	defer func() { _ = resp.Body.Close() }()

	if err := c.evaluateChallengeOrPaywall(resp); err != nil {
		return attemptOutcome{retryable: false, err: err}
	}

	status := resp.StatusCode

	if status == http.StatusNotFound || status == http.StatusGone {
		return attemptOutcome{
			retryable: false,
			err:       fmt.Errorf("%w: status %d on %s", ErrPermanentHTTP, status, finalURL),
		}
	}

	if status == http.StatusTooManyRequests {
		retryAfterHeader := resp.Header.Get("Retry-After")
		if delay, ok := ParseRetryAfter(retryAfterHeader, c.clock(), 120*time.Second); ok {
			if c.limiter != nil {
				c.limiter.SetCooldown(domain, c.clock().Add(delay))
			}
			return attemptOutcome{
				cooldown:    delay,
				hasCooldown: true,
				retryable:   true,
				err:         fmt.Errorf("%w: 429 too many requests on %s", ErrRetryableHTTP, finalURL),
			}
		}
		return attemptOutcome{
			retryable: true,
			err:       fmt.Errorf("%w: 429 too many requests on %s", ErrRetryableHTTP, finalURL),
		}
	}

	if status >= 500 && status <= 599 {
		return attemptOutcome{
			retryable: true,
			err:       fmt.Errorf("%w: status %d on %s", ErrRetryableHTTP, status, finalURL),
		}
	}

	bodyBytes, err := readBoundedBody(resp.Body, maxBodyBytes)
	if err != nil {
		return attemptOutcome{retryable: true, err: err}
	}

	if err := inspectBodyForChallenges(bodyBytes); err != nil {
		return attemptOutcome{retryable: false, err: err}
	}

	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return attemptOutcome{
			retryable: false,
			err:       fmt.Errorf("%w: status %d on %s", ErrPaywallDetected, status, finalURL),
		}
	}

	return attemptOutcome{
		response: &Response{
			FinalURL:   finalURL,
			StatusCode: status,
			Header:     resp.Header,
			Body:       bodyBytes,
		},
	}
}

// doWithRedirects executes an HTTP request following up to maxRedirectHops while applying headers.
func (c *Client) doWithRedirects(ctx context.Context, rt http.RoundTripper, targetURL string, opts RequestOptions, maxBodyBytes int64) (*http.Response, string, error) {
	currentURL := targetURL
	var resp *http.Response

	for hop := 0; hop < maxRedirectHops; hop++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, currentURL, nil)
		if err != nil {
			return nil, currentURL, fmt.Errorf("fetch: create request: %w", err)
		}

		// Apply browser headers
		baseHeaders := BrowserHeaders(opts.TLSMode)
		for k, vals := range baseHeaders {
			for _, v := range vals {
				req.Header.Add(k, v)
			}
		}

		// Apply user custom headers
		for k, v := range opts.Headers {
			req.Header.Set(k, v)
		}

		resp, err = rt.RoundTrip(req)
		if err != nil {
			return nil, currentURL, err
		}

		// Check challenge headers on intermediate response
		if err := c.evaluateChallengeOrPaywall(resp); err != nil {
			_ = resp.Body.Close()
			return nil, currentURL, err
		}

		// Handle redirects (exclude 304 Not Modified)
		if resp.StatusCode == http.StatusMovedPermanently ||
			resp.StatusCode == http.StatusFound ||
			resp.StatusCode == http.StatusSeeOther ||
			resp.StatusCode == http.StatusTemporaryRedirect ||
			resp.StatusCode == http.StatusPermanentRedirect {
			loc := resp.Header.Get("Location")
			_ = resp.Body.Close()
			if loc == "" {
				return nil, currentURL, fmt.Errorf("fetch: redirect status %d with empty Location", resp.StatusCode)
			}
			nextURL, err := req.URL.Parse(loc)
			if err != nil {
				return nil, currentURL, fmt.Errorf("fetch: parse redirect URL %q: %w", loc, err)
			}
			currentURL = nextURL.String()
			continue
		}

		return resp, currentURL, nil
	}

	if resp != nil {
		_ = resp.Body.Close()
	}
	return nil, currentURL, fmt.Errorf("fetch: exceeded max redirect hops (%d)", maxRedirectHops)
}

// evaluateChallengeOrPaywall inspects status and headers for anti-bot or paywall gates.
func (c *Client) evaluateChallengeOrPaywall(resp *http.Response) error {
	if resp == nil {
		return nil
	}

	// Cloudflare mitigation header
	if strings.EqualFold(resp.Header.Get("cf-mitigated"), "challenge") {
		return ErrChallengeDetected
	}

	// HTTP 402 Payment Required
	if resp.StatusCode == http.StatusPaymentRequired {
		return ErrPaywallDetected
	}

	return nil
}

// inspectBodyForChallenges reads initial bytes of payload checking for Cloudflare Turnstile, CAPTCHA, or paywalls.
func (c *Client) inspectBodyForChallenges(body []byte) error {
	return inspectBodyForChallenges(body)
}

func inspectBodyForChallenges(body []byte) error {
	limit := 8192
	if len(body) < limit {
		limit = len(body)
	}
	snippet := strings.ToLower(string(body[:limit]))

	challengeMarkers := []string{
		"challenges.cloudflare.com",
		"cf-turnstile",
		"hashcash_config",
		"g-recaptcha",
		"hcaptcha",
		"<title>just a moment...</title>",
		"attention required! | cloudflare",
	}

	for _, marker := range challengeMarkers {
		if strings.Contains(snippet, marker) {
			return ErrChallengeDetected
		}
	}

	return nil
}

// readBoundedBody reads up to maxBodyBytes from r; returns ErrBodyTooLarge if exceeded.
func readBoundedBody(r io.Reader, maxBodyBytes int64) ([]byte, error) {
	limited := io.LimitReader(r, maxBodyBytes+1)
	buf := bytes.NewBuffer(make([]byte, 0, 64*1024))
	n, err := buf.ReadFrom(limited)
	if err != nil {
		return nil, fmt.Errorf("fetch: read body: %w", err)
	}
	if n > maxBodyBytes {
		return nil, ErrBodyTooLarge
	}
	return buf.Bytes(), nil
}

// backoffSleep implements exponential backoff with random jitter.
func (c *Client) backoffSleep(ctx context.Context, attempt int) {
	base := 500 * time.Millisecond
	multiplier := 1 << attempt
	delay := base * time.Duration(multiplier)
	if delay > 5*time.Second {
		delay = 5 * time.Second
	}
	jitter := time.Duration(rand.Int63n(int64(250 * time.Millisecond)))
	select {
	case <-time.After(delay + jitter):
	case <-ctx.Done():
	}
}
