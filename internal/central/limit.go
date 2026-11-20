// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package central

import (
	"sync"
	"time"
)

// window counts events per key in fixed windows of one length. It is for
// the join, login and event limits, where an exact sliding window buys
// nothing: the point is that a guesser gets twenty tries a minute, not two
// thousand.
//
// >>> Where the work between "may I?" and "count it" is slow (a password
// hash), use Take, never Count followed by Add: requests sent in parallel
// would all pass the Count before the first Add, and the limit would be
// "twenty, plus however many fit in one hash time".
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

// Take counts one event for key if fewer than limit are counted in the
// current window, in one step, and reports whether it did.
func (w *window) Take(key string, now time.Time, limit int) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.get(key, now)
	if b.n >= limit {
		return false
	}
	b.n++
	return true
}

// Release gives back one event taken in the current window, for an attempt
// that turned out not to count (a login that succeeded).
func (w *window) Release(key string, now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if b := w.get(key, now); b.n > 0 {
		b.n--
	}
}

// Next counts one event for key and returns how many there are now in the
// window, this one included.
func (w *window) Next(key string, now time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	b := w.get(key, now)
	b.n++
	return b.n
}
