package central

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/central/store"
	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
	"github.com/macsnoeren/juiceshop-ctf/web/static"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata/")

const base = "/juiceshop-ctf"

var t0 = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

type env struct {
	t   *testing.T
	st  *store.Store
	srv *Server
	h   http.Handler
	now time.Time
	jar map[string]string // admin cookies
}

func newEnv(t *testing.T) *env {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SaveSettings(store.Settings{Title: "Juice Shop CTF", Timezone: "UTC", RetentionDays: 30})
	srv, err := New(Config{BasePath: base}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	e := &env{t: t, st: st, srv: srv, now: t0, jar: map[string]string{}}
	srv.SetClock(func() time.Time { return e.now })
	e.h = srv.Handler()
	return e
}

func (e *env) req(method, path string, body io.Reader, hdr ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, body)
	r.RemoteAddr = "192.0.2.7:4000"
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	for k, v := range e.jar {
		r.AddCookie(&http.Cookie{Name: k, Value: v})
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, r)
	for _, c := range w.Result().Cookies() {
		e.jar[c.Name] = c.Value
	}
	return w
}

func (e *env) get(path string) *httptest.ResponseRecorder { return e.req("GET", path, nil) }

func (e *env) json(path string, v any, hdr ...string) *httptest.ResponseRecorder {
	b, _ := json.Marshal(v)
	return e.req("POST", path, bytes.NewReader(b), append([]string{"Content-Type", "application/json"}, hdr...)...)
}

var csrfRE = regexp.MustCompile(`name="csrf" value="([0-9a-f]+)"`)

// post submits an admin form with the CSRF token of the admin page.
func (e *env) post(path string, form url.Values) *httptest.ResponseRecorder {
	e.t.Helper()
	m := csrfRE.FindStringSubmatch(e.get(base + "/admin/").Body.String())
	if m == nil {
		e.t.Fatal("no CSRF token on the admin page")
	}
	if form == nil {
		form = url.Values{}
	}
	form.Set("csrf", m[1])
	return e.req("POST", path, strings.NewReader(form.Encode()), "Content-Type", "application/x-www-form-urlencoded")
}

var codeRE = regexp.MustCompile(`<p class="joincode">([a-z0-9-]+)</p>`)

// game creates a game through admin and returns its id and join code.
func (e *env) game(name string, form url.Values, state string) (int64, string) {
	e.t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("name", name)
	w := e.post(base+"/admin/games", form)
	m := codeRE.FindStringSubmatch(w.Body.String())
	if w.Code != 200 || m == nil {
		e.t.Fatalf("create game: %d %s", w.Code, w.Body.String())
	}
	gs, _ := e.st.Games(e.now)
	id := gs[0].ID
	if state != "" {
		e.post(fmt.Sprintf("%s/admin/games/%d/state", base, id), url.Values{"to": {state}, "confirm": {name}})
	}
	return id, m[1]
}

func (e *env) join(code, name string) (int, protocol.JoinResponse) {
	var jr protocol.JoinResponse
	w := e.json(base+"/api/v2/join", protocol.JoinRequest{Protocol: 2, Name: name, Code: code,
		Node: protocol.Node{UID: protocol.NewNodeUID(), Version: "v1.1.0-test", JuiceShop: "20.3.0"}})
	json.Unmarshal(w.Body.Bytes(), &jr)
	return w.Code, jr
}

