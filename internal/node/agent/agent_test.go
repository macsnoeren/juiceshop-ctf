package agent

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	protocol "github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol/v1"
)

func TestRestoreAfterJuiceShopRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.register("Sanne de Vries")
	h.shop.solve("scoreBoardChallenge", "loginAdminChallenge")
	h.poll()
	if h.a.st.ContinueCodes.Empty() {
		t.Fatal("continue codes were not fetched after a new solve")
	}

	h.shop.restart()
	h.poll()
	if h.shop.applied != 1 {
		t.Fatalf("applied %d times, want 1", h.shop.applied)
	}
	if !h.shop.solved["scoreBoardChallenge"] || !h.shop.solved["loginAdminChallenge"] {
		t.Error("juice shop progress not restored")
	}
	if h.a.st.RestoreFailed {
		t.Error("restore_failed set after a successful restore")
	}
	if !strings.Contains(h.logs.String(), "progress restored") {
		t.Error("a restore must be logged")
	}

	h.poll()
	if h.shop.applied != 1 {
		t.Error("codes applied again although nothing was missing")
	}
}

func TestRestoreFailureIsFlaggedAndNotRetriedInTheSameBoot(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.register("Sanne de Vries")
	h.shop.solve("scoreBoardChallenge")
	h.poll()

	h.shop.restart()
	h.shop.applyFails = true
	h.poll()
	h.poll()
	h.poll()
	if h.shop.applied != 1 {
		t.Errorf("applied %d times in one boot, want 1", h.shop.applied)
	}
	if !h.a.st.RestoreFailed || !h.a.BuildReport().JuiceShop.RestoreFailed {
		t.Error("restore_failed not set / not reported")
	}
	if len(h.a.st.Solved) != 1 {
		t.Error("a failed restore must not remove the recorded solve")
	}

	h.shop.restart()
	h.poll()
	if h.shop.applied != 2 {
		t.Errorf("a new boot must get a new attempt: applied %d", h.shop.applied)
	}
}

func TestNoRestoreWithoutRegistration(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	h.shop.restart()
	h.poll()
	if h.shop.applied != 0 {
		t.Error("restored without a registration")
	}
}

func TestBaselineAtRegistration(t *testing.T) {
	h := newHarness(t, Config{})
	h.shop.solve("scoreBoardChallenge") // left over from someone else
	h.poll()
	h.register("Sanne de Vries")
	h.shop.solve("loginAdminChallenge")
	h.poll()
	r := h.a.BuildReport()
	for _, c := range r.Challenges {
		if c.Key == "scoreBoardChallenge" && c.SolvedAt != nil {
			t.Error("a challenge solved before registration was counted")
		}
		if c.Key == "loginAdminChallenge" && c.SolvedAt == nil {
			t.Error("a challenge solved after registration was not counted")
		}
	}
	// After a restart the baseline is gone: solving it now counts.
	h.shop.restart()
	h.poll()
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	if _, ok := h.a.st.Solved["scoreBoardChallenge"]; !ok {
		t.Error("in a new boot the challenge must count")
	}
}

func TestWebhook(t *testing.T) {
	h := newHarness(t, Config{PollInterval: time.Hour, PollDebounce: 300 * time.Millisecond})
	h.a.now = time.Now
	srv := httptest.NewServer(h.a.WebhookHandler())
	defer srv.Close()

	resp, _ := http.Get(srv.URL + "/webhook")
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d, want 405", resp.StatusCode)
	}
	big := strings.NewReader(strings.Repeat("x", maxWebhookBody+1))
	resp, _ = http.Post(srv.URL+"/webhook", "application/json", big)
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("large body: %d, want 413", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.a.RunPoller(ctx) }()

	h.a.Register("Sanne de Vries")
	start := h.a.polls.Load()
	forged := `{"solution":{"challenge":"loginAdminChallenge","cheatScore":0.1}}`
	for i := 0; i < 20; i++ {
		resp, err := http.Post(srv.URL+"/webhook", "application/json", strings.NewReader(forged))
		if err != nil || resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST: %v %v", resp, err)
		}
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)
	cancel()
	wg.Wait()
	if n := h.a.polls.Load() - start; n < 1 || n > 3 {
		t.Errorf("20 webhooks in 0.5 s caused %d polls, want 1-3 (debounce %v)", n, h.a.cfg.PollDebounce)
	}
	if len(h.a.st.Solved) != 0 {
		t.Error("a webhook body changed the solved set")
	}
}

func api(t *testing.T, a *Agent, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	a.APIHandler().ServeHTTP(rec, req)
	return rec
}

func TestInternalAPI(t *testing.T) {
	h := newHarness(t, Config{InternalToken: "s3cret-internal-token"})
	h.poll()
	tok := "s3cret-internal-token"

	for _, tc := range []string{"", "wrong", tok + "x"} {
		if rec := api(t, h.a, "GET", "/state", tc, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("token %q: %d, want 401", tc, rec.Code)
		}
	}
	if rec := api(t, h.a, "GET", "/healthz", "", ""); rec.Code != http.StatusNoContent {
		t.Errorf("healthz: %d", rec.Code)
	}
	if rec := api(t, h.a, "GET", "/registration", tok, ""); rec.Code != http.StatusNotFound {
		t.Errorf("registration before sign-up: %d, want 404", rec.Code)
	}
	if rec := api(t, h.a, "POST", "/registration", tok, `{"name":"<script>"}`); rec.Code != http.StatusBadRequest ||
		!strings.Contains(rec.Body.String(), "letters") {
		t.Errorf("bad name: %d %s", rec.Code, rec.Body)
	}
	rec := api(t, h.a, "POST", "/registration", tok, `{"name":"  Sanne  de Vries "}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("register: %d %s", rec.Code, rec.Body)
	}
	var reg protocol.Registration
	json.Unmarshal(rec.Body.Bytes(), &reg)
	if reg.Name != "Sanne de Vries" || !protocol.ValidRegistrationID(reg.ID) {
		t.Errorf("registration = %+v", reg)
	}
	if rec := api(t, h.a, "POST", "/registration", tok, `{"name":"Someone Else"}`); rec.Code != http.StatusConflict {
		t.Errorf("second registration: %d, want 409", rec.Code)
	}

	// It survives a restart of the agent: it is in the state file.
	again := h.newAgent(Config{InternalToken: tok})
	rec = api(t, again, "GET", "/registration", tok, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), reg.ID) {
		t.Errorf("after restart: %d %s", rec.Code, rec.Body)
	}
	rec = api(t, again, "GET", "/state", tok, "")
	var v StateView
	json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Registration == nil || v.Instance != "cybersecurity-1" {
		t.Errorf("state view = %+v", v)
	}
}
