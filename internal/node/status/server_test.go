package status

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/node/agent"
	"github.com/macsnoeren/juiceshop-ctf/internal/node/gate"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/names"
	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
)

type fakeAgent struct {
	mu    sync.Mutex
	reg   *protocol.Registration
	down  bool
	calls atomic.Int64
	delay time.Duration
}

func (f *fakeAgent) Registration(ctx context.Context) (*protocol.Registration, error) {
	f.calls.Add(1)
	time.Sleep(f.delay)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("connection refused")
	}
	if f.reg == nil {
		return nil, nil
	}
	r := *f.reg
	return &r, nil
}

func (f *fakeAgent) Register(ctx context.Context, name string) (*protocol.Registration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("down")
	}
	if f.reg != nil {
		return nil, ErrRegistered
	}
	n, err := names.Normalize(name)
	if err != nil {
		return nil, NameError{Msg: err.Error()}
	}
	f.reg = &protocol.Registration{ID: strings.Repeat("ab", 16), Name: n, RegisteredAt: time.Now()}
	return f.reg, nil
}

func (f *fakeAgent) State(ctx context.Context) (*agent.StateView, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, errors.New("down")
	}
	return &agent.StateView{Instance: "cybersecurity-1", Registration: f.reg, Solved: 1, Total: 98,
		Categories: []agent.Category{{Name: "Injection", Solved: 1, Total: 10}},
		Recent:     []agent.SolveView{{Name: "Score Board", Difficulty: 1, SolvedAt: time.Now()}}}, nil
}

func (f *fakeAgent) reset() {
	f.mu.Lock()
	f.reg = &protocol.Registration{ID: strings.Repeat("cd", 16), Name: "Next Student", RegisteredAt: time.Now()}
	f.mu.Unlock()
}

var secret = []byte("gate-secret-for-tests-0123456789")

type tclock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *tclock) Now() time.Time      { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *tclock) Add(d time.Duration) { c.mu.Lock(); c.t = c.t.Add(d); c.mu.Unlock() }

func newServer(t *testing.T, secure bool) (*Server, *fakeAgent, *tclock) {
	t.Helper()
	ag := &fakeAgent{}
	s, err := New(Config{
		BasePath: "/ctf", AccessCode: "test1", GateSecret: secret, CookieSecure: secure,
		JuiceShopPath: "/juiceshop/", LeaderboardURL: "/leaderboard/",
	}, ag, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	c := &tclock{t: time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)}
	s.now = c.Now
	s.limiter.now = c.Now
	return s, ag, c
}

func get(s *Server, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("GET", path, nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

// csrf fetches the index page and returns the CSRF cookie.
func csrf(t *testing.T, s *Server) *http.Cookie {
	t.Helper()
	for _, c := range get(s, "/ctf/").Result().Cookies() {
		if c.Name == csrfCookie {
			return c
		}
	}
	t.Fatal("no csrf cookie")
	return nil
}

func post(s *Server, path string, form url.Values, ip string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Real-IP", ip)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func gateCookie(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == gate.CookieName {
			return c
		}
	}
	return nil
}

var urlAttr = regexp.MustCompile(`(?:href|action|src)="([^"]*)"`)

func checkLinks(t *testing.T, page, body string) {
	t.Helper()
	for _, m := range urlAttr.FindAllStringSubmatch(body, -1) {
		u := m[1]
		if !strings.HasPrefix(u, "/") {
			continue
		}
		if !strings.HasPrefix(u, "/ctf/") && u != "/juiceshop/" && u != "/leaderboard/" {
			t.Errorf("%s: link %q is not under the base path", page, u)
		}
	}
}

func TestPagesAndSignUp(t *testing.T) {
	for _, secure := range []bool{true, false} {
		s, ag, _ := newServer(t, secure)

		rec := get(s, "/ctf/")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `action="/ctf/register"`) {
			t.Fatalf("unregistered: %d, no registration form", rec.Code)
		}
		checkLinks(t, "register", body)
		if !strings.Contains(rec.Header().Get("Content-Security-Policy"), "script-src 'self'") {
			t.Error("no CSP header")
		}

		cs := csrf(t, s)
		form := url.Values{"csrf": {cs.Value}, "name": {"Zoë  de Vries"}, "code": {"wrong"}}
		rec = post(s, "/ctf/register", form, "10.0.0.1", cs)
		if rec.Code != http.StatusForbidden || gateCookie(rec) != nil || !strings.Contains(rec.Body.String(), "not right") {
			t.Errorf("wrong code: %d, cookie %v", rec.Code, gateCookie(rec))
		}
		if ag.reg != nil {
			t.Error("a wrong code registered the instance")
		}

		form.Set("code", "test1")
		rec = post(s, "/ctf/register", form, "10.0.0.1", cs)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/juiceshop/" {
			t.Fatalf("register: %d → %q", rec.Code, rec.Header().Get("Location"))
		}
		gc := gateCookie(rec)
		if gc == nil || !gc.HttpOnly || gc.Secure != secure || gc.Path != "/" || gc.SameSite != http.SameSiteLaxMode {
			t.Fatalf("gate cookie = %+v (secure should be %v)", gc, secure)
		}
		if ag.reg.Name != "Zoë de Vries" {
			t.Errorf("name = %q", ag.reg.Name)
		}

		rec = get(s, "/ctf/")
		if !strings.Contains(rec.Body.String(), `action="/ctf/enter"`) {
			t.Error("registered without cookie: no re-entry form")
		}
		checkLinks(t, "enter", rec.Body.String())

		rec = get(s, "/ctf/", gc)
		body = rec.Body.String()
		if !strings.Contains(body, "Zoë de Vries") || !strings.Contains(body, "Injection") {
			t.Error("progress page does not show name and categories")
		}
		checkLinks(t, "progress", body)

		cs = csrf(t, s)
		rec = post(s, "/ctf/enter", url.Values{"csrf": {cs.Value}, "code": {"test1"}}, "10.0.0.2", cs)
		if rec.Code != http.StatusSeeOther || gateCookie(rec) == nil {
			t.Errorf("re-entry: %d", rec.Code)
		}
	}
}

