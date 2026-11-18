package agent

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
)

func strp(s string) *string { return &s }

// fakeShop is a Juice Shop in memory: a small catalogue, a solved set, a boot
// time that changes on restart, and continue codes that really encode the
// solved set, so a restore can succeed or fail on purpose.
type fakeShop struct {
	mu         sync.Mutex
	boot       time.Time
	solved     map[string]bool
	applyFails bool
	down       bool
	applied    int
	challenges int // calls to Challenges
}

var catalogue = []juiceshop.Challenge{
	{ID: 1, Key: "scoreBoardChallenge", Name: "Score Board", Category: "Miscellaneous", Difficulty: 1},
	{ID: 2, Key: "loginAdminChallenge", Name: "Login Admin", Category: "Injection", Difficulty: 2},
	{ID: 3, Key: "directoryListingChallenge", Name: "Confidential Document", Category: "Sensitive Data Exposure", Difficulty: 1},
	{ID: 4, Key: "xxeFileDisclosureChallenge", Name: "XXE Data Access", Category: "XXE", Difficulty: 3, DisabledEnv: strp("Docker")},
}

func newFakeShop() *fakeShop {
	return &fakeShop{boot: time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC), solved: map[string]bool{}}
}

func (f *fakeShop) solve(keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, k := range keys {
		f.solved[k] = true
	}
}

func (f *fakeShop) restart() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.boot = f.boot.Add(time.Minute)
	f.solved = map[string]bool{}
}

func (f *fakeShop) Challenges(ctx context.Context) ([]juiceshop.Challenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenges++
	if f.down {
		return nil, errors.New("connection refused")
	}
	out := make([]juiceshop.Challenge, len(catalogue))
	for i, c := range catalogue {
		c.Solved = f.solved[c.Key]
		c.CreatedAt = f.boot
		out[i] = c
	}
	return out, nil
}

func (f *fakeShop) Version(ctx context.Context) (string, error) { return "20.3.0", nil }

func (f *fakeShop) ContinueCodes(ctx context.Context) (juiceshop.Codes, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for k := range f.solved {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return juiceshop.Codes{Challenges: "code:" + strings.Join(keys, "+")}, nil
}

func (f *fakeShop) ApplyContinueCodes(ctx context.Context, c juiceshop.Codes) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.applied++
	if f.applyFails {
		return juiceshop.ErrInvalidCode
	}
	for _, k := range strings.Split(strings.TrimPrefix(c.Challenges, "code:"), "+") {
		if k != "" {
			f.solved[k] = true
		}
	}
	return nil
}

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

type harness struct {
	t     *testing.T
	shop  *fakeShop
	clock *clock
	logs  *logBuf
	path  string
	a     *Agent
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{
		t:     t,
		shop:  newFakeShop(),
		clock: &clock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)},
		logs:  &logBuf{},
		path:  filepath.Join(t.TempDir(), "state.json"),
	}
	h.a = h.newAgent(cfg)
	return h
}

func (h *harness) newAgent(cfg Config) *Agent {
	h.t.Helper()
	if cfg.InstanceID == "" {
		cfg.InstanceID = "cybersecurity-1"
	}
	if cfg.InternalToken == "" {
		cfg.InternalToken = "internal-token-for-tests"
	}
	log := slog.New(slog.NewTextHandler(io.MultiWriter(h.logs), nil))
	a, err := New(cfg, h.shop, NewStore(h.path), log)
	if err != nil {
		h.t.Fatal(err)
	}
	a.now = h.clock.Now
	return a
}

func (h *harness) register(name string) {
	h.t.Helper()
	if _, err := h.a.Register(name); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) poll() { h.a.Poll(context.Background()) }
