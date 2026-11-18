// Package agent is ctf-agent: it watches one Juice Shop, keeps the instance's
// state, and reports it to ctf-central (DESIGN.md §6, §7).
//
// Three loops share one State behind one mutex:
//
//   - the poller reads /api/Challenges every PollInterval, or sooner when the
//     webhook says something was solved. It is the only source of truth
//     about solves (§6.1), and it restores a restarted Juice Shop (§6.2);
//   - the reporter sends the full snapshot to the central server within a
//     couple of seconds of a change, and every ReportInterval regardless
//     (§7.1), and carries out the commands that come back;
//   - the HTTP servers: the webhook on the ctf-juice network, and the
//     internal API on ctf-internal for ctf-status (§4, §9).
//
// No network call is ever made while holding the mutex.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/httpx"
	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/names"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// Shop is the part of the Juice Shop client the agent uses; tests fake it.
type Shop interface {
	Challenges(ctx context.Context) ([]juiceshop.Challenge, error)
	Version(ctx context.Context) (string, error)
	ContinueCodes(ctx context.Context) (juiceshop.Codes, error)
	ApplyContinueCodes(ctx context.Context, codes juiceshop.Codes) error
}

// Config is the agent's configuration (§9).
type Config struct {
	InstanceID     string
	CentralURL     string // e.g. https://ctf.jmnl.nl; /api/v1/report is appended
	CentralToken   string
	InternalToken  string
	WebhookAddr    string // :8081
	APIAddr        string // :8082
	PollInterval   time.Duration
	ReportInterval time.Duration
	// PollDebounce is the minimum gap between two polls, so a burst of
	// webhooks is one poll (§6.1). ReportDebounce is how long the reporter
	// gathers changes before it sends (§7.1).
	PollDebounce   time.Duration
	ReportDebounce time.Duration
	// BackoffBase and BackoffMax shape the retry after a failed report:
	// BackoffBase doubling per failure, capped at BackoffMax (§7.1).
	BackoffBase time.Duration
	BackoffMax  time.Duration
}

func (c *Config) defaults() {
	if c.PollInterval == 0 {
		c.PollInterval = 30 * time.Second
	}
	if c.ReportInterval == 0 {
		c.ReportInterval = 60 * time.Second
	}
	if c.PollDebounce == 0 {
		c.PollDebounce = 2 * time.Second
	}
	if c.ReportDebounce == 0 {
		c.ReportDebounce = 2 * time.Second
	}
	if c.BackoffBase == 0 {
		c.BackoffBase = 2 * time.Second
	}
	if c.BackoffMax == 0 {
		c.BackoffMax = 5 * time.Minute
	}
	if c.WebhookAddr == "" {
		c.WebhookAddr = ":8081"
	}
	if c.APIAddr == "" {
		c.APIAddr = ":8082"
	}
}

// Agent is one running ctf-agent.
type Agent struct {
	cfg   Config
	shop  Shop
	store Store
	log   *slog.Logger
	now   func() time.Time
	httpc *http.Client

	saveMu sync.Mutex // orders writes of the state file
	mu     sync.Mutex // guards everything below
	st     *State

	catalogue   []juiceshop.Challenge
	boot        string
	reachable   bool
	jsVersion   string
	restoreBoot string // the boot a restore was last attempted for
	lastSig     string // progress signature the codes were last fetched for
	lastPoll    time.Time
	lastAuthLog time.Time

	pollNow   chan struct{}
	reportNow chan struct{}

	polls   atomic.Int64 // for tests
	reports atomic.Int64 // for tests
}

// New loads the state and returns an agent. A corrupt state file is moved
// aside and logged loudly (§6.3); a state file from a newer agent is fatal.
func New(cfg Config, shop Shop, store Store, log *slog.Logger) (*Agent, error) {
	cfg.defaults()
	if store.now == nil {
		store.now = time.Now
	}
	st, err := store.Load()
	if errors.Is(err, ErrCorrupt) {
		log.Error("STATE FILE CORRUPT: starting empty, the registration and continue codes are gone; the central server still has every solve", "err", err)
	} else if err != nil {
		return nil, err
	}
	return &Agent{
		cfg:       cfg,
		shop:      shop,
		store:     store,
		log:       log,
		now:       time.Now,
		httpc:     &http.Client{Timeout: 15 * time.Second},
		st:        st,
		pollNow:   make(chan struct{}, 1),
		reportNow: make(chan struct{}, 1),
	}, nil
}

