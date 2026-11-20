// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/names"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// Shop is the part of the Juice Shop client the node uses; tests fake it.
type Shop interface {
	Challenges(ctx context.Context) ([]juiceshop.Challenge, error)
	Version(ctx context.Context) (string, error)
	ContinueCodes(ctx context.Context) (juiceshop.Codes, error)
	ApplyContinueCodes(ctx context.Context, codes juiceshop.Codes) error
}

// Keeper restarts Juice Shop: the keeper over its control pipe in the image
// (keeper.Client), a fake in tests, nil when ctf-node runs on its own.
type Keeper interface {
	RestartJuiceShop(ctx context.Context) error
}

// Config is the node's configuration (cmd/ctf-node).
type Config struct {
	// DefaultCentralURL is CTF_CENTRAL_URL: the image may carry one (the
	// build arg CENTRAL_URL). The page can override it while not joined.
	DefaultCentralURL string
	Version           string
	// Gate is CTF_GATE=session.
	Gate bool

	PollInterval   time.Duration
	ReportInterval time.Duration
	PollDebounce   time.Duration
	ReportDebounce time.Duration
	BackoffBase    time.Duration
	BackoffMax     time.Duration
	JoinTimeout    time.Duration
}

func (c *Config) defaults() {
	set := func(d *time.Duration, v time.Duration) {
		if *d == 0 {
			*d = v
		}
	}
	set(&c.PollInterval, 30*time.Second)
	set(&c.ReportInterval, 60*time.Second)
	set(&c.PollDebounce, 2*time.Second)
	set(&c.ReportDebounce, 2*time.Second)
	set(&c.BackoffBase, 2*time.Second)
	set(&c.BackoffMax, 5*time.Minute)
	set(&c.JoinTimeout, 10*time.Second)
	c.DefaultCentralURL = strings.TrimRight(strings.TrimSpace(c.DefaultCentralURL), "/")
}

// DefaultScoring is the local score's table when there is no central server
// to say otherwise.
var DefaultScoring = map[int]int{1: 100, 2: 250, 3: 450, 4: 700, 5: 1000, 6: 1350}

// Node is one running ctf-node.
type Node struct {
	cfg    Config
	shop   Shop
	db     *Store
	keeper Keeper
	log    *slog.Logger
	now    func() time.Time
	httpc  *http.Client

	saveMu sync.Mutex // orders writes of node.db
	opMu   sync.Mutex // one poll, Forget or Restart at a time
	mu     sync.Mutex // guards everything below
	st     *State

	catalogue   []juiceshop.Challenge
	boot        string
	reachable   bool
	jsVersion   string
	restoreBoot string
	lastSig     string
	lastPoll    time.Time
	lastAuthLog time.Time

	pollNow   chan struct{}
	reportNow chan struct{}
	joinNow   chan struct{}

	polls   atomic.Int64
	reports atomic.Int64
}

