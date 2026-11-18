// Command ctf-central receives the agents' reports and serves the leaderboard
// (DESIGN.md §7, §8). See internal/central.
//
//	ctf-central                                    serve
//	ctf-central seed -instance ID -token T [-label L]
//	                                               create or update an instance with a GIVEN token
//	ctf-central -healthcheck | -version
//
// Configuration is environment only (DESIGN.md §9):
//
//	CTF_DB_PATH      default /data/central.db (on the ctf-central-db volume)
//	CTF_BASE_PATH    default "" (own hostname); /leaderboard in the docker/ test setup
//	CTF_TITLE        default "Juice Shop CTF"
//	CTF_TIMEZONE     default Europe/Amsterdam
//	CTF_ADDR         default :8080
//	CTF_SEED         "id=token,id2=token2": instances to create at start.
//	                 For test setups, and for a deployment that makes both
//	                 halves of each token in one place (the agent's env file
//	                 holds it in the clear anyway). Otherwise instances and
//	                 their tokens are made in /admin/, where the token is never
//	                 stored in the clear anywhere.
//	LOG_LEVEL        debug | info | warn | error
//
// >>> /admin/ HAS NO LOGIN OF ITS OWN. nginx must guard it (DESIGN.md §8.4).
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
	_ "time/tzdata" // the scratch image has no zoneinfo

	"github.com/macsnoeren/juiceshop-ctf/internal/central"
	"github.com/macsnoeren/juiceshop-ctf/internal/config"
	"github.com/macsnoeren/juiceshop-ctf/internal/health"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/version"
)

func main() {
	env := config.FromOS()
	dbPath := env.String("CTF_DB_PATH", "/data/central.db")
	log := httpx.NewLogger(env.String("LOG_LEVEL", "info"))

	if len(os.Args) > 1 && os.Args[1] == "seed" {
		os.Exit(seed(dbPath, os.Args[2:]))
	}

	healthcheck := flag.Bool("healthcheck", false, "check the running server and exit 0 or 1")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()
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

	loc, err := time.LoadLocation(env.String("CTF_TIMEZONE", "Europe/Amsterdam"))
	if err != nil {
		log.Error("CTF_TIMEZONE", "err", err)
		os.Exit(2)
	}
	seeds := env.String("CTF_SEED", "")
	if err := env.Err(); err != nil {
		log.Error(err.Error())
		os.Exit(2)
	}

	st, err := store.Open(dbPath)
	if err != nil {
		log.Error("cannot open the database", "path", dbPath, "err", err)
		os.Exit(1)
	}
	defer st.Close()

	if seeds != "" {
		log.Warn("CTF_SEED is set: creating instances with tokens from the environment")
		for _, pair := range strings.Split(seeds, ",") {
			id, tok, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if !ok {
				log.Error("CTF_SEED entries are id=token", "entry", id)
				os.Exit(2)
			}
			if err := st.SeedInstance(id, "seeded", tok, time.Now()); err != nil {
				log.Error("seeding failed", "instance", id, "err", err)
				os.Exit(2)
			}
			log.Info("instance seeded", "instance", id)
		}
	}

	srv, err := central.New(central.Config{
		BasePath: base,
		Title:    env.String("CTF_TITLE", "Juice Shop CTF"),
		Location: loc,
	}, st, log)
	if err != nil {
		log.Error("cannot start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("ctf-central starting", "version", version.Version, "base", base, "db", dbPath)
	if err := httpx.Serve(ctx, httpx.NewServer(addr, srv.Handler()), log); err != nil {
		log.Error("stopped with an error", "err", err)
		os.Exit(1)
	}
}

func seed(dbPath string, args []string) int {
	fs := flag.NewFlagSet("seed", flag.ExitOnError)
	id := fs.String("instance", "", "instance id, e.g. cybersecurity-1")
	tok := fs.String("token", "", "the token the agent will send (16+ characters)")
	label := fs.String("label", "", "label shown in /admin/")
	fs.Parse(args)
	if *id == "" || *tok == "" {
		fmt.Fprintln(os.Stderr, "usage: ctf-central seed -instance ID -token TOKEN [-label LABEL]")
		return 2
	}
	st, err := store.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer st.Close()
	if err := st.SeedInstance(*id, *label, *tok, time.Now()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("instance", *id, "seeded")
	return 0
}
