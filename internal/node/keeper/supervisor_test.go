// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package keeper

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// fakeProc is a child that exits when told to, or when it "crashes".
type fakeProc struct {
	pid     int
	done    chan struct{}
	once    sync.Once
	stopped bool
	mu      sync.Mutex
}

func (p *fakeProc) Done() <-chan struct{} { return p.done }
func (p *fakeProc) Pid() int              { return p.pid }
func (p *fakeProc) exit()                 { p.once.Do(func() { close(p.done) }) }
func (p *fakeProc) Stop() error {
	p.mu.Lock()
	p.stopped = true
	p.mu.Unlock()
	p.exit()
	return nil
}
func (p *fakeProc) Kill() error { p.exit(); return nil }

type fakeLauncher struct {
	mu     sync.Mutex
	procs  []*fakeProc
	starts chan *fakeProc
}

func (l *fakeLauncher) launch() (Process, error) {
	l.mu.Lock()
	p := &fakeProc{pid: 100 + len(l.procs), done: make(chan struct{})}
	l.procs = append(l.procs, p)
	l.mu.Unlock()
	l.starts <- p
	return p, nil
}

func newSup(t *testing.T) (*Supervisor, *fakeLauncher, *[]time.Duration, *sync.Mutex) {
	l := &fakeLauncher{starts: make(chan *fakeProc, 10)}
	var waits []time.Duration
	var mu sync.Mutex
	s := &Supervisor{Launch: l.launch, Log: slog.New(slog.NewTextHandler(io.Discard, nil)), Grace: time.Second}
	s.sleep = func(ctx context.Context, d time.Duration) {
		mu.Lock()
		waits = append(waits, d)
		mu.Unlock()
	}
	return s, l, &waits, &mu
}

func next(t *testing.T, l *fakeLauncher) *fakeProc {
	t.Helper()
	select {
	case p := <-l.starts:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no start")
		return nil
	}
}

func TestCrashIsRestartedWithBackoff(t *testing.T) {
	s, l, waits, mu := newSup(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	p := next(t, l)
	for range 3 { // three crashes in a row
		p.exit()
		p = next(t, l)
	}
	mu.Lock()
	got := append([]time.Duration(nil), (*waits)...)
	mu.Unlock()
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Errorf("back-off %v, want %v", got, want)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return")
	}
	if !p.stopped {
		t.Error("the child was not stopped before Run returned")
	}
}

func TestRestartIsNotACrash(t *testing.T) {
	s, l, waits, mu := newSup(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	p := next(t, l)
	errc := make(chan error, 1)
	go func() { errc <- s.Restart(context.Background()) }()
	q := next(t, l)
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !p.stopped || q == p {
		t.Error("restart did not stop the old child and start a new one")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*waits) != 0 {
		t.Errorf("a requested restart waited %v", *waits)
	}
}

func TestBackoffIsCapped(t *testing.T) {
	s := &Supervisor{}
	s.init()
	if s.Backoff(1) != time.Second || s.Backoff(6) != 30*time.Second || s.Backoff(60) != 30*time.Second {
		t.Errorf("backoff %v %v %v", s.Backoff(1), s.Backoff(6), s.Backoff(60))
	}
}