// TriggerPoll asks for a poll as soon as the debounce allows.
func (a *Agent) TriggerPoll() {
	select {
	case a.pollNow <- struct{}{}:
	default:
	}
}

// TriggerReport asks for a report soon.
func (a *Agent) TriggerReport() {
	select {
	case a.reportNow <- struct{}{}:
	default:
	}
}

func (a *Agent) save() {
	a.saveMu.Lock()
	defer a.saveMu.Unlock()
	a.mu.Lock()
	cp := *a.st
	cp.Solved = copyMap(a.st.Solved)
	cp.Cheat = copyMap(a.st.Cheat)
	cp.Baseline = append([]string(nil), a.st.Baseline...)
	cp.CommandsApplied = append([]string(nil), a.st.CommandsApplied...)
	a.mu.Unlock()
	if err := a.store.Save(&cp); err != nil {
		a.log.Error("saving state failed", "err", err)
	}
}

func copyMap[V any](m map[string]V) map[string]V {
	out := make(map[string]V, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// Poll reads Juice Shop once, restores it if it lost progress, records new
// solves, and refreshes the continue codes when progress changed.
func (a *Agent) Poll(ctx context.Context) {
	a.polls.Add(1)
	a.mu.Lock()
	a.lastPoll = a.now()
	a.mu.Unlock()

	chs, err := a.shop.Challenges(ctx)
	if err != nil {
		a.mu.Lock()
		was := a.reachable
		a.reachable = false
		a.mu.Unlock()
		if was {
			a.log.Warn("juice shop unreachable", "err", err)
			a.TriggerReport()
		}
		return
	}
	boot := bootID(chs)
	dirty := false

	a.mu.Lock()
	wasReachable := a.reachable
	a.reachable = true
	newBoot := boot != a.boot
	a.boot, a.catalogue = boot, chs
	st := a.st
	if st.BaselineBoot != "" && st.BaselineBoot != boot {
		st.Baseline, st.BaselineBoot = nil, ""
		dirty = true
	}
	missing := deficit(st, chs, boot)
	needRestore := len(missing) > 0 && !st.ContinueCodes.Empty() && a.restoreBoot != boot
	codes := st.ContinueCodes
	if needRestore {
		a.restoreBoot = boot
	}
	needVersion := newBoot || a.jsVersion == ""
	a.mu.Unlock()

	if needVersion {
		if v, err := a.shop.Version(ctx); err == nil {
			a.mu.Lock()
			a.jsVersion = v
			a.mu.Unlock()
		}
		if newBoot && wasReachable {
			a.log.Info("juice shop restarted", "boot", boot)
		}
	}

	if needRestore {
		a.log.Info("juice shop lost progress, applying the continue codes", "missing", len(missing))
		if err := a.shop.ApplyContinueCodes(ctx, codes); err != nil {
			a.log.Warn("applying continue codes failed", "err", err)
		}
		if again, err := a.shop.Challenges(ctx); err == nil {
			chs = again
			a.mu.Lock()
			a.catalogue = chs
			a.mu.Unlock()
		}
		a.mu.Lock()
		still := deficit(st, chs, boot)
		st.RestoreFailed = len(still) > 0
		a.mu.Unlock()
		if len(still) > 0 {
			a.log.Warn("juice shop progress NOT fully restored; the central score is unaffected", "missing", len(still))
		} else {
			a.log.Info("juice shop progress restored", "challenges", len(missing))
		}
		dirty = true
	}

	a.mu.Lock()
	obs := Observe(st, chs, boot, a.now())
	if len(obs.NewSolves) > 0 {
		dirty = true
	}
	if st.RestoreFailed && len(obs.Deficit) == 0 {
		st.RestoreFailed = false
		dirty = true
	}
	sig := signature(chs)
	fetchCodes := st.Registration != nil && sig != a.lastSig &&
		(len(obs.Deficit) == 0 || a.restoreBoot == boot)
	a.mu.Unlock()
	for _, k := range obs.NewSolves {
		a.log.Info("challenge solved", "challenge", k)
	}

	if fetchCodes {
		if got, err := a.shop.ContinueCodes(ctx); err != nil {
			a.log.Warn("fetching continue codes failed", "err", err)
		} else {
			a.mu.Lock()
			if st.Registration != nil && got != st.ContinueCodes {
				st.ContinueCodes = got
				dirty = true
			}
			a.lastSig = sig
			a.mu.Unlock()
		}
	}

	if dirty {
		a.save()
	}
	if dirty || !wasReachable {
		a.TriggerReport()
	}
}

// RunPoller polls every PollInterval and on TriggerPoll, never more often
// than PollDebounce.
func (a *Agent) RunPoller(ctx context.Context) {
	t := time.NewTicker(a.cfg.PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.pollNow:
		}
		a.mu.Lock()
		wait := a.cfg.PollDebounce - a.now().Sub(a.lastPoll)
		a.mu.Unlock()
		if wait > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
		}
		select { // triggers that arrived while waiting are this poll
		case <-a.pollNow:
		default:
		}
		a.Poll(ctx)
	}
}

