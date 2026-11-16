package fetch

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestDomainLimiter(t *testing.T) {
	t.Parallel()

	t.Run("enforces minInterval pacing", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		limiter := NewDomainLimiter(0, nil)

		start := time.Now()
		for i := 0; i < 3; i++ {
			if err := limiter.Wait(ctx, "example.com", 20*time.Millisecond); err != nil {
				t.Fatalf("Wait failed: %v", err)
			}
		}
		elapsed := time.Since(start)
		if elapsed < 35*time.Millisecond {
			t.Errorf("elapsed = %v, expected at least 35ms", elapsed)
		}
	})

	t.Run("enforces SetFloor", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		limiter := NewDomainLimiter(0, nil)
		limiter.SetFloor("slow.com", 30*time.Millisecond)

		start := time.Now()
		_ = limiter.Wait(ctx, "slow.com", 5*time.Millisecond)
		_ = limiter.Wait(ctx, "slow.com", 5*time.Millisecond)
		elapsed := time.Since(start)
		if elapsed < 25*time.Millisecond {
			t.Errorf("elapsed = %v, expected floor of >= 25ms", elapsed)
		}
	})

	t.Run("respects context cancellation immediately", func(t *testing.T) {
		t.Parallel()
		limiter := NewDomainLimiter(0, nil)
		limiter.SetCooldown("cooldown.com", time.Now().Add(10*time.Second))

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()

		start := time.Now()
		err := limiter.Wait(ctx, "cooldown.com", 10*time.Millisecond)
		elapsed := time.Since(start)

		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected DeadlineExceeded, got %v", err)
		}
		if elapsed > 100*time.Millisecond {
			t.Errorf("Wait took too long to cancel: %v", elapsed)
		}
	})

	t.Run("concurrent goroutines paced safely", func(t *testing.T) {
		t.Parallel()
		ctx := context.Background()
		limiter := NewDomainLimiter(0, nil)

		const workers = 4
		var wg sync.WaitGroup
		wg.Add(workers)

		start := time.Now()
		for w := 0; w < workers; w++ {
			go func() {
				defer wg.Done()
				_ = limiter.Wait(ctx, "shared.com", 15*time.Millisecond)
			}()
		}
		wg.Wait()
		elapsed := time.Since(start)
		// 4 workers sequentially acquiring 15ms tokens should take >= 40ms
		if elapsed < 35*time.Millisecond {
			t.Errorf("elapsed = %v, expected >= 35ms", elapsed)
		}
	})
}

func TestBrowserHeaders(t *testing.T) {
	t.Parallel()

	chrome := BrowserHeaders("chrome_auto")
	safari := BrowserHeaders("safari_auto")

	if chrome.Get("User-Agent") == "" || safari.Get("User-Agent") == "" {
		t.Fatal("User-Agent must not be empty")
	}
	if chrome.Get("User-Agent") == safari.Get("User-Agent") {
		t.Error("Chrome and Safari profiles must produce distinct User-Agent strings")
	}
	if chrome.Get("Sec-Ch-Ua") == "" {
		t.Error("Chrome profile must contain Sec-Ch-Ua")
	}
	if safari.Get("Sec-Ch-Ua") != "" {
		t.Error("Safari profile should not contain Sec-Ch-Ua")
	}
}

func TestParseRetryAfter(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

	// Integer seconds
	delay, ok := ParseRetryAfter("15", now, 60*time.Second)
	if !ok || delay != 15*time.Second {
		t.Errorf("ParseRetryAfter(15) = (%v, %v), want (15s, true)", delay, ok)
	}

	// Capped to maxWait
	delayCapped, ok := ParseRetryAfter("120", now, 30*time.Second)
	if !ok || delayCapped != 30*time.Second {
		t.Errorf("ParseRetryAfter(120) = (%v, %v), want (30s, true)", delayCapped, ok)
	}

	// HTTP-date
	dateStr := now.Add(20 * time.Second).Format(http.TimeFormat)
	delayDate, ok := ParseRetryAfter(dateStr, now, 60*time.Second)
	if !ok || delayDate != 20*time.Second {
		t.Errorf("ParseRetryAfter(date) = (%v, %v), want (20s, true)", delayDate, ok)
	}

	// Invalid
	if _, ok := ParseRetryAfter("", now, 60*time.Second); ok {
		t.Error("expected false for empty header")
	}
	if _, ok := ParseRetryAfter("not-a-number", now, 60*time.Second); ok {
		t.Error("expected false for malformed header")
	}
}