func TestCSRFRequired(t *testing.T) {
	s, ag, _ := newServer(t, true)
	rec := post(s, "/ctf/register", url.Values{"name": {"Zoë"}, "code": {"test1"}}, "10.0.0.1")
	if rec.Code != http.StatusForbidden || ag.reg != nil {
		t.Errorf("POST without CSRF: %d", rec.Code)
	}
}

func TestBadNameShowsReason(t *testing.T) {
	s, _, _ := newServer(t, true)
	cs := csrf(t, s)
	rec := post(s, "/ctf/register", url.Values{"csrf": {cs.Value}, "name": {"<b>"}, "code": {"test1"}}, "10.0.0.1", cs)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "letters") {
		t.Errorf("bad name: %d", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "<b>") {
		t.Error("the name was echoed unescaped")
	}
}

func TestGate(t *testing.T) {
	s, ag, clk := newServer(t, true)
	ag.reg = &protocol.Registration{ID: strings.Repeat("ab", 16), Name: "Zoë"}
	valid := &http.Cookie{Name: gate.CookieName, Value: gate.Sign(ag.reg.ID, clk.Now().Add(time.Hour), secret)}
	expired := &http.Cookie{Name: gate.CookieName, Value: gate.Sign(ag.reg.ID, clk.Now().Add(-time.Second), secret)}
	forged := &http.Cookie{Name: gate.CookieName, Value: gate.Sign(ag.reg.ID, clk.Now().Add(time.Hour), []byte("other"))}

	check := func(name string, want int, c ...*http.Cookie) {
		t.Helper()
		if rec := get(s, "/ctf/gate", c...); rec.Code != want {
			t.Errorf("%s: %d, want %d", name, rec.Code, want)
		}
	}
	check("no cookie", 401)
	check("forged", 401, forged)
	check("expired", 401, expired)
	check("valid", 204, valid)

	ag.reset()
	clk.Add(6 * time.Second) // past the cache
	check("valid but registration reset", 401, valid)

	ag.down = true
	clk.Add(6 * time.Second)
	newCookie := &http.Cookie{Name: gate.CookieName, Value: gate.Sign(strings.Repeat("cd", 16), clk.Now().Add(time.Hour), secret)}
	check("agent down", 401, newCookie)
	ag.down = false
	clk.Add(3 * time.Second) // past the error TTL
	check("agent back", 204, newCookie)
}

func TestGateCachesAgentCalls(t *testing.T) {
	s, ag, clk := newServer(t, true)
	s.now = time.Now // real time, concurrent requests
	ag.reg = &protocol.Registration{ID: strings.Repeat("ab", 16), Name: "Zoë"}
	ag.delay = 20 * time.Millisecond
	valid := &http.Cookie{Name: gate.CookieName, Value: gate.Sign(ag.reg.ID, time.Now().Add(time.Hour), secret)}
	_ = clk
	var wg sync.WaitGroup
	for i := 0; i < 80; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if rec := get(s, "/ctf/gate", valid); rec.Code != 204 {
				t.Errorf("gate: %d", rec.Code)
			}
		}()
	}
	wg.Wait()
	if n := ag.calls.Load(); n != 1 {
		t.Errorf("80 concurrent gate checks made %d agent calls, want 1", n)
	}
}

func TestRateLimit(t *testing.T) {
	s, ag, clk := newServer(t, true)
	ag.reg = &protocol.Registration{ID: strings.Repeat("ab", 16), Name: "Zoë"}
	cs := csrf(t, s)
	try := func(code string) int {
		return post(s, "/ctf/enter", url.Values{"csrf": {cs.Value}, "code": {code}}, "192.0.2.7", cs).Code
	}
	for i := 0; i < 5; i++ {
		if c := try("guess"); c != http.StatusForbidden {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	if c := try("test1"); c != http.StatusTooManyRequests {
		t.Errorf("6th attempt with the right code: %d, want 429", c)
	}
	if c := post(s, "/ctf/enter", url.Values{"csrf": {cs.Value}, "code": {"test1"}}, "192.0.2.8", cs).Code; c != http.StatusSeeOther {
		t.Errorf("another address is not limited: %d", c)
	}
	clk.Add(time.Minute)
	if c := try("test1"); c != http.StatusSeeOther {
		t.Errorf("after a minute: %d, want 303", c)
	}
}

func TestUnavailableWhenAgentDown(t *testing.T) {
	s, ag, _ := newServer(t, true)
	ag.down = true
	if rec := get(s, "/ctf/"); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "unavailable") {
		t.Errorf("agent down: %d", rec.Code)
	}
}