func (e *env) report(token string, solves map[string]time.Time, applied ...string) (int, protocol.ReportResponse) {
	r := protocol.Report{Protocol: 2, SentAt: e.now, Boot: "boot-1", Reachable: true, CommandsApplied: applied,
		Node: protocol.Node{UID: strings.Repeat("b", 32), Version: "v1.1.0-test", JuiceShop: "20.3.0"}}
	for k, t := range solves {
		r.Solves = append(r.Solves, protocol.Solve{Key: k, SolvedAt: t})
	}
	var out protocol.ReportResponse
	w := e.json(base+"/api/v2/report", r, "Authorization", "Bearer "+token)
	json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

func (e *env) events(kind string) []store.Event {
	evs, _ := e.st.Events(store.EventFilter{Kind: kind})
	return evs
}

// ---- T1.1-14, -15, -19: the API ------------------------------------------

func TestJoinAnswers(t *testing.T) {
	e := newEnv(t)
	_, code := e.game("Open", nil, "open")
	_, closedCode := e.game("Ended", nil, "closed")
	status, jr := e.join(code, "Zoë")
	if status != 201 || !protocol.ValidParticipantToken(jr.Token) || jr.Game.State != "open" || !strings.HasSuffix(jr.Board, base+"/g/open/") {
		t.Fatalf("join: %d %+v", status, jr)
	}
	if s, _ := e.join("23456789ab", "Zoë"); s != 403 {
		t.Errorf("unknown code: %d", s)
	}
	if s, _ := e.join(closedCode, "Zoë"); s != 409 {
		t.Errorf("closed game: %d", s)
	}
	if s, _ := e.join(code, "<script>"); s != 400 {
		t.Errorf("invalid name: %d", s)
	}
	if s, _ := e.report(jr.Token, map[string]time.Time{"scoreBoardChallenge": t0}); s != 200 {
		t.Errorf("the token does not work for a report: %d", s)
	}
	if w := e.json(base+"/api/v2/report", map[string]int{"protocol": 2}, "Authorization", "Bearer ctfp_nonsense"); w.Code != 401 {
		t.Errorf("a malformed token: %d", w.Code)
	}
	if len(e.events(store.EvJoin)) != 1 || len(e.events(store.EvJoinRefused)) != 3 || len(e.events(store.EvRejected)) != 1 {
		t.Errorf("events: %d joins, %d refused, %d rejected", len(e.events(store.EvJoin)), len(e.events(store.EvJoinRefused)), len(e.events(store.EvRejected)))
	}
}

func TestJoinLimits(t *testing.T) {
	e := newEnv(t)
	_, code := e.game("Hall", nil, "running")
	for i := range 30 {
		if s, _ := e.join(code, fmt.Sprintf("Student %d", i)); s != 201 {
			t.Fatalf("join %d from one address within a minute: %d", i+1, s)
		}
	}
	for i := 1; i <= 21; i++ {
		s, _ := e.join("23456789ab", "Guess")
		want := 403
		if i == 21 {
			want = 429
		}
		if s != want {
			t.Errorf("wrong code %d: %d, want %d", i, s, want)
		}
	}
	e.now = e.now.Add(time.Minute)
	if s, _ := e.join(code, "After a minute"); s != 201 {
		t.Errorf("after a minute the address may join again: %d", s)
	}
	for i := len(e.srv.codeJoins.m); i < 0; i++ {
	}
	e.srv.codeJoins.m[protocol.Hash(strings.ReplaceAll(code, "-", ""))].n = JoinsPerGamePerMin
	if s, _ := e.join(code, "Number 301"); s != 429 {
		t.Errorf("the 301st join of a game in a minute: %d", s)
	}
}

func TestLegacyReportIsGone(t *testing.T) {
	e := newEnv(t)
	v1 := `{"protocol":1,"instance":"cybersecurity-1","sent_at":"2026-11-18T10:00:00Z","agent_version":"0.1.0","juiceshop":{"version":"20.3.0","reachable":true},"registration":null,"challenges":[]}`
	w := e.req("POST", base+"/api/v1/report", strings.NewReader(v1), "Authorization", "Bearer ctf_abc", "X-Real-IP", "198.51.100.9")
	if w.Code != 410 {
		t.Fatalf("1.0 report: %d", w.Code)
	}
	evs := e.events(store.EvLegacy)
	if len(evs) != 1 || evs[0].Address != "198.51.100.9" || !strings.Contains(evs[0].Text, "cybersecurity-1") {
		t.Errorf("legacy events %+v", evs)
	}
}

// ---- T1.1-24..27: admin ----------------------------------------------------

func TestJoinCodeIsShownOnce(t *testing.T) {
	e := newEnv(t)
	id, code := e.game("Class", nil, "")
	page := e.get(fmt.Sprintf("%s/admin/games/%d", base, id)).Body.String()
	if strings.Contains(page, code) || strings.Contains(page, strings.ReplaceAll(code, "-", "")) {
		t.Error("the join code is shown again after leaving the page")
	}
	w := e.post(fmt.Sprintf("%s/admin/games/%d/code", base, id), nil)
	m := codeRE.FindStringSubmatch(w.Body.String())
	if m == nil || m[1] == code {
		t.Fatal("regenerating showed no new code")
	}
	e.post(fmt.Sprintf("%s/admin/games/%d/state", base, id), url.Values{"to": {"open"}})
	if s, _ := e.join(code, "Old code"); s != 403 {
		t.Errorf("the old code still admits: %d", s)
	}
	if s, _ := e.join(m[1], "New code"); s != 201 {
		t.Errorf("the new code does not admit: %d", s)
	}
	// every form carries a CSRF token, and a POST without one is refused
	for _, p := range []string{"/admin/", fmt.Sprintf("/admin/games/%d", id)} {
		body := e.get(base + p).Body.String()
		if forms, tokens := strings.Count(body, `method="post"`), strings.Count(body, `name="csrf"`); forms == 0 || forms != tokens {
			t.Errorf("%s: %d forms, %d CSRF tokens", p, forms, tokens)
		}
	}
	w = e.req("POST", fmt.Sprintf("%s/admin/games/%d/clear", base, id), strings.NewReader("confirm=Class"), "Content-Type", "application/x-www-form-urlencoded")
	if w.Code != 403 {
		t.Errorf("a POST without the CSRF token: %d", w.Code)
	}
}

func TestGamePageShowsEveryState(t *testing.T) {
	e := newEnv(t)
	id, code := e.game("Five", nil, "running")
	var tokens []string
	var ids []int64
	for _, n := range []string{"Active One", "Active Two", "Hidden Three", "Left Four", "Removed Five"} {
		_, jr := e.join(code, n)
		tokens = append(tokens, jr.Token)
		ids = append(ids, jr.Participant)
		e.report(jr.Token, map[string]time.Time{"scoreBoardChallenge": t0})
	}
	e.post(fmt.Sprintf("%s/admin/participants/%d/hide", base, ids[2]), nil)
	e.json(base+"/api/v2/leave", protocol.LeaveRequest{Protocol: 2, Reason: "forget"}, "Authorization", "Bearer "+tokens[3])
	e.post(fmt.Sprintf("%s/admin/participants/%d/remove", base, ids[4]), url.Values{"confirm": {"Five"}})
	page := e.get(fmt.Sprintf("%s/admin/games/%d", base, id)).Body.String()
	for _, want := range []string{
		`Active One`, `<span class="badge st-active">active</span>`, `<span class="badge st-hidden">hidden</span>`,
		`<span class="badge st-left">left</span>`, `<span class="badge st-removed">removed</span>`, `v1.1.0-test`,
	} {
		if !strings.Contains(page, want) {
			t.Errorf("game page lacks %q", want)
		}
	}
	if strings.Count(page, `class="badge st-active"`) != 2 {
		t.Error("want two active participants")
	}
	b := e.get(base + "/g/five/board.json").Body.String()
	for _, n := range []string{"Hidden Three", "Left Four", "Removed Five"} {
		if strings.Contains(b, n) {
			t.Errorf("%s is on the board", n)
		}
	}
}

func TestCleaningUp(t *testing.T) {
	e := newEnv(t)
	a, acode := e.game("Game A", nil, "running")
	b, bcode := e.game("Game B", nil, "running")
	_, ja := e.join(acode, "Anna")
	_, jb := e.join(bcode, "Bram")
	e.report(ja.Token, map[string]time.Time{"scoreBoardChallenge": t0})
	e.report(jb.Token, map[string]time.Time{"scoreBoardChallenge": t0})
	before := len(e.events(store.EvAdmin))

	w := e.post(fmt.Sprintf("%s/admin/games/%d/clear", base, a), url.Values{"confirm": {"Game B"}})
	if w.Code != 400 || len(e.events(store.EvAdmin)) != before {
		t.Fatalf("clear with the wrong name: %d", w.Code)
	}
	if s, _ := e.report(ja.Token, nil); s != 200 {
		t.Fatal("a refused clear changed something")
	}
	steps := []struct {
		path string
		form url.Values
	}{
		{fmt.Sprintf("%s/admin/participants/%d/hide", base, jb.Participant), nil},
		{fmt.Sprintf("%s/admin/participants/%d/show", base, jb.Participant), nil},
		{fmt.Sprintf("%s/admin/games/%d/clear", base, a), url.Values{"confirm": {"Game A"}}},
		{fmt.Sprintf("%s/admin/games/%d/state", base, b), url.Values{"to": {"closed"}, "confirm": {"Game B"}}},
		{fmt.Sprintf("%s/admin/games/%d/delete", base, a), url.Values{"confirm": {" Game A "}}},
	}
	for i, s := range steps {
		w := e.post(s.path, s.form)
		if w.Code != 200 && w.Code != 303 {
			t.Fatalf("step %d: %d %s", i, w.Code, w.Body.String())
		}
		if n := len(e.events(store.EvAdmin)); n != before+i+1 {
			t.Errorf("step %d left %d admin events, want exactly one", i, n-before-i)
		}
	}
	if _, resp := e.report(ja.Token, nil); len(resp.Commands) != 1 || resp.Commands[0].Type != "forget" {
		t.Errorf("Anna's VM was not told forget: %+v", resp)
	}
	if s, resp := e.report(jb.Token, nil); s != 200 || resp.Game.State != "closed" || resp.Standing == nil || resp.Standing.Score != 100 {
		t.Errorf("game B was touched: %d %+v", s, resp)
	}
}

func TestEventFeed(t *testing.T) {
	e := newEnv(t)
	a, acode := e.game("Feed A", nil, "running")
	_, bcode := e.game("Feed B", nil, "running")
	_, ja := e.join(acode, "-Anna")
	e.join(bcode, "Bram")
	e.join("23456789ab", "Guess")
	e.report(ja.Token, map[string]time.Time{"scoreBoardChallenge": t0})
	rows := func(q string) int {
		return strings.Count(e.get(base+"/admin/events"+q).Body.String(), `<tr class="ev-`)
	}
	all := rows("")
	if all < 6 {
		t.Fatalf("feed has %d rows", all)
	}
	if n := rows(fmt.Sprintf("?game=%d", a)); n == 0 || n >= all {
		t.Errorf("game filter: %d of %d", n, all)
	}
	if n := rows("?kind=join-refused"); n != 1 {
		t.Errorf("kind filter: %d", n)
	}
	if n := rows(fmt.Sprintf("?participant=%d", ja.Participant)); n < 3 {
		t.Errorf("participant filter: %d", n)
	}
	r := csv.NewReader(strings.NewReader(e.get(base + "/admin/events.csv?kind=join").Body.String()))
	recs, err := r.ReadAll()
	if err != nil || len(recs) != 3 {
		t.Fatalf("csv: %v %d", err, len(recs))
	}
	for _, rec := range recs[1:] {
		for _, cell := range rec {
			if cell != "" && strings.ContainsRune("=+-@", rune(cell[0])) {
				t.Errorf("CSV cell %q could be read as a formula", cell)
			}
		}
	}
	page := e.get(base + "/admin/events").Body.String()
	if strings.Contains(page, ja.Token) || strings.Contains(page, acode) || strings.Contains(page, strings.ReplaceAll(acode, "-", "")) {
		t.Error("the feed shows a token or a join code")
	}
}

// ---- T1.1-44..51: the boards -------------------------------------------------

// seed builds the golden game: four participants with solves at fixed times,
// one of them hidden.
func seed(t *testing.T, e *env, form url.Values) (int64, []protocol.JoinResponse) {
	t.Helper()
	if form == nil {
		form = url.Values{}
	}
	form.Set("starts_at", "2026-11-19T09:00")
	form.Set("ends_at", "2026-11-19T11:00")
	id, code := e.game("Golden Game", form, "running")
	var js []protocol.JoinResponse
	for i, n := range []string{"Zoë de Vries", "Bram O'Neill", "Chen Li", "Hidden Henk"} {
		e.now = t0.Add(time.Duration(i+1) * time.Minute)
		_, jr := e.join(code, n)
		js = append(js, jr)
	}
	m := func(i int) time.Time { return t0.Add(time.Duration(i) * time.Minute) }
	plan := []struct {
		who    int
		at     int
		solves map[string]int
	}{
		{0, 10, map[string]int{"scoreBoardChallenge": 9}},
		{1, 12, map[string]int{"scoreBoardChallenge": 11, "loginAdminChallenge": 12}},
		{2, 15, map[string]int{"directoryListingChallenge": 14}},
		{0, 20, map[string]int{"scoreBoardChallenge": 9, "nullByteChallenge": 19, "easterEggLevelOneChallenge": 19}},
		{3, 21, map[string]int{"scoreBoardChallenge": 1}},
		{2, 30, map[string]int{"directoryListingChallenge": 14, "loginAdminChallenge": 29}},
	}
	for _, p := range plan {
		e.now = m(p.at)
		sv := map[string]time.Time{}
		for k, at := range p.solves {
			sv[k] = m(at)
		}
		e.report(js[p.who].Token, sv)
	}
	e.post(fmt.Sprintf("%s/admin/participants/%d/hide", base, js[3].Participant), nil)
	e.now = m(40)
	return id, js
}

func golden(t *testing.T, name string, got []byte) {
	t.Helper()
	path := filepath.Join("testdata", name)
	if *update {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test -run %s -update once)", err, t.Name())
	}
	if !bytes.Equal(got, want) {
		t.Errorf("%s differs from the golden file (go test -update to accept):\n%s", name, got)
	}
}

