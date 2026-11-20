// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Command ctf-node is the CTF half of the juiceshop-ctf container: the student's page, the proxy to Juice
// Shop, the watcher and the reporter, in one binary that plays three parts:
//
//	ctf-node                  as root: the KEEPER (PID 1). Binds :80 and :8080, starts
//	                          Juice Shop as 65532 and itself as `-serve` as
//	                          10001, restarts Juice Shop on request.
//	ctf-node -serve           as 10001, no capabilities: everything else.
//	ctf-node -signal P S      a helper the keeper runs as 65532 to signal
//	                          Juice Shop's process group P with S.
//	ctf-node -healthcheck     for the image's HEALTHCHECK
//	ctf-node -version
//
// No embedded tzdata: the Juice Shop image (distroless) has
// /usr/share/zoneinfo, and leaving it out keeps the image within 20 MB of
// Juice Shop's own.
//
// Started as a normal user (outside the image, for development) it runs
// -serve directly on CTF_ADDR and CTF_PAGE_ADDR, against a Juice Shop it
// does not supervise.
//
// Configuration is environment only, and NOTHING in it is a secret: the
// image is the same for every student, and a VM's identity is made when the
// student joins.
//
//	CTF_CENTRAL_URL     the central server, e.g. https://ctf.example.org;
//	                    empty: none. The student page can change it before joining.
//	CTF_GATE            "session" for the optional gate; default: none
//	CTF_DATA_DIR        default /var/lib/ctf (node.db; 0700, uid 10001)
//	CTF_ADDR            default :80, the shop (/juiceshop/)
//	CTF_PAGE_ADDR       default :8080, the student's page (/ctf/): ANOTHER ORIGIN than
//	                    the shop, so script in the shop cannot use it (internal/node/page.go)
//	CTF_SHOP_PORT       the port a browser uses for the shop, for the links (default: CTF_ADDR's)
//	CTF_PAGE_PORT       the same for the page (default: CTF_PAGE_ADDR's); set both behind
//	                    a port mapping such as the test setup's
//	CTF_TIMEZONE        default Europe/Amsterdam, for the times on the page
//	JUICESHOP_URL       default http://127.0.0.1:3000
//	POLL_INTERVAL       default 30s
//	REPORT_INTERVAL     default 60s
//	REPORT_BACKOFF_MAX  default 5m
//	LOG_LEVEL           debug | info | warn | error
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/config"
	"github.com/macsnoeren/juiceshop-ctf/internal/health"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/node"
	"github.com/macsnoeren/juiceshop-ctf/internal/node/keeper"
	"github.com/macsnoeren/juiceshop-ctf/internal/version"
)

// webhookAddr is where Juice Shop's SOLUTIONS_WEBHOOK points: loopback, in
// the network namespace Juice Shop shares with ctf-node.
const webhookAddr = "127.0.0.1:8081"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-signal" {
		os.Exit(keeper.RunSignalHelper(os.Args[2:]))
	}
	serve := flag.Bool("serve", false, "run as ctf-node proper (started by the keeper)")
	healthcheck := flag.Bool("healthcheck", false, "check the running node and exit 0 or 1")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	env := config.FromOS()
	addr := env.String("CTF_ADDR", ":80")
	pageAddr := env.String("CTF_PAGE_ADDR", ":8080")
	if *showVersion {
		fmt.Println("ctf-node", version.Version)
		return
	}
	if *healthcheck {
		if err := health.Check(pageAddr, "/ctf/healthz"); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	log := httpx.NewLogger(env.String("LOG_LEVEL", "info"))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if !*serve && os.Getuid() == 0 {
		log.Info("ctf-node keeper starting", "version", version.Version)
		os.Exit(keeper.Run(ctx, keeper.Config{
			Addr:     addr,
			PageAddr: pageAddr,
			Log:      log.With("part", "keeper"),
			JuiceShopEnv: []string{
				"PATH=/usr/local/bin:/usr/bin:/bin",
				"SSL_CERT_FILE=/etc/ssl/certs/ca-certificates.crt",
				"HOME=/tmp",
				// Only for the redirects Juice Shop builds itself; the
				// proxy strips the prefix on the way in.
				"BASE_PATH=/juiceshop",
				"SOLUTIONS_WEBHOOK=http://" + webhookAddr + "/webhook",
			},
			NodeEnv: append(passThrough(), "CTF_KEEPER=1"),
		}))
	}
	os.Exit(runServe(ctx, stop, env, addr, pageAddr, log))
}

// portOf is the port of a listen address such as ":8080".
func portOf(addr string) int {
	_, p, err := net.SplitHostPort(addr)
	if err != nil {
		return 0
	}
	n, _ := strconv.Atoi(p)
	return n
}

// passThrough is the part of the environment ctf-node -serve gets.
func passThrough() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CTF_") || strings.HasPrefix(k, "POLL_") || strings.HasPrefix(k, "REPORT_") ||
			k == "JUICESHOP_URL" || k == "LOG_LEVEL" || k == "TZ" || k == "SSL_CERT_FILE" {
			out = append(out, kv)
		}
	}
	return out
}

