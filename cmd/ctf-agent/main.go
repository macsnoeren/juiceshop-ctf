// Command ctf-agent watches one Juice Shop and reports its progress to
// ctf-central (DESIGN.md §6, §7). See internal/agent for how it works.
//
// Configuration is environment only (DESIGN.md §9):
//
//	CTF_INSTANCE_ID       e.g. cybersecurity-1                     (required)
//	CTF_CENTRAL_URL       e.g. https://ctf.jmnl.nl                  (required)
//	CTF_CENTRAL_TOKEN     issued in ctf-central /admin/             (required, secret)
//	CTF_INTERNAL_TOKEN    shared with ctf-status                    (required, secret)
//	JUICESHOP_URL         default http://juiceshop:3000
//	CTF_STATE_PATH        default /var/lib/ctf-agent/state.json
//	CTF_WEBHOOK_ADDR      default :8081; set to ctf-agent-hook:8081 so it binds on
//	                      the ctf-juice network ONLY (DESIGN.md §4)
//	CTF_API_ADDR          default :8082; set to ctf-agent-api:8082 so it binds on
//	                      the ctf-internal network ONLY, out of Juice Shop's reach
//	POLL_INTERVAL         default 30s
//	REPORT_INTERVAL       default 60s
//	REPORT_BACKOFF_MAX    default 5m, the longest wait between failed reports
//	LOG_LEVEL             debug | info | warn | error
//
// Flags: -healthcheck (for the Dockerfile's HEALTHCHECK) and -version.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/node/agent"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/config"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/health"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/httpx"
	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/version"
)

func main() {
	healthcheck := flag.Bool("healthcheck", false, "check the running agent and exit 0 or 1")
	showVersion := flag.Bool("version", false, "print the version and exit")
	flag.Parse()

	env := config.FromOS()
	apiAddr := env.String("CTF_API_ADDR", ":8082")
	if *showVersion {
		fmt.Println("ctf-agent", version.Version)
		return
	}
	if *healthcheck {
		if err := health.Check(apiAddr, "/healthz"); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}

	log := httpx.NewLogger(env.String("LOG_LEVEL", "info"))
	cfg := agent.Config{
		InstanceID:     env.Required("CTF_INSTANCE_ID"),
		CentralURL:     env.Required("CTF_CENTRAL_URL"),
		CentralToken:   env.Secret("CTF_CENTRAL_TOKEN", 16),
		InternalToken:  env.Secret("CTF_INTERNAL_TOKEN", 16),
		WebhookAddr:    env.String("CTF_WEBHOOK_ADDR", ":8081"),
		APIAddr:        apiAddr,
		PollInterval:   env.Duration("POLL_INTERVAL", 0),
		ReportInterval: env.Duration("REPORT_INTERVAL", 0),
		BackoffMax:     env.Duration("REPORT_BACKOFF_MAX", 0),
	}
	jsURL := env.String("JUICESHOP_URL", "http://juiceshop:3000")
	statePath := env.String("CTF_STATE_PATH", "/var/lib/ctf-agent/state.json")
	if err := env.Err(); err != nil {
		log.Error(err.Error())
		os.Exit(2)
	}
	if !protocol.ValidInstanceID(cfg.InstanceID) {
		log.Error("CTF_INSTANCE_ID must be lower-case letters, digits and dashes", "value", cfg.InstanceID)
		os.Exit(2)
	}

	a, err := agent.New(cfg, juiceshop.New(jsURL), agent.NewStore(statePath), log)
	if err != nil {
		log.Error("cannot start", "err", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	log.Info("ctf-agent starting", "version", version.Version, "instance", cfg.InstanceID, "juiceshop", jsURL, "central", cfg.CentralURL)
	if err := a.Run(ctx); err != nil {
		log.Error("stopped with an error", "err", err)
		os.Exit(1)
	}
}
