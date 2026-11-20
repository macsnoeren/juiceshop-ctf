// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package node

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// ---- node.db ------------------------------------------------------------------

func TestNodeDBModesAndMigrations(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "ctf")
	path := filepath.Join(dir, "node.db")
	db, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	st := newState()
	st.Membership = &Membership{Status: StatusLocal, Name: "Zoë", NodeUID: protocol.NewNodeUID(), JoinedAt: time.Now()}
	st.Solved["scoreBoardChallenge"] = time.Now()
	st.Coding[CodingKey{"scoreBoardChallenge", protocol.PhaseFindIt}] = time.Now()
	st.CodingBaseline = []CodingKey{{"loginAdminChallenge", protocol.PhaseFindIt}}
	st.BaselineBoot = "boot-1"
	if err := db.Save(st); err != nil {
		t.Fatal(err)
	}
	v1, _ := db.SchemaVersion()
	db.Close()
	db, err = OpenStore(path) // twice: a no-op
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	v2, _ := db.SchemaVersion()
	got, _ := db.Load()
	if want := len(nodeMigrations); v1 != want || v2 != want || got.Membership == nil || len(got.Solved) != 1 ||
		len(got.Coding) != 1 || len(got.CodingBaseline) != 1 || got.BaselineBoot != "boot-1" {
		t.Errorf("schema %d→%d, state %+v", v1, v2, got)
	}
	if fi, _ := os.Stat(dir); fi.Mode().Perm() != 0o700 {
		t.Errorf("directory mode %v, want 0700", fi.Mode().Perm())
	}
	for _, f := range []string{"node.db", "node.db-wal", "node.db-shm"} {
		fi, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			if f == "node.db" {
				t.Fatal(err)
			}
			continue
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s mode %v, want 0600", f, fi.Mode().Perm())
		}
	}
}

func TestImportV1(t *testing.T) {
	h := newHarness(t, Config{})
	v1 := `{"version":1,"registration":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"Sanne","registered_at":"2026-11-18T10:00:00Z"},
		"solved":{"scoreBoardChallenge":"2026-11-18T10:05:00Z","loginAdminChallenge":"2026-11-18T10:07:00Z"},
		"cheat":{"loginAdminChallenge":0.2},"continue_codes":{"challenges":"code:loginAdminChallenge+scoreBoardChallenge"},"commands_applied":[]}`
	path := filepath.Join(h.dir, "state.json")
	os.WriteFile(path, []byte(v1), 0o600)
	n, err := h.n.ImportV1(path)
	if err != nil || n != 2 {
		t.Fatalf("import: %d %v", n, err)
	}
	st, _ := h.db.Load()
	if len(st.Solved) != 2 || st.Membership != nil || st.ContinueCodes.Empty() {
		t.Errorf("after import: %d solves, membership %v, codes %v", len(st.Solved), st.Membership, st.ContinueCodes)
	}
	if _, err := os.Stat(path + ".imported"); err != nil {
		t.Error("state.json was not renamed")
	}
	if n, _ := h.n.ImportV1(path); n != -1 {
		t.Error("imported twice")
	}
}

// ---- the watcher ----------------------------------------------------------------

func TestRestoreAfterJuiceShopRestart(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.join("Sanne de Vries", "")
	h.shop.solve("scoreBoardChallenge", "loginAdminChallenge")
	h.poll()
	if h.n.st.ContinueCodes.Empty() {
		t.Fatal("continue codes were not fetched after a new solve")
	}
	h.shop.restart()
	h.poll()
	if h.shop.applied != 1 || h.shop.nSolved() != 2 {
		t.Fatalf("applied %d, %d solved in the shop", h.shop.applied, h.shop.nSolved())
	}
	if h.n.st.RestoreFailed || !strings.Contains(h.logs.String(), "progress restored") {
		t.Error("a successful restore must be logged and not flagged")
	}
	h.poll()
	if h.shop.applied != 1 {
		t.Error("codes applied again although nothing was missing")
	}
	// and it all survives a restart of ctf-node itself
	h.open(Config{})
	if len(h.n.st.Solved) != 2 || h.n.st.ContinueCodes.Empty() || h.n.st.Membership == nil {
		t.Error("node.db lost the state across a restart of ctf-node")
	}
}