// New loads node.db and returns a node.
func New(cfg Config, shop Shop, db *Store, keeper Keeper, log *slog.Logger) (*Node, error) {
	cfg.defaults()
	st, err := db.Load()
	if err != nil {
		return nil, err
	}
	return &Node{
		cfg: cfg, shop: shop, db: db, keeper: keeper, log: log, now: time.Now,
		// No redirects: the central server never sends one, and following
		// it could carry the token to another scheme or host.
		httpc: &http.Client{Timeout: 15 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		st:    st, pollNow: make(chan struct{}, 1), reportNow: make(chan struct{}, 1), joinNow: make(chan struct{}, 1),
	}, nil
}

// SetTransport replaces the HTTP transport to the central server; tests use
// it to see every outbound request.
func (n *Node) SetTransport(rt http.RoundTripper) { n.httpc.Transport = rt }

// SetClock replaces the clock; for tests.
func (n *Node) SetClock(now func() time.Time) { n.now = now }

func kick(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// TriggerPoll asks for a poll as soon as the debounce allows.
func (n *Node) TriggerPoll() { kick(n.pollNow) }

// TriggerReport asks for a report soon.
func (n *Node) TriggerReport() { kick(n.reportNow) }

func (n *Node) save() {
	n.saveMu.Lock()
	defer n.saveMu.Unlock()
	n.mu.Lock()
	cp := n.st.clone()
	n.mu.Unlock()
	if err := n.db.Save(cp); err != nil {
		n.log.Error("saving node.db failed", "err", err)
	}
}

// ---- the central server's address ------------------------------------------

const settingCentral = "central_url"

// centralNone is the stored override for "no central server".
const centralNone = "none"

// CentralURL is the central server a NEW join goes to: the page's choice, or
// the default; "" means none (central-less).
func (n *Node) CentralURL() string {
	v, err := n.db.Setting(settingCentral)
	if err != nil {
		n.log.Error("reading the central server setting", "err", err)
	}
	switch v {
	case "":
		return n.cfg.DefaultCentralURL
	case centralNone:
		return ""
	}
	return v
}

// ErrJoinedAlready refuses what only makes sense before joining.
var ErrJoinedAlready = errors.New("this VM has joined a game: press Forget first")

// ValidateCentralURL checks an address typed on the page, or the image's
// default.
//
// >>> PLAIN HTTP only to a server that is not on the internet: localhost, a
// name without a dot (a Docker service, the test setup) or a private
// address. Over plain HTTP anyone on the network between the VM and the
// server reads the join code and every token, and can answer a report with
// a forget that wipes the VM. On a network others share (the school's
// Wi-Fi) that is still true, so the page says so (PlainOnSharedNetwork).
func ValidateCentralURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || len(raw) > 200 {
		return "", errors.New("give the central server as http://… or https://…, e.g. https://ctf.example.org")
	}
	if u.Scheme == "http" && !localHost(u.Hostname()) {
		return "", errors.New("a central server on the internet is reached with https://, never http://: over http anyone on the way reads your token")
	}
	return raw, nil
}

// localHost reports whether h is localhost, a loopback or private address,
// or a name without a dot.
func localHost(h string) bool {
	if a, err := netip.ParseAddr(h); err == nil {
		a = a.Unmap()
		return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
	}
	return strings.EqualFold(h, "localhost") || !strings.Contains(h, ".")
}

// PlainOnSharedNetwork reports whether the central server is reached over
// plain HTTP on something other than this machine.
func PlainOnSharedNetwork(central string) bool {
	u, err := url.Parse(central)
	if err != nil || u.Scheme != "http" {
		return false
	}
	h := u.Hostname()
	if a, err := netip.ParseAddr(h); err == nil {
		return !a.Unmap().IsLoopback()
	}
	return !strings.EqualFold(h, "localhost")
}

// SetCentral changes the central server for the next join: a URL, "" for the
// default, or "none". Refused while a membership exists: its token is only
// good on the server that issued it.
func (n *Node) SetCentral(v string) error {
	n.mu.Lock()
	joined := n.st.Membership != nil || n.st.Outbox != nil
	n.mu.Unlock()
	if joined {
		return ErrJoinedAlready
	}
	if v != "" && v != centralNone {
		var err error
		if v, err = ValidateCentralURL(v); err != nil {
			return err
		}
		if v == n.cfg.DefaultCentralURL {
			v = ""
		}
	}
	n.log.Info("central server changed on the page", "central", map[bool]string{true: "default", false: v}[v == ""])
	return n.db.SetSetting(settingCentral, v)
}

// ---- joining ---------------------------------------------------------------

// JoinOutcome says how a join went.
type JoinOutcome int

const (
	JoinedOnline JoinOutcome = iota // the central server issued a token
	JoinQueued                      // unreachable: in the outbox, play on
	JoinedLocal                     // no central server: on this VM only
)

// JoinError is a refusal to show on the page.
type JoinError struct {
	Status int
	Msg    string
}

func (e *JoinError) Error() string { return e.Msg }

// JoinResult is a successful (or queued) join. ReturnCode is set only with
// the gate on, and shown once.
type JoinResult struct {
	Outcome    JoinOutcome
	ReturnCode string
}

// Join signs the student up: name and join code
// go to the central server, and its token is stored; the code is NOT kept,
// unless the server cannot be reached, and then only in the outbox until it
// can. Whatever Juice Shop has solved at the first join is the baseline.
func (n *Node) Join(ctx context.Context, name, code string) (*JoinResult, error) {
	nm, err := names.Normalize(name)
	if err != nil {
		return nil, &JoinError{http.StatusBadRequest, err.Error()}
	}
	n.mu.Lock()
	cur := n.st.Membership
	if cur != nil && cur.Status != StatusRefused && cur.Status != StatusRejected {
		n.mu.Unlock()
		return nil, ErrJoinedAlready
	}
	var uid string
	var joinedAt time.Time
	central := n.CentralURL()
	if cur != nil {
		uid, joinedAt, central = cur.NodeUID, cur.JoinedAt, cur.CentralURL
	} else {
		uid, joinedAt = protocol.NewNodeUID(), n.now().UTC().Truncate(time.Second)
	}
	n.mu.Unlock()

	res := &JoinResult{}
	gate := func(m *Membership) {
		if n.cfg.Gate {
			res.ReturnCode = protocol.NewReturnCode()
			m.ReturnCodeHash = protocol.Hash(res.ReturnCode)
		}
	}
	if central == "" {
		m := &Membership{Status: StatusLocal, Name: nm, NodeUID: uid, JoinedAt: joinedAt}
		gate(m)
		n.mu.Lock()
		n.st.Membership = m
		n.takeBaseline()
		n.mu.Unlock()
		n.save()
		n.log.Info("joined locally (no central server)")
		res.Outcome = JoinedLocal
		return res, nil
	}
	c, ok := protocol.NormalizeJoinCode(code)
	if !ok {
		return nil, &JoinError{http.StatusBadRequest, "A join code is 10 letters and digits, as your teacher shows it."}
	}

	jctx, cancel := context.WithTimeout(ctx, n.cfg.JoinTimeout)
	defer cancel()
	jr, err := n.sendJoin(jctx, central, nm, c, uid, joinedAt)
	var refused *JoinError
	switch {
	case errors.As(err, &refused):
		if cur != nil {
			n.mu.Lock()
			n.st.Membership.Problem = refused.Msg
			n.mu.Unlock()
			n.save()
		}
		return nil, refused
	case err != nil:
		n.log.Warn("central server unreachable at join: queued, the student plays on", "err", err)
		m := &Membership{Status: StatusPending, Name: nm, NodeUID: uid, JoinedAt: joinedAt, CentralURL: central}
		gate(m)
		n.mu.Lock()
		if cur == nil {
			n.st.Membership = m
			n.takeBaseline()
		} else {
			m.ReturnCodeHash = keepIf(m.ReturnCodeHash, cur.ReturnCodeHash)
			n.st.Membership = m
		}
		n.st.Outbox = &Outbox{Name: nm, Code: c, JoinedAt: joinedAt, NextAt: n.now().Add(n.cfg.BackoffBase), LastError: err.Error()}
		n.mu.Unlock()
		n.save()
		kick(n.joinNow)
		res.Outcome = JoinQueued
		return res, nil
	}
	m := joinedMembership(jr, uid, joinedAt, central)
	gate(m)
	n.mu.Lock()
	if cur == nil {
		n.st.Membership = m
		n.takeBaseline()
	} else {
		m.ReturnCodeHash = keepIf(m.ReturnCodeHash, cur.ReturnCodeHash)
		n.st.Membership = m
	}
	n.mu.Unlock()
	n.save()
	n.log.Info("joined", "participant", jr.Participant, "game", jr.Game.Slug)
	n.TriggerReport()
	n.TriggerPoll()
	res.Outcome = JoinedOnline
	return res, nil
}

func keepIf(newer, old string) string {
	if newer != "" {
		return newer
	}
	return old
}

func joinedMembership(jr *protocol.JoinResponse, uid string, joinedAt time.Time, central string) *Membership {
	return &Membership{Status: StatusJoined, Name: jr.Name, NodeUID: uid, JoinedAt: joinedAt, CentralURL: central,
		ParticipantID: jr.Participant, Token: jr.Token, Game: jr.Game, BoardURL: jr.Board}
}

// ---- Forget and Restart --------------------------------------------------------

// ErrForgetNoRestart is Forget that wiped node.db but could not restart Juice
// Shop; the page says so.
var ErrForgetNoRestart = errors.New("everything about the student is deleted, but Juice Shop could not be restarted")

// Forget wipes ALL student information — membership, token, name, solves,
// continue codes, baseline, outbox, standing, sessions — tells the central
// server (when it can be reached; nothing waits for it) and restarts Juice
// Shop with nothing to restore, so its database starts empty. The student's
// button and the operator's forget command do exactly this.
func (n *Node) Forget(ctx context.Context, reason string) error {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	n.mu.Lock()
	m := n.st.Membership
	n.mu.Unlock()
	if m != nil && m.Token != "" {
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		if err := n.sendLeave(lctx, m.CentralURL, m.Token, reason); err != nil {
			n.log.Warn("telling the central server about Forget failed; wiping anyway", "err", err)
		}
		cancel()
	}
	n.saveMu.Lock()
	err := n.db.Wipe()
	n.mu.Lock()
	n.st = newState()
	n.lastSig, n.restoreBoot = "", ""
	n.mu.Unlock()
	n.saveMu.Unlock()
	if err != nil {
		return err
	}
	n.log.Info("forgot the student", "reason", reason)
	rctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	if err := n.restartShop(rctx); err != nil {
		n.log.Error("restarting Juice Shop after Forget failed", "err", err)
		return ErrForgetNoRestart
	}
	return nil
}

// Restart restarts Juice Shop; the next polls put the progress back from
// the continue codes, as after any restart.
func (n *Node) Restart(ctx context.Context) error {
	n.opMu.Lock()
	defer n.opMu.Unlock()
	return n.restartShop(ctx)
}

func (n *Node) restartShop(ctx context.Context) error {
	if n.keeper == nil {
		return errors.New("Juice Shop cannot be restarted from here (ctf-node runs without its keeper)")
	}
	if err := n.keeper.RestartJuiceShop(ctx); err != nil {
		return err
	}
	n.log.Info("juice shop restarted from the student page")
	go n.pollUntilBack(60 * time.Second)
	return nil
}

// pollUntilBack polls every two seconds until Juice Shop answers with a new
// boot, so the restore follows a restart within seconds, not a poll interval.
func (n *Node) pollUntilBack(limit time.Duration) {
	n.mu.Lock()
	old := n.boot
	n.mu.Unlock()
	end := time.Now().Add(limit)
	for time.Now().Before(end) {
		time.Sleep(2 * time.Second)
		n.Poll(context.Background())
		n.mu.Lock()
		back := n.reachable && n.boot != old
		n.mu.Unlock()
		if back {
			n.Poll(context.Background()) // and once more, to see the restore
			return
		}
	}
}

// ---- what the page shows ---------------------------------------------------

// SolveView is one solved challenge, or one coding phase done, on the page.
type SolveView struct {
	Name       string
	Difficulty int
	SolvedAt   time.Time
	// Phase is "" for the hacking challenge, else "Find it" or "Fix it".
	Phase string
}

// View is the state as the student's page sees it. It never holds the token.
type View struct {
	Membership      *Membership
	Queued          bool
	QueueError      string
	Solved          int
	Total           int
	Coding          int
	CodingTotal     int
	LocalScore      int
	Categories      []Category
	Recent          []SolveView
	Standing        *Standing
	LastReportAt    *time.Time
	Reachable       bool
	RestoreFailed   bool
	JuiceShop       string
	CentralURL      string // where a new join would go
	CentralDefault  string
	CentralOverride bool
	// CentralAsk: the image has no default and the page has made no choice
	// (an address, or "none"), so the page asks for the central server
	// before the join form. Without it a student on a VM built without
	// CENTRAL_URL would join "on this VM only" without ever noticing.
	CentralAsk bool
	CanRestart bool
	Gate       bool
}

// View returns the page's view of the state.
func (n *Node) View() View {
	central := n.CentralURL()
	ov, _ := n.db.Setting(settingCentral)
	n.mu.Lock()
	defer n.mu.Unlock()
	st := n.st
	v := View{
		Standing: st.Standing, LastReportAt: st.LastReportAt, Reachable: n.reachable, RestoreFailed: st.RestoreFailed,
		JuiceShop: n.jsVersion, CentralURL: central, CentralDefault: n.cfg.DefaultCentralURL, CentralOverride: ov != "",
		CentralAsk: n.cfg.DefaultCentralURL == "" && ov == "",
		CanRestart: n.keeper != nil, Gate: n.cfg.Gate, Queued: st.Outbox != nil,
	}
	if st.Outbox != nil {
		v.QueueError = st.Outbox.LastError
	}
	if m := st.Membership; m != nil {
		cp := *m
		cp.Token, cp.ReturnCodeHash = "", ""
		v.Membership = &cp
	}
	p := Summary(st, n.catalogue, DefaultScoring)
	v.Solved, v.Total, v.Coding, v.CodingTotal, v.LocalScore, v.Categories = p.Solved, p.Total, p.Coding, p.CodingTotal, p.Score, p.Categories
	if st.Membership != nil {
		for _, c := range n.catalogue {
			if t, ok := st.Solved[c.Key]; ok {
				v.Recent = append(v.Recent, SolveView{Name: c.Name, Difficulty: c.Difficulty, SolvedAt: t})
			}
			for _, ph := range allPhases {
				if t, ok := st.Coding[CodingKey{c.Key, ph}]; ok {
					v.Recent = append(v.Recent, SolveView{Name: c.Name, Difficulty: c.Difficulty, SolvedAt: t, Phase: protocol.PhaseName(ph)})
				}
			}
		}
	}
	sort.SliceStable(v.Recent, func(i, j int) bool { return v.Recent[i].SolvedAt.After(v.Recent[j].SolvedAt) })
	if len(v.Recent) > 10 {
		v.Recent = v.Recent[:10]
	}
	return v
}

// ImportV1 imports a 1.0 state.json once: saved first,
// then the file is renamed, so a crash in between imports it again rather
// than losing it.
func (n *Node) ImportV1(path string) (int, error) {
	n.mu.Lock()
	count, err := ImportV1(path, n.st)
	n.mu.Unlock()
	if err != nil || count < 0 {
		return count, err
	}
	n.save()
	return count, os.Rename(path, path+".imported")
}

// ---- running -----------------------------------------------------------------

// Run starts the loops and blocks until ctx is cancelled. The HTTP servers
// are the caller's (cmd/ctf-node), so tests can run the node without ports.
func (n *Node) Run(ctx context.Context) {
	n.Poll(ctx)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); n.RunPoller(ctx) }()
	go func() { defer wg.Done(); n.RunReporter(ctx) }()
	go func() { defer wg.Done(); n.RunJoiner(ctx) }()
	wg.Wait()
	n.save()
	n.log.Info("stopped")
}
