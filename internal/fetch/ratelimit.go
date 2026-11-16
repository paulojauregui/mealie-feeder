package fetch

import (
	"context"
	"math/rand"
	"strings"
	"sync"
	"time"
)

type domainState struct {
	mu          sync.Mutex
	lastRequest time.Time
	floor       time.Duration
	cooldown    time.Time
}

// DomainLimiter enforces per-domain minimum request pacing, optional robots.txt Crawl-delay floors,
// and dynamic Retry-After cooldown windows across concurrent callers.
// Pre-conditions: Must be constructed via NewDomainLimiter; safe for concurrent use across goroutines.
// Post-conditions: Guarantees consecutive requests to the same domain never violate max(minInterval, floor) or cooldown until.
type DomainLimiter struct {
	mu        sync.Mutex
	domains   map[string]*domainState
	jitterMax time.Duration
	now       func() time.Time
}

// NewDomainLimiter creates a DomainLimiter with optional random jitter up to jitterMax and injectable clock (nil uses time.Now).
// Pre-conditions: jitterMax >= 0.
// Post-conditions: Ready for concurrent Wait, SetFloor, and SetCooldown calls.
func NewDomainLimiter(jitterMax time.Duration, now func() time.Time) *DomainLimiter {
	if jitterMax < 0 {
		jitterMax = 0
	}
	if now == nil {
		now = time.Now
	}
	return &DomainLimiter{
		domains:   make(map[string]*domainState),
		jitterMax: jitterMax,
		now:       now,
	}
}

// getDomainState retrieves or instantiates the pacing state for domain.
func (l *DomainLimiter) getDomainState(domain string) *domainState {
	norm := strings.ToLower(strings.TrimSpace(domain))
	l.mu.Lock()
	defer l.mu.Unlock()

	ds, exists := l.domains[norm]
	if !exists {
		ds = &domainState{}
		l.domains[norm] = ds
	}
	return ds
}

// SetFloor sets or raises the minimum delay floor (e.g. from robots.txt Crawl-delay) for domain.
// Pre-conditions: domain non-empty, floor >= 0.
// Post-conditions: Future Wait calls for domain honor at least floor.
func (l *DomainLimiter) SetFloor(domain string, floor time.Duration) {
	if strings.TrimSpace(domain) == "" || floor <= 0 {
		return
	}
	ds := l.getDomainState(domain)
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if floor > ds.floor {
		ds.floor = floor
	}
}

// SetCooldown pushes back the earliest permitted request timestamp for domain (e.g. from HTTP 429 Retry-After).
// Pre-conditions: domain non-empty.
// Post-conditions: Future Wait calls sleep until at least `until` unless ctx is cancelled.
func (l *DomainLimiter) SetCooldown(domain string, until time.Time) {
	if strings.TrimSpace(domain) == "" {
		return
	}
	ds := l.getDomainState(domain)
	ds.mu.Lock()
	defer ds.mu.Unlock()
	if until.After(ds.cooldown) {
		ds.cooldown = until
	}
}

// Wait blocks until domain is permitted to execute one request at minInterval (or floor/cooldown if larger).
// Pre-conditions: ctx non-nil, domain non-empty, minInterval >= 0.
// Post-conditions: Returns nil once token/slot is acquired, or ctx.Err() if cancelled while waiting.
func (l *DomainLimiter) Wait(ctx context.Context, domain string, minInterval time.Duration) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(domain) == "" {
		return nil
	}

	ds := l.getDomainState(domain)
	ds.mu.Lock()
	defer ds.mu.Unlock()

	effectiveInterval := minInterval
	if ds.floor > effectiveInterval {
		effectiveInterval = ds.floor
	}

	var jitter time.Duration
	if l.jitterMax > 0 {
		jitter = time.Duration(rand.Int63n(int64(l.jitterMax)))
	}

	now := l.now()
	var earliest time.Time

	if !ds.lastRequest.IsZero() {
		earliest = ds.lastRequest.Add(effectiveInterval + jitter)
	}
	if !ds.cooldown.IsZero() && ds.cooldown.After(earliest) {
		earliest = ds.cooldown
	}

	if earliest.After(now) {
		delay := earliest.Sub(now)
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	ds.lastRequest = l.now()
	return nil
}
