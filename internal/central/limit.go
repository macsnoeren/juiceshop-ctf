// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package central

import (
	"sync"
	"time"
)

// window counts events per key in fixed windows of one length. It is for
// the join and login limits, where an exact sliding window buys nothing: the
// point is that a guesser gets twenty tries a minute, not two thousand.
type window struct {
	mu   sync.Mutex
	size time.Duration
	m    map[string]*bucket
	gc   time.Time
}

type bucket struct {
	start time.Time
	n     int
}

func newWindow(size time.Duration) *window { return &window{size: size, m: map[string]*bucket{}} }

func (w *window) get(key string, now time.Time) *bucket {
	if now.Sub(w.gc) > 10*w.size { // forget old keys now and then
		for k, b := range w.m {
			if now.Sub(b.start) >= w.size {
				delete(w.m, k)
			}
		}
		w.gc = now
	}
	b := w.m[key]
	if b == nil || now.Sub(b.start) >= w.size {
		b = &bucket{start: now}
		w.m[key] = b
	}
	return b
}

// Count returns the number of events for key in the current window.
func (w *window) Count(key string, now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.get(key, now).n
}

// Add counts one event for key.
func (w *window) Add(key string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.get(key, now).n++
}
