package status

import (
	"sync"
	"time"
)

// Limiter allows max attempts per window per key (DESIGN.md §5.3: five
// access-code attempts per minute per client address). It is a fixed window
// in memory: a restart of ctf-status forgets it, which costs an attacker a
// restart they cannot cause, and keeps the status page stateless.
//
// EVERY attempt counts, right or wrong. Otherwise the sixth guess could be
// made "for free" as soon as one of the first five was the right code, and
// the limit would stop counting exactly when it matters.
type Limiter struct {
	max    int
	window time.Duration
	now    func() time.Time

	mu   sync.Mutex
	hits map[string]*bucket
}

type bucket struct {
	start time.Time
	n     int
}

// NewLimiter returns a limiter of max attempts per window.
func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{max: max, window: window, now: time.Now, hits: map[string]*bucket{}}
}

// Allow records an attempt for key and reports whether it is within the limit.
func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.hits[key]
	if b == nil || now.Sub(b.start) >= l.window {
		if len(l.hits) > 10000 {
			l.sweep(now)
		}
		b = &bucket{start: now}
		l.hits[key] = b
	}
	b.n++
	return b.n <= l.max
}

func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.hits {
		if now.Sub(b.start) >= l.window {
			delete(l.hits, k)
		}
	}
}
