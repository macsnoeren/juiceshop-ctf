package juiceshop

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fakeShop serves the fixtures and records the PUTs it receives.
type fakeShop struct {
	t        *testing.T
	solved   bool
	mu       sync.Mutex
	applied  []string
	failWith int
}

func (f *fakeShop) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if f.failWith != 0 {
		w.WriteHeader(f.failWith)
		return
	}
	switch {
	case r.Method == http.MethodGet && r.URL.Path == "/api/Challenges":
		if f.solved {
			w.Write(fixture(f.t, "challenges-solved.json"))
		} else {
			w.Write(fixture(f.t, "challenges-fresh.json"))
		}
	case r.Method == http.MethodGet && r.URL.Path == "/rest/admin/application-version":
		w.Write(fixture(f.t, "application-version.json"))
	case r.Method == http.MethodGet && r.URL.Path == "/rest/continue-code":
		w.Write(fixture(f.t, "continue-code.json"))
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/rest/continue-code-"):
		w.Write(fixture(f.t, "continue-code-empty.json"))
	case r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, "/rest/continue-code"):
		f.mu.Lock()
		f.applied = append(f.applied, r.URL.Path)
		f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/bogus") {
			w.WriteHeader(http.StatusNotFound)
			w.Write(fixture(f.t, "apply-invalid.txt"))
			return
		}
		w.Write(fixture(f.t, "apply-ok.json"))
	default:
		http.NotFound(w, r)
	}
}

func TestChallenges(t *testing.T) {
	f := &fakeShop{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL)

	chs, err := c.Challenges(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(chs) != 116 {
		t.Errorf("got %d challenges, want 116", len(chs))
	}
	disabled, solved := 0, 0
	boot := chs[0].CreatedAt
	for _, ch := range chs {
		if ch.Disabled() {
			disabled++
		}
		if ch.Solved {
			solved++
		}
		if ch.Key == "" || ch.Name == "" || ch.Difficulty < 1 || ch.Difficulty > 6 {
			t.Errorf("incomplete challenge %+v", ch)
		}
		if !ch.CreatedAt.Equal(boot) {
			t.Errorf("createdAt differs between challenges; the boot id assumption is wrong")
		}
	}
	if disabled != 18 || solved != 0 {
		t.Errorf("disabled=%d solved=%d, want 18 and 0", disabled, solved)
	}

	f.solved = true
	chs, _ = c.Challenges(context.Background())
	var keys []string
	for _, ch := range chs {
		if ch.Solved {
			keys = append(keys, ch.Key)
		}
	}
	if strings.Join(keys, ",") != "directoryListingChallenge,exposedMetricsChallenge" &&
		strings.Join(keys, ",") != "exposedMetricsChallenge,directoryListingChallenge" {
		t.Errorf("solved = %v", keys)
	}
}

func TestVersionAndCodes(t *testing.T) {
	srv := httptest.NewServer(&fakeShop{t: t})
	defer srv.Close()
	c := New(srv.URL)
	v, err := c.Version(context.Background())
	if err != nil || v != "20.3.0" {
		t.Errorf("Version = %q, %v", v, err)
	}
	codes, err := c.ContinueCodes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if codes.Challenges == "" || codes.FindIt != "" || codes.FixIt != "" {
		t.Errorf("codes = %+v; want only the challenges code", codes)
	}
}

func TestApplySkipsEmptyAndReportsInvalid(t *testing.T) {
	f := &fakeShop{t: t}
	srv := httptest.NewServer(f)
	defer srv.Close()
	c := New(srv.URL)

	if err := c.ApplyContinueCodes(context.Background(), Codes{Challenges: "abc"}); err != nil {
		t.Fatal(err)
	}
	if len(f.applied) != 1 || f.applied[0] != "/rest/continue-code/apply/abc" {
		t.Errorf("applied %v; empty codes must be skipped", f.applied)
	}
	err := c.ApplyContinueCodes(context.Background(), Codes{Challenges: "bogus", FixIt: "ok"})
	if !errors.Is(err, ErrInvalidCode) {
		t.Errorf("err = %v, want ErrInvalidCode", err)
	}
	if len(f.applied) != 3 {
		t.Errorf("a refused code must not stop the next one: applied %v", f.applied)
	}
}

func TestErrors(t *testing.T) {
	srv := httptest.NewServer(&fakeShop{t: t, failWith: 500})
	defer srv.Close()
	if _, err := New(srv.URL).Challenges(context.Background()); err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("HTTP 500: err = %v", err)
	}

	html := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html>not json</html>"))
	}))
	defer html.Close()
	if _, err := New(html.URL).Challenges(context.Background()); err == nil {
		t.Error("a body that is not JSON must be an error")
	}

	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
		case <-r.Context().Done():
		}
	}))
	defer slow.Close()
	c := New(slow.URL)
	c.http.Timeout = 100 * time.Millisecond
	start := time.Now()
	if _, err := c.Challenges(context.Background()); err == nil {
		t.Error("a timeout must be an error")
	}
	if time.Since(start) > time.Second {
		t.Error("the timeout was not applied")
	}
}

func TestWebhookFixtureShape(t *testing.T) {
	// The agent only reads solution.challenge and solution.cheatScore from the
	// webhook (DESIGN.md §6.1); make sure the captured body has them.
	b := string(fixture(t, "webhook.json"))
	for _, want := range []string{`"challenge":"exposedMetricsChallenge"`, `"cheatScore":`} {
		if !strings.Contains(b, want) {
			t.Errorf("webhook fixture lacks %s", want)
		}
	}
}
