// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package keeper

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// Reaper is the keeper's ONE waiter: as PID 1 of the container it reaps
// every child, including the orphans Juice Shop leaves behind when it is
// restarted (they are re-parented to PID 1, and would stay zombies), and it
// tells each process the keeper started when that process exited. So nothing
// in the keeper calls Wait on a process itself: it would race the reaper for
// the same exit.
type Reaper struct {
	mu      sync.Mutex
	waiting map[int]chan struct{}
	exited  map[int]time.Time // reaped before they were registered
}

// NewReaper starts reaping. Call it once, before starting any child.
func NewReaper() *Reaper {
	r := &Reaper{waiting: map[int]chan struct{}{}, exited: map[int]time.Time{}}
	ch := make(chan os.Signal, 16)
	signal.Notify(ch, syscall.SIGCHLD)
	go func() {
		tick := time.NewTicker(30 * time.Second) // a missed SIGCHLD is not fatal
		defer tick.Stop()
		for {
			select {
			case <-ch:
			case <-tick.C:
			}
			r.reap()
		}
	}()
	return r
}

func (r *Reaper) reap() {
	for {
		var ws syscall.WaitStatus
		pid, err := syscall.Wait4(-1, &ws, syscall.WNOHANG, nil)
		if pid <= 0 || err != nil {
			return
		}
		r.mu.Lock()
		if ch, ok := r.waiting[pid]; ok {
			close(ch)
			delete(r.waiting, pid)
		} else {
			r.exited[pid] = time.Now()
		}
		for p, t := range r.exited {
			if time.Since(t) > time.Minute {
				delete(r.exited, p)
			}
		}
		r.mu.Unlock()
	}
}

// done registers pid and returns a channel closed when it has exited.
func (r *Reaper) done(pid int) <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	ch := make(chan struct{})
	if _, gone := r.exited[pid]; gone {
		delete(r.exited, pid)
		close(ch)
		return ch
	}
	r.waiting[pid] = ch
	return ch
}

// Start starts a program as uid:gid in its own process group and returns it,
// reaped by r.
func (r *Reaper) Start(path string, args []string, dir string, env []string, uid, gid int, extra []*os.File) (*os.Process, <-chan struct{}, error) {
	attr := &os.ProcAttr{Dir: dir, Env: env, Files: append([]*os.File{nil, os.Stdout, os.Stderr}, extra...),
		Sys: &syscall.SysProcAttr{Setpgid: true}}
	if uid >= 0 {
		attr.Sys.Credential = &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid), Groups: []uint32{}}
	}
	// The reaper's lock is held across the start, so the child cannot be
	// reaped (and its exit lost) before it is registered.
	p, err := os.StartProcess(path, append([]string{path}, args...), attr)
	if err != nil {
		return nil, nil, err
	}
	return p, r.done(p.Pid), nil
}

// SignalAs sends sig to the process group pgid from a helper running as
// uid:gid — the only way a root without CAP_KILL can signal another uid.
// The helper is this binary: `ctf-node -signal <pgid> <signal number>`
// (RunSignalHelper).
func (r *Reaper) SignalAs(pgid int, sig syscall.Signal, uid, gid int) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	p, done, err := r.Start(self, []string{"-signal", strconv.Itoa(pgid), strconv.Itoa(int(sig))}, "/", nil, uid, gid, nil)
	if err != nil {
		return err
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		p.Kill()
		return errors.New("the signal helper did not finish")
	}
	return nil
}

// RunSignalHelper is the helper's whole program. It signals the process
// group and exits; it is never left running.
func RunSignalHelper(args []string) int {
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: ctf-node -signal <pgid> <signal>")
		return 2
	}
	pgid, err1 := strconv.Atoi(args[0])
	sig, err2 := strconv.Atoi(args[1])
	if err1 != nil || err2 != nil || pgid <= 1 {
		fmt.Fprintln(os.Stderr, "ctf-node -signal: bad arguments")
		return 2
	}
	if err := syscall.Kill(-pgid, syscall.Signal(sig)); err != nil && !errors.Is(err, syscall.ESRCH) {
		fmt.Fprintln(os.Stderr, "ctf-node -signal:", err)
		return 1
	}
	return 0
}

// child is a Process started through the Reaper.
type child struct {
	r        *Reaper
	p        *os.Process
	done     <-chan struct{}
	uid, gid int
}

func (c *child) Done() <-chan struct{} { return c.done }
func (c *child) Pid() int              { return c.p.Pid }

func (c *child) signal(sig syscall.Signal) error {
	if c.uid < 0 || os.Getuid() == c.uid {
		return syscall.Kill(-c.p.Pid, sig)
	}
	return c.r.SignalAs(c.p.Pid, sig, c.uid, c.gid)
}

func (c *child) Stop() error { return c.signal(syscall.SIGTERM) }
func (c *child) Kill() error { return c.signal(syscall.SIGKILL) }

// JuiceShop returns the Launcher for Juice Shop as uid:gid (65532 in the
// image; -1 keeps the current user, for a keeper that is not root).
func JuiceShop(r *Reaper, uid, gid int, env []string) Launcher {
	return func() (Process, error) {
		p, done, err := r.Start("/nodejs/bin/node", []string{"build/app.js"}, "/juice-shop", env, uid, gid, nil)
		if err != nil {
			return nil, err
		}
		return &child{r: r, p: p, done: done, uid: uid, gid: gid}, nil
	}
}