func TestBoardJSONGolden(t *testing.T) {
	e := newEnv(t)
	seed(t, e, nil)
	w := e.get(base + "/g/golden-game/board.json")
	if w.Code != 200 {
		t.Fatal(w.Code)
	}
	var v any
	json.Unmarshal(w.Body.Bytes(), &v)
	pretty, _ := json.MarshalIndent(v, "", "  ")
	golden(t, "board.golden.json", append(pretty, '\n'))
	var b store.Board
	json.Unmarshal(w.Body.Bytes(), &b)
	if len(b.Rows) != 3 || b.Rows[0].Name != "Zoë de Vries" || len(b.Podium) != 3 || len(b.Series) != 3 {
		t.Errorf("board %+v", b.Rows)
	}
}

func TestChartGolden(t *testing.T) {
	e := newEnv(t)
	seed(t, e, nil)
	w := e.get(base + "/g/golden-game/chart.svg")
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "image/svg+xml") || strings.Contains(w.Header().Get("Content-Security-Policy"), "script") {
		t.Fatalf("chart: %d %v", w.Code, w.Header())
	}
	golden(t, "chart.golden.svg", w.Body.Bytes())
	if n := strings.Count(w.Body.String(), "<polyline"); n != 3 {
		t.Errorf("%d lines, want 3", n)
	}
	if !strings.Contains(w.Body.String(), "prefers-color-scheme: dark") {
		t.Error("the chart has no dark colours")
	}
}

