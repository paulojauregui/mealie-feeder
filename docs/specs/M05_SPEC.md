# M05 Specification: Browser-TLS HTTP Transport & Polite Rate Limiter

## 1. Objective & Target Files

Implement the browser-TLS (`refraction-networking/utls v1.8.2`) HTTP transport, coherent desktop header profiles, per-domain polite rate limiter (`golang.org/x/time/rate`), `429`/`5xx` retry backoff, and fail-closed anti-challenge / paywall detection in `internal/fetch`.

**Target Files (strict scope):**
1. `internal/fetch/ratelimit.go`
2. `internal/fetch/client.go`
3. `internal/fetch/client_test.go`

---

## 2. Package Contract (`package fetch`)

### 2.1 Rate Limiter (`internal/fetch/ratelimit.go`)

```go
package fetch

import (
	"context"
	"time"
)

// DomainLimiter enforces per-domain minimum request pacing, optional robots.txt Crawl-delay floors,
// and dynamic Retry-After cooldown windows across concurrent callers.
// Pre-conditions: Must be constructed via NewDomainLimiter; safe for concurrent use across goroutines.
// Post-conditions: Guarantees consecutive requests to the same domain never violate max(minInterval, floor) or cooldown until.
type DomainLimiter struct {
	// unexported fields: mu sync.Mutex, domains map[string]*domainState, jitterMax time.Duration, now func() time.Time
}

// NewDomainLimiter creates a DomainLimiter with optional random jitter up to jitterMax and injectable clock (nil uses time.Now).
// Pre-conditions: jitterMax >= 0.
// Post-conditions: Ready for concurrent Wait, SetFloor, and SetCooldown calls.
func NewDomainLimiter(jitterMax time.Duration, now func() time.Time) *DomainLimiter

// SetFloor sets or raises the minimum delay floor (e.g. from robots.txt Crawl-delay) for domain.
// Pre-conditions: domain non-empty, floor >= 0.
// Post-conditions: Future Wait calls for domain honor at least floor.
func (l *DomainLimiter) SetFloor(domain string, floor time.Duration)

// SetCooldown pushes back the earliest permitted request timestamp for domain (e.g. from HTTP 429 Retry-After).
// Pre-conditions: domain non-empty.
// Post-conditions: Future Wait calls sleep until at least `until` unless ctx is cancelled.
func (l *DomainLimiter) SetCooldown(domain string, until time.Time)

// Wait blocks until domain is permitted to execute one request at minInterval (or floor/cooldown if larger).
// Pre-conditions: ctx non-nil, domain non-empty, minInterval >= 0.
// Post-conditions: Returns nil once token/slot is acquired, or ctx.Err() if cancelled while waiting.
func (l *DomainLimiter) Wait(ctx context.Context, domain string, minInterval time.Duration) error
```

### 2.2 Browser-TLS Client & Anti-Challenge Guard (`internal/fetch/client.go`)

```go
package fetch

import (
	"context"
	"errors"
	"net/http"
	"time"
)

// Typed errors distinguishing policy stops (never bypass/solve) from transient vs permanent HTTP failures.
var (
	ErrChallengeDetected = errors.New("anti-bot or proof-of-work challenge detected")
	ErrPaywallDetected   = errors.New("paywall or login gate detected")
	ErrRetryableHTTP     = errors.New("retryable upstream HTTP status")
	ErrPermanentHTTP     = errors.New("permanent upstream HTTP error")
	ErrBodyTooLarge      = errors.New("response body exceeded MaxBodyBytes limit")
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
	// unexported fields
}

// NewClient creates a Client backed by limiter and optional custom RoundTripper factory (nil uses production uTLS transport).
// Pre-conditions: limiter must be non-nil.
// Post-conditions: Ready to serve Fetch calls across chrome_auto, safari_auto, and plain profiles.
func NewClient(limiter *DomainLimiter, clock func() time.Time) *Client

// NewClientWithTransport creates a Client with an injected http.RoundTripper (primarily for offline httptest verification).
// Pre-conditions: limiter and rt must be non-nil.
// Post-conditions: Executes full header injection, rate limiting, retries, and challenge detection over rt.
func NewClientWithTransport(limiter *DomainLimiter, rt http.RoundTripper, clock func() time.Time) *Client

// BrowserHeaders returns coherent HTTP request headers matching the given TLSMode ("chrome_auto" or "safari_auto").
// Pre-conditions: None.
// Post-conditions: Returns non-empty User-Agent, Accept, Accept-Language, and Sec-Fetch-* headers matching the TLS profile.
func BrowserHeaders(tlsMode string) http.Header

// ParseRetryAfter parses HTTP Retry-After header values (integer seconds or RFC1123 HTTP-date), bounded by maxWait.
// Pre-conditions: now is reference UTC time; maxWait > 0.
// Post-conditions: Returns positive delay clamped to [0, maxWait] and true if valid, else (0, false).
func ParseRetryAfter(headerVal string, now time.Time, maxWait time.Duration) (time.Duration, bool)
```

