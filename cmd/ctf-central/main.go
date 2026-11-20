// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Command ctf-central admits students into games, receives their VMs'
// reports and serves the boards and the operator's admin. See
// internal/central.
//
//	ctf-central                     serve
//	ctf-central -healthcheck        check the running server, exit 0 or 1
//	ctf-central -version
//
// The DATABASE LEADS: games, join codes, scoring,
// titles, the time zone and the event retention are made and changed in
// /admin/ and live in the database. The environment says only where the
// database is, where to listen and whom to believe — that is all it reads:
//
//	CTF_DB_PATH          default /data/central.db (on the ctf-central-db volume)
//	CTF_ADDR             default :8080
//	CTF_BASE_PATH        the path it is served under: default "", e.g. /juiceshop-ctf behind a proxy
//	CTF_TRUSTED_PROXIES  the peers whose X-Real-IP and X-Forwarded-Proto count: addresses and
//	                     CIDRs, comma-separated; default loopback and the private ranges (a
//	                     reverse proxy on this host or on a Docker network); "none" when the
//	                     server is reached directly, without a proxy that sets X-Real-IP
//	LOG_LEVEL            debug | info | warn | error
//
// 1.0's CTF_SEED, CTF_TITLE and CTF_TIMEZONE are gone; a CTF_SEED still in an
// env file is logged and ignored, so an old env file cannot stop the server.
//
// /admin/ has accounts of its own (internal/central/auth.go). >>> A FRESH
// DATABASE HAS NONE: the server then logs a one-time setup code
// (setup_code=…), and <base>/admin/setup asks for it to make the first one.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the scratch image has no zoneinfo

	"github.com/macsnoeren/juiceshop-ctf/internal/central"
	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/config"
	"github.com/macsnoeren/juiceshop-ctf/internal/health"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/version"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the running server and exit 0 or 1")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	env := config.FromOS()
	addr := env.String("CTF_ADDR", ":8080")
	base := env.BasePath("CTF_BASE_PATH", "")
	if *showVersion {
		fmt.Println("ctf-central", version.Version)
		return
	}
	if *healthcheck {
		if err := health.Check(addr, base+"/healthz"); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	log := httpx.NewLogger(env.String("LOG_LEVEL", "info"))
	dbPath := env.String("CTF_DB_PATH", "/data/central.db")
	proxies, err := httpx.ParseProxies(env.String("CTF_TRUSTED_PROXIES", httpx.DefaultTrustedProxies))
	if err != nil {
		log.Error("CTF_TRUSTED_PROXIES", "err", err)
		os.Exit(2)
	}
	if _, set := os.LookupEnv("CTF_SEED"); set {
		log.Warn("CTF_SEED is set but no longer used: games and join codes are made in /admin/ (1.1)")
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("cannot open the database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()
	srv, err := central.New(central.Config{BasePath: base, TrustedProxies: proxies}, st, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go prune(ctx, st, log)
	schema, _ := st.SchemaVersion()
	log.Info("ctf-central starting", "version", version.Version, "base", base, "db", dbPath, "schema", schema)
	if err := httpx.Serve(ctx, httpx.NewServer(addr, srv.Handler()), log); err != nil {
		log.Error("stopped with an error", "err", err)
		os.Exit(1)
	}
}

// prune applies the event retention at start and then once a day.
func prune(ctx context.Context, st *store.Store, log *slog.Logger) {
	for {
		if n, err := st.Prune(time.Now()); err != nil {
			log.Error("pruning old events failed", "err", err)
		} else if n > 0 {
			log.Info("old events deleted", "events", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}