func TestRestoreFailureIsFlaggedAndNotRetriedInTheSameBoot(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.join("Sanne de Vries", "")
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	h.shop.restart()
	h.shop.applyFails = true
	h.poll()
	h.poll()
	h.poll()
	if h.shop.applied != 1 || !h.n.st.RestoreFailed || len(h.n.st.Solved) != 1 {
		t.Errorf("applied %d, restore_failed %v, %d solves", h.shop.applied, h.n.st.RestoreFailed, len(h.n.st.Solved))
	}
	h.shop.restart()
	h.poll()
	if h.shop.applied != 2 {
		t.Errorf("a new boot must get a new attempt: applied %d", h.shop.applied)
	}
}

func TestNoRestoreAndNoSolvesWithoutJoining(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	h.shop.restart()
	h.poll()
	if h.shop.applied != 0 || len(h.n.st.Solved) != 0 {
		t.Error("recorded or restored without a student")
	}
}

func TestBaselineAtJoin(t *testing.T) {
	h := newHarness(t, Config{})
	h.shop.solve("scoreBoardChallenge") // from before the join
	h.poll()
	h.join("Sanne de Vries", "")
	h.shop.solve("loginAdminChallenge")
	h.poll()
	if _, ok := h.n.st.Solved["scoreBoardChallenge"]; ok {
		t.Error("a challenge solved before the join counts")
	}
	if _, ok := h.n.st.Solved["loginAdminChallenge"]; !ok {
		t.Error("a challenge solved after the join does not count")
	}
	h.shop.restart()
	h.poll()
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	if _, ok := h.n.st.Solved["scoreBoardChallenge"]; !ok {
		t.Error("in a new boot the challenge must count")
	}
}

// ---- coding challenges: recorded, reported, restored like solves ----

