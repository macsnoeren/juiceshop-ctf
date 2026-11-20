// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central"
	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
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
}

var catalogue = []juiceshop.Challenge{
	{ID: 1, Key: "scoreBoardChallenge", Name: "Score Board", Category: "Miscellaneous", Difficulty: 1},
	{ID: 2, Key: "loginAdminChallenge", Name: "Login Admin", Category: "Injection", Difficulty: 2},
	{ID: 3, Key: "directoryListingChallenge", Name: "Confidential Document", Category: "Sensitive Data Exposure", Difficulty: 1},
	{ID: 4, Key: "xxeFileDisclosureChallenge", Name: "XXE Data Access", Category: "XXE", Difficulty: 3, DisabledEnv: strp("Docker")},
}

func newFakeShop() *fakeShop {
	return &fakeShop{boot: time.Date(2026, 11, 19, 8, 0, 0, 0, time.UTC), solved: map[string]bool{}}
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

func (f *fakeShop) nSolved() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.solved)
}

func (f *fakeShop) Challenges(ctx context.Context) ([]juiceshop.Challenge, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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

// fakeKeeper restarts the fake shop, as the real keeper restarts Juice Shop.
type fakeKeeper struct {
	shop     *fakeShop
	restarts atomic.Int32
}

func (k *fakeKeeper) RestartJuiceShop(ctx context.Context) error {
	k.restarts.Add(1)
	k.shop.restart()
	return nil
}

type logBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *logBuf) Write(p []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(p) }
func (l *logBuf) String() string              { l.mu.Lock(); defer l.mu.Unlock(); return l.b.String() }

// centralServer is the REAL central server, in-process, with a switch that
// makes it unreachable (connections dropped) for the offline tests.
type centralServer struct {
	st   *store.Store
	srv  *central.Server
	ts   *httptest.Server
	down atomic.Bool
	URL  string
}

func newCentral(t *testing.T) *centralServer {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	srv, err := central.New(central.Config{BasePath: "/juiceshop-ctf"}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &centralServer{st: st, srv: srv}
	h := srv.Handler()
	c.ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c.down.Load() {
			if hj, ok := w.(http.Hijacker); ok {
				conn, _, _ := hj.Hijack()
				conn.Close()
				return
			}
		}
		h.ServeHTTP(w, r)
	}))
	t.Cleanup(c.ts.Close)
	c.URL = c.ts.URL + "/juiceshop-ctf"
	return c
}

// game makes a running game and returns it with its join code.
func (c *centralServer) game(t *testing.T, name string) (*store.Game, string) {
	t.Helper()
	g, code, err := c.st.CreateGame(store.GameInput{Name: name}, time.Now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.st.SetState(g.ID, protocol.StateRunning, time.Now()); err != nil {
		t.Fatal(err)
	}
	return g, code
}

func (c *centralServer) participants(t *testing.T, g *store.Game) []*store.Participant {
	t.Helper()
	g, _ = c.st.Game(g.ID)
	ps, err := c.st.GameParticipants(g, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	return ps
}

type harness struct {
	t      *testing.T
	shop   *fakeShop
	keeper *fakeKeeper
	logs   *logBuf
	dir    string
	db     *Store
	n      *Node
	srv    *Server
	h      http.Handler
	jar    map[string]string
}

func newHarness(t *testing.T, cfg Config) *harness {
	t.Helper()
	h := &harness{t: t, shop: newFakeShop(), logs: &logBuf{}, dir: filepath.Join(t.TempDir(), "ctf"), jar: map[string]string{}}
	h.keeper = &fakeKeeper{shop: h.shop}
	h.open(cfg)
	return h
}

func (h *harness) open(cfg Config) {
	h.t.Helper()
	db, err := OpenStore(filepath.Join(h.dir, "node.db"))
	if err != nil {
		h.t.Fatal(err)
	}
	h.t.Cleanup(func() { db.Close() })
	if cfg.Version == "" {
		cfg.Version = "v1.1.0-test"
	}
	if cfg.BackoffBase == 0 {
		cfg.BackoffBase = time.Millisecond
	}
	n, err := New(cfg, h.shop, db, h.keeper, slog.New(slog.NewTextHandler(h.logs, nil)))
	if err != nil {
		h.t.Fatal(err)
	}
	srv, err := NewServer(n, PageConfig{JuiceShop: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "juice shop: "+r.URL.Path)
	})})
	if err != nil {
		h.t.Fatal(err)
	}
	h.db, h.n, h.srv, h.h = db, n, srv, both(srv)
}

// both puts the page and the shop on one handler, FOR THE TESTS ONLY, which
// ask for /ctf/ and /juiceshop/ alike. In the image they are two origins on
// two ports, and must stay so (page.go says why).
func both(srv *Server) http.Handler {
	page, shop := srv.Page(), srv.Shop()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/juiceshop/") || strings.HasPrefix(r.URL.Path, "/assets/") {
			shop.ServeHTTP(w, r)
			return
		}
		page.ServeHTTP(w, r)
	})
}

func (h *harness) poll() { h.n.Poll(context.Background()) }

func (h *harness) join(name, code string) *JoinResult {
	h.t.Helper()
	res, err := h.n.Join(context.Background(), name, code)
	if err != nil {
		h.t.Fatal(err)
	}
	return res
}

func (h *harness) report() error { return h.n.Report(context.Background()) }

// req sends a request to the VM's web server with the harness's cookies.
func (h *harness) req(method, path string, form url.Values) *httptest.ResponseRecorder {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	r := httptest.NewRequest(method, path, body)
	if form != nil {
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for k, v := range h.jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(h.jar, c.Name)
		} else {
			h.jar[c.Name] = c.Value
		}
	}
	return w
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

// submit posts a form of the page, with the page's CSRF token.
func (h *harness) submit(path string, form url.Values) *httptest.ResponseRecorder {
	h.t.Helper()
	m := csrfRE.FindStringSubmatch(h.req("GET", "/ctf/", nil).Body.String())
	if m == nil {
		h.t.Fatal("no CSRF token on the page")
	}
	form.Set("csrf", m[1])
	return h.req("POST", path, form)
}

// failingTransport fails every request, and counts them.
type failingTransport struct{ calls atomic.Int32 }

func (f *failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.calls.Add(1)
	return nil, &net.OpError{Op: "dial", Err: errors.New("no outbound request is allowed in this test")}
}
