package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// fakeCentral records reports and answers with a configurable status and body.
type fakeCentral struct {
	mu      sync.Mutex
	status  int
	resp    protocol.Response
	reports []protocol.Report
	times   []time.Time
	auth    []string
}

func (f *fakeCentral) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var rep protocol.Report
	json.Unmarshal(b, &rep)
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, rep)
	f.times = append(f.times, time.Now())
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if f.status != 0 && f.status != 200 {
		w.WriteHeader(f.status)
		return
	}
	json.NewEncoder(w).Encode(f.resp)
}

func (f *fakeCentral) last() protocol.Report {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reports[len(f.reports)-1]
}

func TestReportValidatesAndCarriesSolves(t *testing.T) {
	c := &fakeCentral{resp: protocol.Response{OK: true, Standing: &protocol.Standing{Score: 100, Rank: 1, Of: 3}}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	h := newHarness(t, Config{CentralURL: srv.URL + "/", CentralToken: "tok"})
	h.poll()
	h.register("Zoë")
	h.shop.solve("scoreBoardChallenge", "xxeFileDisclosureChallenge")
	h.poll()

	if err := h.a.Report(context.Background()); err != nil {
		t.Fatal(err)
	}
	rep := c.last()
	if err := rep.Validate(); err != nil {
		t.Fatalf("report does not validate: %v", err)
	}
	if c.auth[0] != "Bearer tok" {
		t.Errorf("Authorization = %q", c.auth[0])
	}
	if rep.Registration == nil || rep.Registration.Name != "Zoë" || len(rep.Challenges) != 4 || !rep.JuiceShop.Reachable {
		t.Errorf("report = %+v", rep)
	}
	solved := 0
	for _, ch := range rep.Challenges {
		if ch.SolvedAt != nil {
			solved++
		}
		if ch.Key == "xxeFileDisclosureChallenge" && !ch.Disabled {
			t.Error("disabled flag missing")
		}
	}
	if solved != 1 {
		t.Errorf("%d solved in report, want 1", solved)
	}
	if v := h.a.View(); v.Standing == nil || v.Standing.Score != 100 || v.LastReportAt == nil {
		t.Errorf("standing not kept: %+v", v)
	}
}

func TestResetCommandIsAppliedOnce(t *testing.T) {
	c := &fakeCentral{resp: protocol.Response{OK: true, Commands: []protocol.Command{
		{ID: "c-17", Type: protocol.CmdResetRegistration},
		{ID: "c-18", Type: "unknown_thing"},
	}}}
	srv := httptest.NewServer(c)
	defer srv.Close()
	h := newHarness(t, Config{CentralURL: srv.URL, CentralToken: "tok"})
	h.poll()
	h.register("Zoë")
	h.shop.solve("scoreBoardChallenge")
	h.poll()

	h.a.Report(context.Background())
	if h.a.Registration() != nil || len(h.a.st.Solved) != 0 || !h.a.st.ContinueCodes.Empty() {
		t.Fatal("reset did not clear registration, solves and codes")
	}
	h.a.Report(context.Background())
	rep := c.last()
	if len(rep.CommandsApplied) != 1 || rep.CommandsApplied[0] != "c-17" {
		t.Errorf("commands_applied = %v, want [c-17]", rep.CommandsApplied)
	}
	if rep.Registration != nil {
		t.Error("report after reset still has a registration")
	}

	// A new student signs up; the same command id comes back once more
	// (it was not yet confirmed) and must NOT reset the new registration.
	h.register("Second Student")
	h.a.Report(context.Background())
	if h.a.Registration() == nil {
		t.Error("a repeated command id was applied twice")
	}
}

func TestReporterBacksOffAndLogs401Hourly(t *testing.T) {
	c := &fakeCentral{status: http.StatusServiceUnavailable}
	srv := httptest.NewServer(c)
	defer srv.Close()
	h := newHarness(t, Config{
		CentralURL: srv.URL, CentralToken: "tok",
		ReportDebounce: 10 * time.Millisecond, BackoffBase: 20 * time.Millisecond, BackoffMax: time.Second,
		ReportInterval: time.Hour,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	h.a.RunReporter(ctx)

	c.mu.Lock()
	times := append([]time.Time(nil), c.times...)
	c.mu.Unlock()
	if len(times) < 4 {
		t.Fatalf("only %d attempts", len(times))
	}
	var gaps []time.Duration
	for i := 1; i < len(times); i++ {
		gaps = append(gaps, times[i].Sub(times[i-1]))
	}
	if !(gaps[len(gaps)-1] > 2*gaps[0]) {
		t.Errorf("gaps do not grow: %v", gaps)
	}

	// 401: an error line once, however often it happens within the hour.
	c.mu.Lock()
	c.status = http.StatusUnauthorized
	c.mu.Unlock()
	for i := 0; i < 5; i++ {
		err := h.a.Report(context.Background())
		h.a.logReportError(err, time.Second)
		h.clock.Add(5 * time.Minute)
	}
	if n := strings.Count(h.logs.String(), "REFUSES THIS INSTANCE'S TOKEN"); n != 1 {
		t.Errorf("401 logged %d times within the hour, want 1", n)
	}
	h.clock.Add(time.Hour)
	h.a.logReportError(errUnauthorized, time.Second)
	if n := strings.Count(h.logs.String(), "REFUSES THIS INSTANCE'S TOKEN"); n != 2 {
		t.Errorf("401 not logged again after an hour (%d)", n)
	}
}

func TestBackoffBounds(t *testing.T) {
	for f := 1; f < 20; f++ {
		d := backoff(f, 2*time.Second, 5*time.Minute)
		if d < 1600*time.Millisecond || d > 6*time.Minute {
			t.Errorf("backoff(%d) = %v out of bounds", f, d)
		}
	}
	if backoff(1, 2*time.Second, 5*time.Minute) > 3*time.Second {
		t.Error("first retry should be about 2 s")
	}
}