func TestCodingPhasesAreRecordedReportedAndRestored(t *testing.T) {
	c := newCentral(t)
	g, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	h.join("Zoë", code)
	h.shop.solve("scoreBoardChallenge")
	h.shop.code("scoreBoardChallenge", juiceshop.CodingFound)
	h.poll()
	if err := h.report(); err != nil {
		t.Fatal(err)
	}
	if s := h.n.st.Standing; s == nil || s.Score != 150 {
		t.Errorf("after Find it: standing %+v, want 100 + 50", s)
	}
	h.shop.code("scoreBoardChallenge", juiceshop.CodingFixed)
	h.poll()
	rep, _, _, _ := h.n.BuildReport()
	if len(rep.Coding) != 2 || rep.Coding[0].Phase != protocol.PhaseFindIt || rep.Coding[1].Phase != protocol.PhaseFixIt {
		t.Fatalf("coding in the report: %+v", rep.Coding)
	}
	if err := h.report(); err != nil {
		t.Fatal(err)
	}
	if s := h.n.st.Standing; s == nil || s.Score != 200 {
		t.Errorf("after Fix it: standing %+v, want 200", s)
	}
	if ps := c.participants(t, g); ps[0].Solved != 1 || ps[0].Coding != 2 {
		t.Errorf("central: %d solved, %d coding", ps[0].Solved, ps[0].Coding)
	}
	if v := h.n.View(); v.Coding != 2 || v.CodingTotal != 6 || v.LocalScore != 200 {
		t.Errorf("page: coding %d/%d, local score %d", v.Coding, v.CodingTotal, v.LocalScore)
	}
	if body := h.req("GET", "/ctf/", nil).Body.String(); !strings.Contains(body, "Fix it") {
		t.Error("the page does not show the coding phases")
	}

	// Juice Shop restarts: the find-it and fix-it codes bring the coding
	// progress back, and nothing is reported as lost.
	first := h.n.st.Coding[CodingKey{"scoreBoardChallenge", protocol.PhaseFindIt}]
	h.shop.restart()
	h.poll()
	if s := h.shop.codingStatus("scoreBoardChallenge"); s != juiceshop.CodingFixed || h.n.st.RestoreFailed {
		t.Errorf("after the restart: coding status %d in the shop, restore_failed %v", s, h.n.st.RestoreFailed)
	}
	if got := h.n.st.Coding[CodingKey{"scoreBoardChallenge", protocol.PhaseFindIt}]; !got.Equal(first) {
		t.Error("the restore moved the first time of a coding phase")
	}

	// only coding progress lost: still a restore, and flagged when it fails
	h.shop.code("scoreBoardChallenge", juiceshop.CodingNone)
	h.shop.restart()
	h.shop.solve("scoreBoardChallenge")
	h.shop.applyFails = true
	h.poll()
	if !h.n.st.RestoreFailed {
		t.Error("lost coding progress that could not be restored is not flagged")
	}
	h.shop.applyFails = false

	h.open(Config{DefaultCentralURL: c.URL})
	if len(h.n.st.Coding) != 2 {
		t.Error("node.db lost the coding phases across a restart of ctf-node")
	}
	if w := h.submit("/ctf/forget", url.Values{"confirm": {"forget"}}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	assertForgotten(t, h)
}

func TestCodingBeforeTheJoinDoesNotCount(t *testing.T) {
	h := newHarness(t, Config{})
	h.shop.solve("scoreBoardChallenge")
	h.shop.code("scoreBoardChallenge", juiceshop.CodingFound) // from before the join
	h.poll()
	h.join("Sanne de Vries", "")
	h.shop.code("scoreBoardChallenge", juiceshop.CodingFixed)
	h.shop.solve("loginAdminChallenge")
	h.shop.code("loginAdminChallenge", juiceshop.CodingFound)
	h.poll()
	want := map[CodingKey]bool{{"scoreBoardChallenge", protocol.PhaseFixIt}: true, {"loginAdminChallenge", protocol.PhaseFindIt}: true}
	if len(h.n.st.Coding) != len(want) {
		t.Errorf("coding %v, want %v", h.n.st.Coding, want)
	}
	for k := range want {
		if _, ok := h.n.st.Coding[k]; !ok {
			t.Errorf("%v after the join does not count", k)
		}
	}
}

func TestWebhook(t *testing.T) {
	h := newHarness(t, Config{PollInterval: time.Hour, PollDebounce: 300 * time.Millisecond})
	srv := httptest.NewServer(h.n.WebhookHandler())
	defer srv.Close()
	if resp, _ := http.Get(srv.URL + "/webhook"); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", resp.StatusCode)
	}
	if resp, _ := http.Post(srv.URL+"/webhook", "application/json", strings.NewReader(strings.Repeat("x", maxWebhookBody+1))); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("large body: %d", resp.StatusCode)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); h.n.RunPoller(ctx) }()
	h.join("Sanne de Vries", "")
	start := h.n.polls.Load()
	for range 20 {
		http.Post(srv.URL+"/webhook", "application/json", strings.NewReader(`{"solution":{"challenge":"loginAdminChallenge","cheatScore":0.1}}`))
		time.Sleep(25 * time.Millisecond)
	}
	time.Sleep(800 * time.Millisecond)
	cancel()
	wg.Wait()
	if n := h.n.polls.Load() - start; n < 1 || n > 3 {
		t.Errorf("20 webhooks caused %d polls, want 1-3", n)
	}
	if len(h.n.st.Solved) != 0 {
		t.Error("a webhook body changed the solved set")
	}
	if h.n.st.Cheat["loginAdminChallenge"] != 0.1 {
		t.Error("the cheat score of the webhook was not kept")
	}
}

// ---- joining and reporting --------------------------------------------------

func TestJoinKeepsTheTokenNotTheCode(t *testing.T) {
	c := newCentral(t)
	g, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	if r := h.join("Zoë de Vries", strings.ToUpper(protocol.FormatCode(code))); r.Outcome != JoinedOnline {
		t.Fatalf("outcome %v", r.Outcome)
	}
	tok := h.n.st.Membership.Token
	h.db.Checkpoint()
	var all []byte
	for _, f := range []string{"node.db", "node.db-wal"} {
		b, _ := os.ReadFile(filepath.Join(h.dir, f))
		all = append(all, b...)
	}
	if !bytes.Contains(all, []byte(tok)) {
		t.Error("the token is not in node.db")
	}
	if bytes.Contains(all, []byte(code)) || bytes.Contains(all, []byte(strings.ToUpper(code))) {
		t.Error("the join code is in node.db")
	}
	if ps := c.participants(t, g); len(ps) != 1 || ps[0].NodeUID != h.n.st.Membership.NodeUID {
		t.Errorf("central has %+v", ps)
	}

	h2 := newHarness(t, Config{DefaultCentralURL: c.URL})
	w := h2.submit("/ctf/join", url.Values{"name": {"Bram"}, "code": {"23456789ab"}})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "Unknown join code") || h2.n.st.Membership != nil {
		t.Errorf("a wrong code: %d, membership %v", w.Code, h2.n.st.Membership)
	}

	// The teacher closed joining: the student is told so, in words.
	c.st.SetJoiningClosed(g.ID, true)
	h3 := newHarness(t, Config{DefaultCentralURL: c.URL})
	w = h3.submit("/ctf/join", url.Values{"name": {"Late Ada"}, "code": {code}})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "joining this game is closed; ask your teacher") || h3.n.st.Membership != nil {
		t.Errorf("joining closed: %d, membership %v", w.Code, h3.n.st.Membership)
	}
}