func TestBoardPage(t *testing.T) {
	e := newEnv(t)
	_, code := e.game("Escape", nil, "running")
	_, jr := e.join(code, "Bram O'Neill")
	e.report(jr.Token, map[string]time.Time{"scoreBoardChallenge": t0})
	body := e.get(base + "/g/escape/").Body.String()
	if !strings.Contains(body, "Bram O&#39;Neill") || strings.Contains(body, "Bram O'Neill") {
		t.Error("a name is not escaped on the board")
	}
	for _, want := range []string{`id="podium"`, `id="rows"`, `id="ticker"`, `class="catbar"`, `chart.svg`, `class="badge blood">first blood`} {
		if !strings.Contains(body, want) {
			t.Errorf("board lacks %s", want)
		}
	}
	css, _ := static.FS.ReadFile("style.css")
	js, _ := static.FS.ReadFile("board.js")
	if !strings.Contains(string(css), "@media (prefers-reduced-motion: reduce)") || !strings.Contains(string(js), "prefers-reduced-motion: reduce") {
		t.Error("reduced motion is not honoured by both the stylesheet and the script")
	}
	if regexp.MustCompile(`\.innerHTML|insertAdjacentHTML|document\.write`).Match(js) {
		t.Error("board.js uses innerHTML")
	}
}

