// Command ctf-status is the student's door to their Juice Shop: sign-up,
// access gate and own progress (DESIGN.md §5). See internal/status.
//
// Configuration is environment only (DESIGN.md §9):
//
//	CTF_ACCESS_CODE       what the student types at the door          (required, secret)
//	CTF_GATE_SECRET       32+ random characters, signs the cookie     (required, secret)
//	CTF_INTERNAL_TOKEN    the same value as in the agent's env        (required, secret)
//	CTF_AGENT_URL         default http://ctf-agent:8082
//	CTF_BASE_PATH         default /ctf
//	CTF_JUICESHOP_PATH    default /juiceshop/
//	CTF_LEADERBOARD_URL   the link to the central leaderboard (optional)
//	CTF_COOKIE_SECURE     default true; false ONLY for plain HTTP on an IP address (§5.3)
//	CTF_TIMEZONE          default Europe/Amsterdam, for the times on the page
//	CTF_ADDR              default :8080
//	LOG_LEVEL             debug | info | warn | error
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // the scratch image has no zoneinfo

	"github.com/macsnoeren/juiceshop-ctf/internal/config"
	"github.com/macsnoeren/juiceshop-ctf/internal/health"
	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/status"
	"github.com/macsnoeren/juiceshop-ctf/internal/version"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the running server and exit 0 or 1")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	env := config.FromOS()
	addr := env.String("CTF_ADDR", ":8080")
	base := env.BasePath("CTF_BASE_PATH", "/ctf")
	if *showVersion {
		fmt.Println("ctf-status", version.Version)
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
	tzName := env.String("CTF_TIMEZONE", "Europe/Amsterdam")
	cfg := status.Config{
		BasePath:       base,
		AccessCode:     env.Secret("CTF_ACCESS_CODE", 4),
		GateSecret:     []byte(env.Secret("CTF_GATE_SECRET", 32)),
		CookieSecure:   env.Bool("CTF_COOKIE_SECURE", true),
		JuiceShopPath:  env.String("CTF_JUICESHOP_PATH", "/juiceshop/"),
		LeaderboardURL: env.String("CTF_LEADERBOARD_URL", ""),
	}
	agentURL := env.String("CTF_AGENT_URL", "http://ctf-agent:8082")
	token := env.Secret("CTF_INTERNAL_TOKEN", 16)
	if err := env.Err(); err != nil {
		log.Error(err.Error())
		os.Exit(2)
	}
	loc, err := time.LoadLocation(tzName)
	if err != nil {
		log.Error("CTF_TIMEZONE", "err", err)
		os.Exit(2)
	}
	cfg.Location = loc
	if !cfg.CookieSecure {
		log.Warn("CTF_COOKIE_SECURE=false: the gate cookie also travels over plain HTTP; use this only in a closed lab")
	}

	srv, err := status.New(cfg, status.NewHTTPAgent(agentURL, token), log)
	if err != nil {
		log.Error("cannot start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("ctf-status starting", "version", version.Version, "base", base, "agent", agentURL)
	if err := httpx.Serve(ctx, httpx.NewServer(addr, srv.Handler()), log); err != nil {
		log.Error("stopped with an error", "err", err)
		os.Exit(1)
	}
}
