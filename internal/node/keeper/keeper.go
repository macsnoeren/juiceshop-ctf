// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package keeper

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The users inside the container. 65532 is Juice
// Shop's own (the distroless "nonroot"), and owns its files in the image;
// 10001 owns /var/lib/ctf, mode 0700.
const (
	JuiceShopUID = 65532
	NodeUID      = 10001
)

// The file descriptors ctf-node -serve inherits from the keeper.
const (
	ListenerFD = 3 // the listening socket of port 80
	ControlFD  = 4 // the control pipe: "restart\n" → "ok\n" | "error …\n"
)

// Config is what the keeper needs.
type Config struct {
	Addr         string   // ":80"
	JuiceShopEnv []string // BASE_PATH, SOLUTIONS_WEBHOOK, …
	NodeEnv      []string // the environment ctf-node -serve gets
	Log          *slog.Logger
}

// Run is the keeper: bind the port, start Juice Shop and ctf-node, serve
// the control pipe, and on SIGTERM stop ctf-node and then Juice Shop. It
// returns the exit code. It refuses to run as anything but root, and fails
// loudly when it cannot start ctf-node as 10001 (no SETUID/SETGID): a
// ctf-node that kept root would defeat the point.
func Run(ctx context.Context, cfg Config) int {
	log := cfg.Log
	if os.Getuid() != 0 {
		log.Error("the keeper must start as root (it starts Juice Shop and ctf-node as their own users)")
		return 1
	}
	reaper := NewReaper()
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		log.Error("cannot listen", "addr", cfg.Addr, "err", err)
		return 1
	}
	lnFile, err := ln.(*net.TCPListener).File()
	if err != nil {
		log.Error("listener", "err", err)
		return 1
	}
	ln.Close() // the dup in lnFile stays open, for the child
	pair, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM|syscall.SOCK_CLOEXEC, 0)
	if err != nil {
		log.Error("socketpair", "err", err)
		return 1
	}
	mine := os.NewFile(uintptr(pair[0]), "control")
	theirs := os.NewFile(uintptr(pair[1]), "control-child")

	sup := &Supervisor{Launch: JuiceShop(reaper, JuiceShopUID, JuiceShopUID, cfg.JuiceShopEnv), Log: log.With("program", "juice-shop")}
	jsCtx, stopJS := context.WithCancel(context.Background())
	defer stopJS()
	jsDone := make(chan struct{})
	go func() { sup.Run(jsCtx); close(jsDone) }()

	self, err := os.Executable()
	if err != nil {
		log.Error("cannot find myself", "err", err)
		return 1
	}
	node, nodeDone, err := reaper.Start(self, []string{"-serve"}, "/", cfg.NodeEnv, NodeUID, NodeUID, []*os.File{lnFile, theirs})
	if err != nil {
		log.Error("CANNOT START ctf-node AS UID 10001: the container needs the SETUID and SETGID capabilities (and nothing more)", "err", err)
		stopJS()
		<-jsDone
		return 1
	}
	theirs.Close()
	lnFile.Close()
	log.Info("keeper running", "juice_shop_uid", JuiceShopUID, "ctf_node_uid", NodeUID, "ctf_node_pid", node.Pid)

	go serveControl(mine, sup, log)

	code := 0
	select {
	case <-ctx.Done():
		log.Info("stopping: ctf-node first, then Juice Shop")
		mine.Close() // ctf-node exits when its control pipe closes
		select {
		case <-nodeDone:
		case <-time.After(10 * time.Second):
			log.Warn("ctf-node did not stop; killing it")
			reaper.SignalAs(node.Pid, syscall.SIGKILL, NodeUID, NodeUID)
			<-nodeDone
		}
	case <-nodeDone:
		log.Error("ctf-node exited; stopping the container so it is restarted as a whole")
		code = 1
	}
	stopJS()
	<-jsDone
	return code
}

// serveControl answers ctf-node's requests, one line each.
func serveControl(f *os.File, sup *Supervisor, log *slog.Logger) {
	in := bufio.NewScanner(f)
	for in.Scan() {
		var reply string
		switch strings.TrimSpace(in.Text()) {
		case "restart":
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := sup.Restart(ctx); err != nil {
				reply = "error " + err.Error()
			} else {
				reply = "ok"
			}
			cancel()
			log.Info("juice shop restarted at ctf-node's request", "result", reply)
		case "ping":
			reply = "ok"
		default:
			reply = "error unknown request"
		}
		if _, err := fmt.Fprintln(f, reply); err != nil {
			return
		}
	}
}

// Client is ctf-node's end of the control pipe. One goroutine reads it:
// replies go to the request that is waiting, and end of file — the keeper
// closing the pipe, its way of stopping ctf-node, which it may not signal —
// calls onClose.
type Client struct {
	mu      sync.Mutex
	f       *os.File
	replies chan string
	closed  chan struct{}
}

// NewClient wraps the inherited control pipe.
func NewClient(f *os.File, onClose func()) *Client {
	c := &Client{f: f, replies: make(chan string, 4), closed: make(chan struct{})}
	go func() {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			select {
			case c.replies <- sc.Text():
			default: // nobody waits any more (a timed-out request)
			}
		}
		close(c.closed)
		onClose()
	}()
	return c
}

// ErrNoKeeper is returned by a nil Client: ctf-node runs without the
// keeper (outside the image), and cannot restart Juice Shop.
var ErrNoKeeper = errors.New("Juice Shop cannot be restarted from here (ctf-node runs without its keeper)")

// RestartJuiceShop asks the keeper to restart Juice Shop and waits for it.
func (c *Client) RestartJuiceShop(ctx context.Context) error {
	if c == nil {
		return ErrNoKeeper
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for len(c.replies) > 0 { // a late reply to a request that timed out
		<-c.replies
	}
	if _, err := fmt.Fprintln(c.f, "restart"); err != nil {
		return err
	}
	select {
	case line := <-c.replies:
		if line = strings.TrimSpace(line); line != "ok" {
			return errors.New(strings.TrimPrefix(line, "error "))
		}
		return nil
	case <-c.closed:
		return errors.New("the keeper has gone")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Capabilities returns the effective capability mask of this process, from
// /proc/self/status, for ctf-node's check that it really has none.
func Capabilities() (string, error) {
	b, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return "", err
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "CapEff:"); ok {
			return strings.TrimSpace(v), nil
		}
	}
	return "", errors.New("no CapEff in /proc/self/status")
}
