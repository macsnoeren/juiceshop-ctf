// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package keeper is the root part of the juiceshop-ctf container: PID 1, and
// nothing but a supervisor.
//
//	keeper (uid 0, caps SETUID+SETGID only)
//	  ├─ Juice Shop   node build/app.js        uid 65532, no caps
//	  └─ ctf-node     /ctf-node -serve          uid 10001, no caps
//	                  gets :80 as an open socket and a control pipe
//
// The keeper starts Juice Shop, restarts it when it dies (with back-off) or
// when ctf-node asks over the pipe, and stops it when the container stops.
// It reads no network input and opens no file of the node: the part that
// talks to students and to the central server is ctf-node, which runs with
// no capabilities at all.
//
// WHY a separate root process instead of one that drops privileges: a
// process that keeps SETUID after dropping to 10001 — needed to start Juice
// Shop again later — can setuid(0) straight back; the Phase 0 spike did. And
// root without CAP_KILL may not signal another uid at all (EPERM), so Juice
// Shop is stopped through a helper the keeper starts AS 65532 (`ctf-node
// -signal <pgid> <sig>`), which may signal its own uid.
package keeper

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"
)

// Process is one running child.
type Process interface {
	// Done is closed when the process has exited.
	Done() <-chan struct{}
	// Stop asks the process to stop and returns at once; Done follows.
	Stop() error
	// Kill stops it hard.
	Kill() error
	Pid() int
}

// Launcher starts the supervised program.
type Launcher func() (Process, error)

// Supervisor keeps one program running.
type Supervisor struct {
	Launch Launcher
	Log    *slog.Logger
	// BackoffBase doubles per crash in a row, up to BackoffMax. A run that
	// lasted Stable resets the count: a Juice Shop that ran for an hour and
	// was then killed by a DoS challenge comes back at once.
	BackoffBase time.Duration
	BackoffMax  time.Duration
	Stable      time.Duration
	// Grace is how long a stopped process gets before it is killed.
	Grace time.Duration
	// sleep waits; tests replace it to see the back-off without waiting.
	sleep func(ctx context.Context, d time.Duration)

	mu      sync.Mutex
	cur     Process
	wanted  bool
	planned bool // the current exit was asked for (restart/stop): no back-off
	kick    chan struct{}
	started chan struct{} // closed and replaced on every start, for Restart
}

func (s *Supervisor) init() {
	if s.BackoffBase == 0 {
		s.BackoffBase = time.Second
	}
	if s.BackoffMax == 0 {
		s.BackoffMax = 30 * time.Second
	}
	if s.Stable == 0 {
		s.Stable = time.Minute
	}
	if s.Grace == 0 {
		s.Grace = 10 * time.Second
	}
	if s.sleep == nil {
		s.sleep = func(ctx context.Context, d time.Duration) {
			select { // a Restart cuts a back-off short
			case <-ctx.Done():
			case <-time.After(d):
			case <-s.kick:
			}
		}
	}
	if s.kick == nil {
		s.kick = make(chan struct{}, 1)
		s.started = make(chan struct{})
	}
}

// Backoff is the wait after the n-th crash in a row (n >= 1).
func (s *Supervisor) Backoff(n int) time.Duration {
	d := s.BackoffBase
	for i := 1; i < n && d < s.BackoffMax; i++ {
		d *= 2
	}
	return min(d, s.BackoffMax)
}

// Run keeps the program running until ctx is cancelled, then stops it and
// returns once it has exited: on SIGTERM the child goes first.
func (s *Supervisor) Run(ctx context.Context) {
	s.mu.Lock()
	s.init()
	s.wanted = true
	s.mu.Unlock()
	crashes := 0
	for {
		s.mu.Lock()
		wanted := s.wanted
		s.mu.Unlock()
		if !wanted {
			select {
			case <-ctx.Done():
				return
			case <-s.kick:
				continue
			}
		}
		p, err := s.Launch()
		if err != nil {
			crashes++
			d := s.Backoff(crashes)
			s.Log.Error("starting the program failed", "err", err, "retry_in", d)
			s.sleep(ctx, d)
			if ctx.Err() != nil {
				return
			}
			continue
		}
		began := time.Now()
		s.mu.Lock()
		s.cur, s.planned = p, false
		close(s.started)
		s.started = make(chan struct{})
		s.mu.Unlock()
		s.Log.Info("program started", "pid", p.Pid())

		select {
		case <-p.Done():
		case <-ctx.Done():
			s.stop(p)
			s.mu.Lock()
			s.cur = nil
			s.mu.Unlock()
			return
		}
		s.mu.Lock()
		planned := s.planned
		s.cur = nil
		s.mu.Unlock()
		if planned {
			crashes = 0
			continue
		}
		if time.Since(began) >= s.Stable {
			crashes = 0
		}
		crashes++
		d := s.Backoff(crashes)
		s.Log.Warn("program exited; starting it again", "pid", p.Pid(), "crashes_in_a_row", crashes, "after", d)
		s.sleep(ctx, d)
		if ctx.Err() != nil {
			return
		}
	}
}

// stop asks p to stop and kills it after the grace period.
func (s *Supervisor) stop(p Process) {
	if err := p.Stop(); err != nil {
		s.Log.Warn("stopping the program failed; killing it", "err", err)
		p.Kill()
	}
	select {
	case <-p.Done():
		return
	case <-time.After(s.Grace):
	}
	s.Log.Warn("the program did not stop in time; killing it", "grace", s.Grace)
	p.Kill()
	<-p.Done()
}

// ErrNotRunning is Restart without a running Run.
var ErrNotRunning = errors.New("the supervisor is not running")

// Restart stops the program and waits until a new one has been started
// (Juice Shop then starts with an empty database: it recreates it on start).
func (s *Supervisor) Restart(ctx context.Context) error {
	s.mu.Lock()
	if s.kick == nil {
		s.mu.Unlock()
		return ErrNotRunning
	}
	p, started := s.cur, s.started
	s.wanted, s.planned = true, true
	s.mu.Unlock()
	select { // in case it was stopped or is waiting out a back-off
	case s.kick <- struct{}{}:
	default:
	}
	if p != nil {
		s.stop(p)
	}
	select {
	case <-started:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