// Errors of Register.
var (
	ErrRegistered = errors.New("this instance already has a registration")
	ErrBadName    = errors.New("invalid name")
)

// Register signs the instance up for a student (§5.4). Whatever Juice Shop
// has solved at this moment becomes the baseline and never counts.
func (a *Agent) Register(name string) (*protocol.Registration, error) {
	n, err := names.Normalize(name)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrBadName, err)
	}
	a.mu.Lock()
	if a.st.Registration != nil {
		a.mu.Unlock()
		return nil, ErrRegistered
	}
	reg := &protocol.Registration{ID: httpx.RandomHex(16), Name: n, RegisteredAt: a.now().UTC().Truncate(time.Second)}
	a.st.clearRegistration()
	a.st.Registration = reg
	for _, c := range a.catalogue {
		if c.Solved && !c.Disabled() {
			a.st.Baseline = append(a.st.Baseline, c.Key)
		}
	}
	if len(a.st.Baseline) > 0 {
		sort.Strings(a.st.Baseline)
		a.st.BaselineBoot = a.boot
	}
	a.lastSig = ""
	baseline := len(a.st.Baseline)
	a.mu.Unlock()
	a.save()
	a.log.Info("registered", "name", n, "baseline", baseline)
	a.TriggerReport()
	a.TriggerPoll()
	return reg, nil
}

// resetRegistration carries out the reset_registration command (§8.4).
func (a *Agent) resetRegistration() {
	a.mu.Lock()
	a.st.clearRegistration()
	a.lastSig = ""
	a.mu.Unlock()
	a.log.Info("registration reset by the central server; restart Juice Shop to give the next student a clean shop")
}

// Registration returns the current registration, or nil.
func (a *Agent) Registration() *protocol.Registration {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.st.Registration == nil {
		return nil
	}
	r := *a.st.Registration
	return &r
}

// Run starts everything and blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errc := make(chan error, 2)
	go func() { errc <- httpx.Serve(ctx, httpx.NewServer(a.cfg.WebhookAddr, a.WebhookHandler()), a.log) }()
	go func() { errc <- httpx.Serve(ctx, httpx.NewServer(a.cfg.APIAddr, a.APIHandler()), a.log) }()

	a.Poll(ctx)
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.RunPoller(ctx) }()
	go func() { defer wg.Done(); a.RunReporter(ctx) }()

	var err error
	select {
	case <-ctx.Done():
	case err = <-errc:
		cancel()
	}
	wg.Wait()
	a.save()
	a.log.Info("stopped")
	return err
}