// crawl follows every link under the game from the board and returns the
// bodies; any page that is not 200 fails the test.
func crawl(t *testing.T, e *env, start string) map[string]string {
	t.Helper()
	hrefRE := regexp.MustCompile(`(?:href|src)="([^"]+)"`)
	seen := map[string]string{}
	todo := []string{start}
	for len(todo) > 0 && len(seen) < 500 {
		p := todo[0]
		todo = todo[1:]
		if _, ok := seen[p]; ok {
			continue
		}
		w := e.get(p)
		if w.Code != 200 {
			t.Errorf("%s: %d", p, w.Code)
		}
		seen[p] = w.Body.String()
		for _, m := range hrefRE.FindAllStringSubmatch(w.Body.String(), -1) {
			u := strings.ReplaceAll(m[1], "&amp;", "&")
			if strings.HasPrefix(u, start) || strings.HasPrefix(u, base+"/static/") {
				todo = append(todo, u)
			}
		}
	}
	return seen
}

func TestEveryLinkResolves(t *testing.T) {
	e := newEnv(t)
	seed(t, e, nil)
	pages := crawl(t, e, base+"/g/golden-game/")
	var parts, chals, cats int
	for p := range pages {
		switch {
		case strings.Contains(p, "/p/"):
			parts++
		case strings.Contains(p, "/c/"):
			chals++
		case strings.Contains(p, "/cat/"):
			cats++
		}
	}
	if parts != 3 || chals < 50 || cats != 14 {
		t.Errorf("crawled %d participant, %d challenge, %d category pages", parts, chals, cats)
	}
	if !strings.Contains(e.get(base+"/g/golden-game/c/xssBonusChallenge").Body.String(), "Nobody has solved this challenge yet.") {
		t.Error("an unsolved challenge does not say so")
	}
	if e.get(base+"/g/golden-game/c/xxeFileDisclosureChallenge").Code != 404 {
		t.Error("a challenge disabled in Docker has a page")
	}
	pp := e.get(fmt.Sprintf(base+"/g/golden-game/p/%d", 1)).Body.String()
	if !strings.Contains(pp, "first blood") || !strings.Contains(pp, "By difficulty") {
		t.Error("the participant page lacks first bloods or the difficulty spread")
	}
}