func TestOfflineJoinArrivesLater(t *testing.T) {
	c := newCentral(t)
	g, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	c.down.Store(true)
	if r := h.join("Zoë", code); r.Outcome != JoinQueued || h.n.st.Membership.Status != StatusPending {
		t.Fatalf("join with the central server down: %v", r.Outcome)
	}
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	solvedAt := h.n.st.Solved["scoreBoardChallenge"]
	// Past the first retry time (BackoffBase is 1 ms in the harness): the
	// poll above can take less than that without the race detector.
	time.Sleep(5 * time.Millisecond)
	h.n.TryOutbox(context.Background())
	if h.n.st.Outbox == nil || h.n.st.Outbox.Attempts != 1 {
		t.Fatal("a failed retry did not stay in the outbox")
	}
	if !strings.Contains(h.req("GET", "/ctf/", nil).Body.String(), "Waiting for the central server") {
		t.Error("the page does not say the join is waiting")
	}
	c.down.Store(false)
	time.Sleep(10 * time.Millisecond)
	h.n.TryOutbox(context.Background())
	if h.n.st.Membership.Status != StatusJoined || h.n.st.Outbox != nil {
		t.Fatalf("after the server returned: %s", h.n.st.Membership.Status)
	}
	if err := h.report(); err != nil {
		t.Fatal(err)
	}
	var at string
	c.st.Ping()
	ps := c.participants(t, g)
	if len(ps) != 1 || ps[0].Solved != 1 {
		t.Fatalf("central has %+v", ps)
	}
	_ = at
	evs, _ := c.st.Events(storeFilter("solve"))
	if len(evs) != 1 {
		t.Errorf("solve events %d", len(evs))
	}
	b, _ := json.Marshal(h.n.st.Solved)
	if !strings.Contains(string(b), solvedAt.UTC().Format("2006-01-02T15:04:05")) {
		t.Error("the solve lost its original time")
	}

	// a queued join with a wrong code: refused later, the solves stay
	h2 := newHarness(t, Config{DefaultCentralURL: c.URL})
	h2.poll()
	c.down.Store(true)
	h2.join("Bram", "23456789ab")
	h2.shop.solve("loginAdminChallenge")
	h2.poll()
	c.down.Store(false)
	time.Sleep(5 * time.Millisecond) // past the retry time, as above
	h2.n.TryOutbox(context.Background())
	if h2.n.st.Membership.Status != StatusRefused || len(h2.n.st.Solved) != 1 {
		t.Fatalf("refused queued join: %s, %d solves", h2.n.st.Membership.Status, len(h2.n.st.Solved))
	}
	page := h2.req("GET", "/ctf/", nil).Body.String()
	if !strings.Contains(page, "Unknown join code") || !strings.Contains(page, `name="code"`) {
		t.Error("the page does not ask for the code again")
	}
	h2.join("Bram", code)
	if h2.n.st.Membership.Status != StatusJoined || len(h2.n.st.Solved) != 1 {
		t.Error("joining again after a refusal lost the solves")
	}
}