### 2.3 Security & Transport Invariants

1. **uTLS Dialer (`chrome_auto` / `safari_auto`):**
   - Uses `utls.UClient(rawConn, &utls.Config{ServerName: host}, helloID)` with `utls.HelloChrome_Auto` or `utls.HelloSafari_Auto`.
   - Inspects `uConn.ConnectionState().NegotiatedProtocol`: if `"h2"`, constructs an `http2.Transport` client connection (`http2.Transport{}.NewClientConn(uConn)`) and caches/reuses or cleans up closed connections cleanly; otherwise executes HTTP/1.1 via `net/http.Transport{DialTLSContext: ...}`.
2. **Fail-Closed Challenge & Paywall Detection (Anti-Challenge Rule):**
   - Inspects response headers (`cf-mitigated == "challenge"`) and response status/body snippets (`401`, `402`, `403`, or body containing `challenges.cloudflare.com`, `HASHCASH_CONFIG`, `cf-turnstile`, `g-recaptcha`, `hcaptcha`).
   - Immediately fails closed with `ErrChallengeDetected` or `ErrPaywallDetected` **without retrying** and without attempting to solve any challenge.
3. **Bounded Reads & Redirects:**
   - Wraps response body with `io.LimitReader(resp.Body, maxBodyBytes+1)`; returns `ErrBodyTooLarge` if exceeded.
   - Tracks redirect chain (max 10 hops) and records `FinalURL = resp.Request.URL.String()`, applying caller headers across same-host redirects cleanly.
4. **Retries:**
   - Retries only on `429` (honoring `ParseRetryAfter` and notifying `limiter.SetCooldown`) and `500/502/503/504` / transient network errors up to `opts.MaxRetries`.
   - `404` and `410` return `ErrPermanentHTTP` immediately without retrying.

---

## 3. Edge Cases & Test Plan (`internal/fetch/client_test.go`)

1. `DomainLimiter` enforces `minInterval` and `SetFloor` spacing across goroutines; respects context cancellation immediately without leaking timers.
2. `Client.Fetch` with `httptest.NewTLSServer` (using `NewClientWithTransport` over `srv.Client().Transport`) handles:
   - `200 OK` capturing `FinalURL` across `301`/`302` redirects.
   - `304 Not Modified` passed through cleanly when `If-None-Match` / `If-Modified-Since` are provided.
   - `429` with `Retry-After: 0` or `1` triggers cooldown + retry and succeeds on attempt 2.
   - `cf-mitigated: challenge` / `403` with Turnstile or `HASHCASH_CONFIG` body fails immediately on attempt 1 with `errors.Is(err, ErrChallengeDetected)` (verify server receives exact count `1` request, zero retries!).
   - `402 Payment Required` returns `ErrPaywallDetected` with zero retries.
   - `404 Not Found` returns `ErrPermanentHTTP` with zero retries.
   - Response exceeding `MaxBodyBytes` returns `ErrBodyTooLarge`.
3. `BrowserHeaders("chrome_auto")` vs `BrowserHeaders("safari_auto")` produce distinct, consistent `User-Agent` and `Sec-Ch-Ua` profiles; `BuildClientHelloID` maps `chrome_auto`, `safari_auto`, and rejects invalid profiles cleanly.

---

## 4. Verification Command

```bash
go test -v -race -cover ./internal/fetch
```
