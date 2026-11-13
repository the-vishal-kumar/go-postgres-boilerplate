package httpapi

import (
	"context"
	"sync"
	"time"
)

type window struct {
	count int
	reset time.Time
}

// Limiter is a fixed-window counter per key, held in memory. Limits are per
// process: several replicas would need a shared store.
type Limiter struct {
	limit  int
	window time.Duration
	mu     sync.Mutex
	m      map[string]*window
}

func NewLimiter(limit int, w time.Duration) *Limiter {
	return &Limiter{limit: limit, window: w, m: map[string]*window{}}
}

// Hit counts one request for key and reports whether it is within the limit,
// how many requests remain, and when the current window ends.
func (l *Limiter) Hit(key string, now time.Time) (allowed bool, remaining int, reset time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.m[key]
	if w == nil || !now.Before(w.reset) {
		w = &window{reset: now.Add(l.window)}
		l.m[key] = w
	}
	w.count++
	return w.count <= l.limit, max(l.limit-w.count, 0), w.reset
}

// Sweep drops windows that have ended and returns how many it removed.
func (l *Limiter) Sweep(now time.Time) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for k, w := range l.m {
		if !now.Before(w.reset) {
			delete(l.m, k)
			n++
		}
	}
	return n
}

// Run sweeps every interval until ctx is done, so idle clients do not pile up.
func (l *Limiter) Run(ctx context.Context, interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.Sweep(now)
		}
	}
}