func TestReporterV2(t *testing.T) {
	c := newCentral(t)
	g, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	h.join("Zoë", code)
	h.shop.solve("scoreBoardChallenge", "xxeFileDisclosureChallenge")
	h.poll()
	rep, _, _, ok := h.n.BuildReport()
	if !ok || len(rep.Solves) != 1 || rep.Validate() != nil {
		t.Fatalf("report %+v", rep)
	}
	if err := h.report(); err != nil {
		t.Fatal(err)
	}
	if s := h.n.st.Standing; s == nil || s.Rank != 1 || s.Score != 100 {
		t.Errorf("standing %+v", s)
	}

	// The operator removes the participant: the VM forgets, exactly as the button.
	ps := c.participants(t, g)
	if err := c.st.Remove(ps[0].ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.report(); err != nil {
		t.Fatal(err)
	}
	assertForgotten(t, h)
	if k := h.keeper.restarts.Load(); k != 1 {
		t.Errorf("Juice Shop restarted %d times", k)
	}
	evs, _ := c.st.Events(storeFilter("command"))
	if len(evs) != 3 || !strings.HasPrefix(evs[0].Text, "forget confirmed") {
		t.Errorf("the forget was not confirmed once: %+v", evs)
	}

	// 401: the central server no longer knows the token
	h.join("Zoë", code)
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	c.st.ClearGame(g.ID, time.Now())
	c.st.Leave(protocol.Hash(h.n.st.Membership.Token), protocol.LeaveCommand, time.Now(), "") // confirm the forget, so only 401 is left
	if err := h.report(); err == nil {
		t.Fatal("no error for a token the server does not know")
	}
	if h.n.st.Membership.Status != StatusRejected || len(h.n.st.Solved) != 1 {
		t.Errorf("after 401: %s", h.n.st.Membership.Status)
	}
	if err := h.report(); err != errNotJoined {
		t.Errorf("reporting goes on after a 401: %v", err)
	}
	if !strings.Contains(h.req("GET", "/ctf/", nil).Body.String(), "Join again") {
		t.Error("the page does not show the join form after a 401")
	}
}

func assertForgotten(t *testing.T, h *harness) {
	t.Helper()
	counts, err := h.db.RowCounts()
	if err != nil {
		t.Fatal(err)
	}
	for table, n := range counts {
		if n != 0 {
			t.Errorf("table %s has %d rows after Forget", table, n)
		}
	}
	if h.n.st.Membership != nil || len(h.n.st.Solved) != 0 || len(h.n.st.Coding) != 0 {
		t.Error("the state in memory was not wiped")
	}
	if h.shop.nSolved() != 0 {
		t.Error("the shop still has solves after Forget")
	}
}

func TestCentralLessSession(t *testing.T) {
	ft := &failingTransport{}
	h := newHarness(t, Config{})
	h.n.SetTransport(ft)
	h.poll()
	if w := h.submit("/ctf/join", url.Values{"name": {"Zoë"}}); w.Code != 200 || h.n.st.Membership.Status != StatusLocal {
		t.Fatalf("local join: %d", w.Code)
	}
	h.shop.solve("scoreBoardChallenge", "loginAdminChallenge")
	h.poll()
	page := h.req("GET", "/ctf/", nil).Body.String()
	if !strings.Contains(page, `<span class="num">350</span>`) {
		t.Error("the page does not show the local score by the scoring table")
	}
	h.submit("/ctf/restart", url.Values{})
	h.poll()
	h.poll()
	h.submit("/ctf/forget", url.Values{"confirm": {"forget"}})
	if err := h.report(); err != errNotJoined {
		t.Errorf("report: %v", err)
	}
	h.n.TryOutbox(context.Background())
	if n := ft.calls.Load(); n != 0 {
		t.Errorf("%d outbound requests in a central-less session", n)
	}
}

func TestAnotherCentralServer(t *testing.T) {
	c := newCentral(t)
	_, code := c.game(t, "Elsewhere")
	h := newHarness(t, Config{DefaultCentralURL: "https://central.invalid/juiceshop-ctf"})
	h.poll()
	if w := h.submit("/ctf/central", url.Values{"url": {"ftp://x"}, "action": {"set"}}); w.Code != 400 {
		t.Errorf("a bad address: %d", w.Code)
	}
	if w := h.submit("/ctf/central", url.Values{"url": {c.URL}, "action": {"set"}}); w.Code != 200 {
		t.Fatalf("set: %d", w.Code)
	}
	if r := h.join("Zoë", code); r.Outcome != JoinedOnline || h.n.st.Membership.CentralURL != c.URL {
		t.Fatalf("join on the other server: %v", r.Outcome)
	}
	if w := h.submit("/ctf/central", url.Values{"action": {"default"}}); w.Code != 409 {
		t.Errorf("changing the server while joined: %d", w.Code)
	}
	h.submit("/ctf/forget", url.Values{"confirm": {"forget"}})
	if w := h.submit("/ctf/central", url.Values{"action": {"default"}}); w.Code != 200 || h.n.CentralURL() != "https://central.invalid/juiceshop-ctf" {
		t.Errorf("back to the default after Forget: %d %s", w.Code, h.n.CentralURL())
	}
	h.submit("/ctf/central", url.Values{"action": {"none"}})
	if h.n.CentralURL() != "" || strings.Contains(h.req("GET", "/ctf/", nil).Body.String(), `name="code"`) {
		t.Error("no central server: the join form still asks for a code")
	}
}

// TestAsksForCentral: an image without a default asks for the central server
// before the join form, until an address or "Play on this VM only" is chosen.
func TestAsksForCentral(t *testing.T) {
	c := newCentral(t)
	_, code := c.game(t, "Asked")
	h := newHarness(t, Config{})
	h.poll()
	page := func() string { return h.req("GET", "/ctf/", nil).Body.String() }
	if p := page(); !strings.Contains(p, "Which central server?") || strings.Contains(p, `action="/ctf/join"`) {
		t.Fatal("no default, no choice: the page does not ask for the central server first")
	}
	if w := h.submit("/ctf/central", url.Values{"url": {c.URL}, "action": {"set"}}); w.Code != 200 || !strings.Contains(w.Body.String(), `name="code"`) {
		t.Fatalf("after setting the address the join form asks for a code: %d", w.Code)
	}
	if r := h.join("Zoë", code); r.Outcome != JoinedOnline {
		t.Fatalf("join: %v", r.Outcome)
	}
	h.submit("/ctf/forget", url.Values{"confirm": {"forget"}})
	h.submit("/ctf/central", url.Values{"action": {"none"}})
	if p := page(); strings.Contains(p, "Which central server?") || !strings.Contains(p, `action="/ctf/join"`) || strings.Contains(p, `name="code"`) {
		t.Error("after Play on this VM only: a join form without a code, and no question")
	}

	d := newHarness(t, Config{DefaultCentralURL: c.URL})
	d.poll()
	if p := d.req("GET", "/ctf/", nil).Body.String(); strings.Contains(p, "Which central server?") || !strings.Contains(p, `name="code"`) {
		t.Error("an image with a default asks for the central server")
	}
}

// ---- the page ----------------------------------------------------------------

func TestPageWithoutCookiesIsTheSame(t *testing.T) {
	c := newCentral(t)
	_, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	h.join("Zoë", code)
	strip := func(s string) string { return csrfRE.ReplaceAllString(s, "") }
	with := strip(h.req("GET", "/ctf/", nil).Body.String())
	h.jar = map[string]string{}
	without := strip(h.req("GET", "/ctf/", nil).Body.String())
	if with != without || !strings.Contains(with, "Zoë") {
		t.Error("the page differs without cookies")
	}
	h.jar = map[string]string{}
	if w := h.req("GET", "/juiceshop/rest/products", nil); w.Code != 200 || w.Body.String() != "juice shop: /juiceshop/rest/products" {
		t.Errorf("the shop without cookies: %d %q", w.Code, w.Body.String())
	}
	if w := h.req("POST", "/ctf/forget", url.Values{"confirm": {"forget"}}); w.Code != 403 || h.n.st.Membership == nil {
		t.Error("Forget without a CSRF token was accepted")
	}
}

func TestRestartButtonRestores(t *testing.T) {
	h := newHarness(t, Config{})
	h.poll()
	h.join("Zoë", "")
	h.shop.solve("scoreBoardChallenge", "loginAdminChallenge")
	h.poll()
	if w := h.submit("/ctf/restart", url.Values{}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if h.keeper.restarts.Load() != 1 {
		t.Fatal("the keeper was not asked")
	}
	deadline := time.Now().Add(10 * time.Second)
	for h.shop.nSolved() != 2 && time.Now().Before(deadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if h.shop.nSolved() != 2 {
		t.Error("the solves are not back in the shop after the restart")
	}
}

func TestForget(t *testing.T) {
	c := newCentral(t)
	g, code := c.game(t, "Lab")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	h.join("Zoë", code)
	h.shop.solve("scoreBoardChallenge")
	h.poll()
	h.report()
	if w := h.submit("/ctf/forget", url.Values{"confirm": {"nope"}}); w.Code != 400 || h.n.st.Membership == nil {
		t.Fatal("Forget without typing forget")
	}
	if w := h.submit("/ctf/forget", url.Values{"confirm": {"Forget"}}); w.Code != 200 {
		t.Fatal(w.Code)
	}
	assertForgotten(t, h)
	if ps := c.participants(t, g); len(ps) != 1 || ps[0].Status() != "left" || ps[0].Solved != 1 {
		t.Errorf("central participant %+v", ps[0])
	}

	// with the central server down, Forget still empties the VM
	h.join("Bram", code)
	h.shop.solve("loginAdminChallenge")
	h.poll()
	c.down.Store(true)
	h.submit("/ctf/forget", url.Values{"confirm": {"forget"}})
	assertForgotten(t, h)
}

func TestGate(t *testing.T) {
	h := newHarness(t, Config{Gate: true})
	h.poll()
	if w := h.req("GET", "/juiceshop/", nil); w.Code != 302 {
		t.Errorf("the gated shop before joining: %d", w.Code)
	}
	w := h.submit("/ctf/join", url.Values{"name": {"Zoë de Vries"}})
	m := regexpFind(`<p class="joincode">([a-z0-9]+)</p>`, w.Body.String())
	if m == "" {
		t.Fatal("no return code shown")
	}
	if w := h.req("GET", "/juiceshop/", nil); w.Code != 200 {
		t.Errorf("the shop with the session: %d", w.Code)
	}
	h.jar = map[string]string{} // every cookie deleted
	if w := h.req("GET", "/juiceshop/", nil); w.Code != 302 {
		t.Error("the shop opened without a session")
	}
	if !strings.Contains(h.req("GET", "/ctf/", nil).Body.String(), "Welcome back") {
		t.Error("no re-entry form")
	}
	for i := 1; i <= 6; i++ {
		w := h.submit("/ctf/enter", url.Values{"name": {"Zoë de Vries"}, "code": {"aaaaaaaa"}})
		want := 403
		if i == 6 {
			want = 429
		}
		if w.Code != want {
			t.Errorf("wrong code %d: %d, want %d", i, w.Code, want)
		}
	}
	h.db.db.Exec(`DELETE FROM gate_failures`)
	if w := h.submit("/ctf/enter", url.Values{"name": {"zoë de vries"}, "code": {strings.ToUpper(m)}}); w.Code != 303 {
		t.Fatalf("re-entry with name and return code: %d", w.Code)
	}
	if w := h.req("GET", "/juiceshop/", nil); w.Code != 200 {
		t.Error("back in, but the shop is closed")
	}

	off := newHarness(t, Config{})
	if w := off.submit("/ctf/enter", url.Values{"name": {"x"}, "code": {"y"}}); w.Code != 404 {
		t.Errorf("/ctf/enter without the gate: %d", w.Code)
	}
	off.submit("/ctf/join", url.Values{"name": {"Zoë"}})
	off.jar = map[string]string{}
	if strings.Contains(off.req("GET", "/ctf/", nil).Body.String(), "return code") || off.req("GET", "/juiceshop/", nil).Code != 200 {
		t.Error("something of the gate is active without CTF_GATE")
	}
}

// ---- the page and the shop are two origins ----

func TestThePageIsNotServedOnTheShopsPort(t *testing.T) {
	h := newHarness(t, Config{})
	shop, page := h.srv.Shop(), h.srv.Page()
	do := func(hd http.Handler, method, target string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, target, nil)
		w := httptest.NewRecorder()
		hd.ServeHTTP(w, r)
		return w
	}
	// On the shop's port, the page is only ever a redirect to its own port.
	for _, p := range []string{"/", "/ctf/", "/ctf/static/style.css"} {
		if w := do(shop, "GET", "http://192.168.56.101"+p); w.Code != 302 || w.Header().Get("Location") != "http://192.168.56.101:8080/ctf/" {
			t.Errorf("GET %s on the shop's port: %d %q", p, w.Code, w.Header().Get("Location"))
		}
	}
	for _, p := range []string{"/ctf/forget", "/ctf/join", "/ctf/central"} {
		if w := do(shop, "POST", "http://192.168.56.101"+p); w.Code != 404 {
			t.Errorf("POST %s on the shop's port: %d", p, w.Code)
		}
	}
	if w := do(shop, "GET", "http://192.168.56.101/juiceshop/rest/products"); w.Body.String() != "juice shop: /juiceshop/rest/products" {
		t.Errorf("the shop: %q", w.Body.String())
	}
	// The page sends the shop to the shop's port, and links there.
	if w := do(page, "GET", "http://192.168.56.101:8080/juiceshop/"); w.Code != 302 || w.Header().Get("Location") != "http://192.168.56.101/juiceshop/" {
		t.Errorf("the shop on the page's port: %d %q", w.Code, w.Header().Get("Location"))
	}
	if b := do(page, "GET", "http://192.168.56.101:8080/ctf/").Body.String(); !strings.Contains(b, `href="http://192.168.56.101/juiceshop/"`) {
		t.Error("the page does not link to the shop on its own port")
	}
}

// ---- a form from another origin does nothing ----

func TestFormsFromAnotherOriginAreRefused(t *testing.T) {
	c := newCentral(t)
	_, code := c.game(t, "Sabotage")
	h := newHarness(t, Config{DefaultCentralURL: c.URL})
	h.poll()
	h.join("Anna", code)
	m := csrfRE.FindStringSubmatch(h.req("GET", "/ctf/", nil).Body.String())
	post := func(hdr ...string) int {
		f := url.Values{"csrf": {m[1]}, "confirm": {"forget"}}
		r := httptest.NewRequest("POST", "http://vm:8080/ctf/forget", strings.NewReader(f.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for i := 0; i+1 < len(hdr); i += 2 {
			r.Header.Set(hdr[i], hdr[i+1])
		}
		w := httptest.NewRecorder()
		h.h.ServeHTTP(w, r)
		return w.Code
	}
	// Script in the shop (port 80) posting to the page (port 8080): the
	// browser says same-site, or at least sends the shop's Origin.
	if code := post("Sec-Fetch-Site", "same-site", "Origin", "http://vm"); code != 403 || h.n.st.Membership == nil {
		t.Errorf("Forget from the shop's origin: %d", code)
	}
	if code := post("Origin", "http://vm"); code != 403 || h.n.st.Membership == nil {
		t.Errorf("Forget with the shop's Origin, no Sec-Fetch-Site: %d", code)
	}
	// A token nobody made is refused, whatever cookie comes with it: a
	// cookie is no part of the token any more.
	f := url.Values{"csrf": {strings.Repeat("0", 80)}, "confirm": {"forget"}}
	r := httptest.NewRequest("POST", "http://vm:8080/ctf/forget", strings.NewReader(f.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: "ctf_node_csrf", Value: strings.Repeat("0", 80)})
	w := httptest.NewRecorder()
	h.h.ServeHTTP(w, r)
	if w.Code != 403 || h.n.st.Membership == nil {
		t.Errorf("a made-up token: %d", w.Code)
	}
	// The page's own form goes through.
	if code := post("Sec-Fetch-Site", "same-origin", "Origin", "http://vm:8080"); code != 200 || h.n.st.Membership != nil {
		t.Errorf("Forget from the page itself: %d", code)
	}
}

// ---- plain HTTP only to a central server that is not on the internet ----

func TestCentralURLOverPlainHTTP(t *testing.T) {
	for raw, ok := range map[string]bool{
		"https://ctf.example.org":             true,
		"https://203.0.113.7/juiceshop-ctf":   true,
		"http://ctf.example.org":              false,
		"http://203.0.113.7:8080":             false,
		"http://localhost:8080":               true,
		"http://127.0.0.1:8080/juiceshop-ctf": true,
		"http://192.168.1.20":                 true,
		"http://[fd00::1]:8080":               true,
		"http://ctf-central:8080":             true,
		"ftp://ctf.example.org":               false,
	} {
		if _, err := ValidateCentralURL(raw); (err == nil) != ok {
			t.Errorf("%s: %v", raw, err)
		}
	}
	for raw, warn := range map[string]bool{
		"https://ctf.example.org": false, "http://localhost:8080": false, "http://127.0.0.1": false,
		"http://192.168.1.20": true, "http://ctf-central:8080": true,
	} {
		if PlainOnSharedNetwork(raw) != warn {
			t.Errorf("warning for %s: %v", raw, !warn)
		}
	}
}

// ---- a cheat score of 0 posted first does not hide a real one ----

func TestWebhookKeepsTheHighestCheatScore(t *testing.T) {
	h := newHarness(t, Config{})
	h.join("Sanne", "")
	srv := httptest.NewServer(h.n.WebhookHandler())
	defer srv.Close()
	for _, c := range []string{"0", "0.8", "0.2"} {
		http.Post(srv.URL+"/webhook", "application/json", strings.NewReader(`{"solution":{"challenge":"loginAdminChallenge","cheatScore":`+c+`}}`))
	}
	h.n.mu.Lock()
	got := h.n.st.Cheat["loginAdminChallenge"]
	h.n.mu.Unlock()
	if got != 0.8 {
		t.Errorf("cheat score %v after 0, 0.8, 0.2; want 0.8", got)
	}
}