func TestClientFetch(t *testing.T) {
	t.Parallel()

	t.Run("200 OK following redirects", func(t *testing.T) {
		t.Parallel()
		var handler http.HandlerFunc = func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirect" {
				http.Redirect(w, r, "/final-destination", http.StatusMovedPermanently)
				return
			}
			if r.URL.Path == "/final-destination" {
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte("<html>Recipe Content</html>"))
				return
			}
			http.NotFound(w, r)
		}
		srv := httptest.NewServer(handler)
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		resp, err := client.Fetch(context.Background(), srv.URL+"/redirect", RequestOptions{})
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
		}
		if string(resp.Body) != "<html>Recipe Content</html>" {
			t.Errorf("Body = %q", string(resp.Body))
		}
		if resp.FinalURL != srv.URL+"/final-destination" {
			t.Errorf("FinalURL = %q, want %q", resp.FinalURL, srv.URL+"/final-destination")
		}
	})

	t.Run("304 Not Modified pass-through", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("If-None-Match") == `"etag123"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("full"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		resp, err := client.Fetch(context.Background(), srv.URL+"/feed", RequestOptions{
			Headers: map[string]string{"If-None-Match": `"etag123"`},
		})
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
		if resp.StatusCode != http.StatusNotModified {
			t.Errorf("StatusCode = %d, want 304", resp.StatusCode)
		}
	})

	t.Run("429 retry after succeeds on second attempt", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := atomic.AddInt32(&count, 1)
			if c == 1 {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("ok after retry"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		resp, err := client.Fetch(context.Background(), srv.URL+"/rate-limited", RequestOptions{
			MaxRetries: 2,
		})
		if err != nil {
			t.Fatalf("Fetch failed: %v", err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Errorf("StatusCode = %d, want 200", resp.StatusCode)
		}
		if atomic.LoadInt32(&count) != 2 {
			t.Errorf("request count = %d, want 2", count)
		}
	})

	t.Run("cf-mitigated challenge fails immediately with zero retries", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			w.Header().Set("cf-mitigated", "challenge")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("challenge page"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/challenge", RequestOptions{
			MaxRetries: 3,
		})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
		if !errors.Is(err, ErrChallengeDetected) {
			t.Errorf("err = %v, want ErrChallengeDetected", err)
		}
		if atomic.LoadInt32(&count) != 1 {
			t.Errorf("request count = %d, want exactly 1 (zero retries)", count)
		}
	})

	t.Run("turnstile body marker fails immediately with zero retries", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`<html><script src="https://challenges.cloudflare.com/turnstile/v0/api.js"></script></html>`))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/turnstile", RequestOptions{
			MaxRetries: 3,
		})
		if !errors.Is(err, ErrChallengeDetected) {
			t.Errorf("err = %v, want ErrChallengeDetected", err)
		}
		if atomic.LoadInt32(&count) != 1 {
			t.Errorf("request count = %d, want exactly 1 (zero retries)", count)
		}
	})

	t.Run("402 payment required returns ErrPaywallDetected with zero retries", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			w.WriteHeader(http.StatusPaymentRequired)
			_, _ = w.Write([]byte("subscription required"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/paywall", RequestOptions{
			MaxRetries: 3,
		})
		if !errors.Is(err, ErrPaywallDetected) {
			t.Errorf("err = %v, want ErrPaywallDetected", err)
		}
		if atomic.LoadInt32(&count) != 1 {
			t.Errorf("request count = %d, want 1", count)
		}
	})

	t.Run("404 not found returns ErrPermanentHTTP with zero retries", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			http.NotFound(w, r)
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/missing", RequestOptions{
			MaxRetries: 3,
		})
		if !errors.Is(err, ErrPermanentHTTP) {
			t.Errorf("err = %v, want ErrPermanentHTTP", err)
		}
		if atomic.LoadInt32(&count) != 1 {
			t.Errorf("request count = %d, want 1", count)
		}
	})

	t.Run("body exceeding MaxBodyBytes returns ErrBodyTooLarge", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("12345678901234567890")) // 20 bytes
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/large", RequestOptions{
			MaxBodyBytes: 10,
		})
		if !errors.Is(err, ErrBodyTooLarge) {
			t.Errorf("err = %v, want ErrBodyTooLarge", err)
		}
	})
}

func TestNewClientAndTransports(t *testing.T) {
	t.Parallel()

	limiter := NewDomainLimiter(-1, nil) // tests jitterMax < 0 guard
	client := NewClient(limiter, nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}

	// Test transport resolution for various modes
	rtPlain := client.getTransport("plain")
	if rtPlain == nil {
		t.Error("expected non-nil plain transport")
	}
	rtChrome := client.getTransport("chrome_auto")
	if rtChrome == nil {
		t.Error("expected non-nil chrome_auto transport")
	}
	rtSafari := client.getTransport("safari_auto")
	if rtSafari == nil {
		t.Error("expected non-nil safari_auto transport")
	}
}

func TestClientFetchErrorsAndRetries(t *testing.T) {
	t.Parallel()

	t.Run("invalid URL returns error", func(t *testing.T) {
		t.Parallel()
		client := NewClient(NewDomainLimiter(0, nil), nil)
		_, err := client.Fetch(context.Background(), "invalid-url", RequestOptions{})
		if err == nil {
			t.Fatal("expected error for invalid URL, got nil")
		}
	})

	t.Run("500 internal server error retries exhausted", func(t *testing.T) {
		t.Parallel()
		var count int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&count, 1)
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/500", RequestOptions{
			MaxRetries: 1,
		})
		if !errors.Is(err, ErrRetryableHTTP) {
			t.Errorf("expected ErrRetryableHTTP, got %v", err)
		}
		if atomic.LoadInt32(&count) != 2 {
			t.Errorf("request count = %d, want 2", count)
		}
	})

	t.Run("limiter empty domain or nil context", func(t *testing.T) {
		t.Parallel()
		limiter := NewDomainLimiter(0, nil)
		limiter.SetFloor("", 10*time.Millisecond)
		limiter.SetCooldown("", time.Now())
		if err := limiter.Wait(nil, "", 0); err != nil {
			t.Errorf("expected nil for empty domain, got %v", err)
		}
	})

	t.Run("streaming body completes without premature context cancellation", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			flusher, ok := w.(http.Flusher)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("part1-"))
			if ok {
				flusher.Flush()
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(15 * time.Millisecond):
			}
			_, _ = w.Write([]byte("part2"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		resp, err := client.Fetch(context.Background(), srv.URL+"/stream", RequestOptions{})
		if err != nil {
			t.Fatalf("Fetch failed on streaming body: %v", err)
		}
		if string(resp.Body) != "part1-part2" {
			t.Errorf("Body = %q, want %q", string(resp.Body), "part1-part2")
		}
	})

	t.Run("403 without challenge markers returns ErrPaywallDetected", func(t *testing.T) {
		t.Parallel()
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte("access denied"))
		}))
		defer srv.Close()

		client := NewClientWithTransport(NewDomainLimiter(0, nil), srv.Client().Transport, nil)
		_, err := client.Fetch(context.Background(), srv.URL+"/forbidden", RequestOptions{MaxRetries: 2})
		if !errors.Is(err, ErrPaywallDetected) {
			t.Errorf("err = %v, want ErrPaywallDetected", err)
		}
	})
}