func runServe(ctx context.Context, stop func(), env *config.Env, addr, pageAddr string, log *slog.Logger) int {
	underKeeper := os.Getenv("CTF_KEEPER") == "1"
	log = log.With("part", "node")
	if underKeeper {
		// The point of the keeper: this process, which talks to the
		// network, holds no privilege at all.
		caps, err := keeper.Capabilities()
		if os.Getuid() == 0 || os.Geteuid() == 0 || err != nil || strings.Trim(caps, "0") != "" {
			log.Error("REFUSING TO START: ctf-node must run as an unprivileged user with no capabilities", "uid", os.Getuid(), "caps", caps)
			return 1
		}
	}
	syscall.Umask(0o077)

	cfg := node.Config{
		DefaultCentralURL: env.String("CTF_CENTRAL_URL", ""),
		Version:           version.Version,
		Gate:              env.String("CTF_GATE", "") == "session",
		PollInterval:      env.Duration("POLL_INTERVAL", 0),
		ReportInterval:    env.Duration("REPORT_INTERVAL", 0),
		BackoffMax:        env.Duration("REPORT_BACKOFF_MAX", 0),
	}
	dataDir := env.String("CTF_DATA_DIR", "/var/lib/ctf")
	jsURL := env.String("JUICESHOP_URL", "http://127.0.0.1:3000")
	tz := env.String("CTF_TIMEZONE", "Europe/Amsterdam")
	shopPort := env.Int("CTF_SHOP_PORT", portOf(addr))
	pagePort := env.Int("CTF_PAGE_PORT", portOf(pageAddr))
	if v := env.String("CTF_GATE", ""); v != "" && v != "session" {
		log.Error(`CTF_GATE must be "session" or empty`)
		return 2
	}
	if err := env.Err(); err != nil {
		log.Error(err.Error())
		return 2
	}
	if cfg.DefaultCentralURL != "" {
		if _, err := node.ValidateCentralURL(cfg.DefaultCentralURL); err != nil {
			log.Error("CTF_CENTRAL_URL", "err", err)
			return 2
		}
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		log.Error("CTF_TIMEZONE", "err", err)
		return 2
	}
	target, err := url.Parse(jsURL)
	if err != nil {
		log.Error("JUICESHOP_URL", "err", err)
		return 2
	}

	db, err := node.OpenStore(filepath.Join(dataDir, "node.db"))
	if err != nil {
		log.Error("cannot open node.db", "err", err)
		return 1
	}
	defer db.Close()

	var k node.Keeper
	var ln, pageLn net.Listener
	if underKeeper {
		ln, err = net.FileListener(os.NewFile(keeper.ListenerFD, "listener"))
		if err == nil {
			pageLn, err = net.FileListener(os.NewFile(keeper.PageListenerFD, "page-listener"))
		}
		if err != nil {
			log.Error("the listeners from the keeper", "err", err)
			return 1
		}
		k = keeper.NewClient(os.NewFile(keeper.ControlFD, "control"), func() {
			log.Info("the keeper closed the control pipe: stopping")
			stop()
		})
	} else {
		if ln, err = net.Listen("tcp", addr); err != nil {
			log.Error("cannot listen", "addr", addr, "err", err)
			return 1
		}
		if pageLn, err = net.Listen("tcp", pageAddr); err != nil {
			log.Error("cannot listen", "addr", pageAddr, "err", err)
			return 1
		}
		log.Warn("running without the keeper: Restart Juice Shop and the restart after Forget are not available")
	}

	n, err := node.New(cfg, juiceshop.New(jsURL), db, k, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	if count, err := n.ImportV1(filepath.Join(dataDir, "state.json")); err != nil {
		log.Error("importing the 1.0 state failed", "err", err)
	} else if count >= 0 {
		log.Info("imported a 1.0 state: solves and continue codes, not the registration", "solves", count)
	}
	srv, err := node.NewServer(n, node.PageConfig{Location: loc, JuiceShop: node.NewProxy(target, log), ShopPort: shopPort, PagePort: pagePort})
	if err != nil {
		log.Error("cannot start", "err", err)
		return 1
	}
	central := cfg.DefaultCentralURL
	if central == "" {
		central = "(none)"
	}
	log.Info("ctf-node starting", "version", version.Version, "uid", os.Getuid(), "central_default", central,
		"gate", cfg.Gate, "juiceshop", jsURL, "shop", addr, "page", pageAddr)

	web := httpx.NewServer(addr, srv.Shop())
	web.WriteTimeout = 0 // the WebSocket of socket.io, and slow sqlmap runs
	web.ReadTimeout = 0
	page := httpx.NewServer(pageAddr, srv.Page())
	hook := httpx.NewServer(webhookAddr, n.WebhookHandler())
	errc := make(chan error, 3)
	serve := func(s *http.Server, l net.Listener) {
		err := s.Serve(l)
		if err == http.ErrServerClosed {
			err = nil
		}
		errc <- err
	}
	go serve(web, ln)
	go serve(page, pageLn)
	go func() { errc <- httpx.Serve(ctx, hook, log) }()
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		web.Shutdown(sctx)
		page.Shutdown(sctx)
	}()
	done := make(chan struct{})
	go func() { n.Run(ctx); close(done) }()
	code := 0
	select {
	case <-ctx.Done():
	case err := <-errc:
		if err != nil {
			log.Error("a server stopped", "err", err)
			code = 1
		}
		stop()
	}
	<-done
	return code
}