func TestBeamerStates(t *testing.T) {
	e := newEnv(t)
	id, _ := seed(t, e, nil)
	running := e.get(base + "/g/golden-game/beamer").Body.String()
	if !strings.Contains(running, `id="countdown" data-ends-at="2026-11-19T11:00:00Z">1:20:00</p>`) || !strings.Contains(running, `class="ticker strip"`) {
		t.Errorf("the running beamer lacks the countdown or the ticker")
	}
	e.post(fmt.Sprintf("%s/admin/games/%d/state", base, id), url.Values{"to": {"closed"}, "confirm": {"Golden Game"}})
	ended := e.get(base + "/g/golden-game/beamer").Body.String()
	if !strings.Contains(ended, "Final result") || strings.Contains(ended, `id="countdown"`) || strings.Contains(ended, `class="ticker strip"`) || !strings.Contains(ended, `data-final="1"`) {
		t.Error("the ended beamer does not show the final result")
	}
}

func TestBoardOptions(t *testing.T) {
	e := newEnv(t)
	e.game("Secret", url.Values{"hidden": {"on"}}, "running")
	for _, p := range []string{"/g/secret/", "/g/secret/board.json", "/g/secret/chart.svg", "/g/secret/beamer", "/g/secret/p/1", "/g/secret/c/scoreBoardChallenge", "/g/secret/cat/xss"} {
		if w := e.get(base + p); w.Code != 404 {
			t.Errorf("hidden board %s: %d", p, w.Code)
		}
	}
	if strings.Contains(e.get(base+"/").Body.String(), "Secret") {
		t.Error("a hidden game is listed")
	}
	e.game("Draft", nil, "")
	if e.get(base+"/g/draft/").Code != 404 {
		t.Error("a draft game's board is public")
	}

	seed(t, e, url.Values{"initials": {"on"}})
	pages := crawl(t, e, base+"/g/golden-game/")
	pages["json"] = e.get(base + "/g/golden-game/board.json").Body.String()
	for p, body := range pages {
		for _, full := range []string{"Zoë", "Vries", "Neill", "Chen"} {
			if strings.Contains(body, full) {
				t.Errorf("%s shows %q on an initials board", p, full)
			}
		}
	}
	if !strings.Contains(pages["json"], `"Z.D.V."`) {
		t.Error("initials are not shown")
	}
}
