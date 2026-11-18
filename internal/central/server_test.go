package central

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
	"github.com/macsnoeren/juiceshop-ctf/internal/store"
)

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

const tok1 = "token-for-cs-1-xxxxxxxxxxxx"

func newServer(t *testing.T) (*Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "c.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	st.SeedInstance("cs-1", "", tok1, t0)
	st.SeedInstance("cs-2", "", "token-for-cs-2-xxxxxxxxxxxx", t0)
	s, err := New(Config{BasePath: "/leaderboard"}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0.Add(time.Hour) }
	return s, st
}

func sampleReport(instance, rid, name string) protocol.Report {
	solved := t0.Add(10 * time.Minute)
	return protocol.Report{
		Protocol: 1, Instance: instance, SentAt: t0.Add(time.Hour), AgentVersion: "0.1.0",
		JuiceShop:    protocol.JuiceShop{Version: "20.3.0", Reachable: true},
		Registration: &protocol.Registration{ID: strings.Repeat(rid, 32), Name: name, RegisteredAt: t0},
		Challenges: []protocol.Challenge{
			{Key: "scoreBoardChallenge", Name: "Score Board", Category: "Miscellaneous", Difficulty: 1, SolvedAt: &solved},
			{Key: "loginAdminChallenge", Name: "Login Admin", Category: "Injection", Difficulty: 2},
		},
	}
}

func send(s *Server, token string, body []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", "/leaderboard/api/v1/report", bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func jsonOf(v any) []byte { b, _ := json.Marshal(v); return b }

func TestReportEndpoint(t *testing.T) {
	s, st := newServer(t)
	good := jsonOf(sampleReport("cs-1", "a", "Sanne de Vries"))

	if rec := send(s, "", good); rec.Code != 401 {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := send(s, "wrong-token-xxxxxxxxxxxx", good); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := send(s, tok1, jsonOf(sampleReport("cs-2", "a", "Sanne de Vries"))); rec.Code != 400 {
		t.Errorf("report for another instance: %d", rec.Code)
	}
	bad := sampleReport("cs-1", "a", "Sanne de Vries")
	bad.Protocol = 9
	if rec := send(s, tok1, jsonOf(bad)); rec.Code != 400 || !strings.Contains(rec.Body.String(), "protocol") {
		t.Errorf("unknown protocol: %d %s", rec.Code, rec.Body)
	}
	if rec := send(s, tok1, []byte("{not json")); rec.Code != 400 {
		t.Errorf("not JSON: %d", rec.Code)
	}
	huge := append([]byte(`{"pad":"`), bytes.Repeat([]byte("x"), protocol.MaxBodyBytes)...)
	huge = append(huge, []byte(`"}`)...)
	if rec := send(s, tok1, huge); rec.Code != 413 {
		t.Errorf("too large: %d", rec.Code)
	}

	rec := send(s, tok1, good)
	if rec.Code != 200 {
		t.Fatalf("good report: %d %s", rec.Code, rec.Body)
	}
	var resp protocol.Response
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if !resp.OK || len(resp.Commands) != 0 || resp.Standing == nil || resp.Standing.Score != 100 || resp.Standing.Rank != 1 {
		t.Errorf("response = %+v", resp)
	}

	rows, _ := st.Leaderboard(t0.Add(time.Hour))
	st.ResetParticipant(rows[0].ParticipantID, t0.Add(time.Hour))
	rec = send(s, tok1, good)
	resp = protocol.Response{} // Unmarshal does not clear fields absent from the JSON
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Commands) != 1 || resp.Commands[0].Type != protocol.CmdResetRegistration {
		t.Errorf("pending command not delivered: %+v", resp)
	}
	if resp.Standing != nil {
		t.Error("an ended participant still has a standing")
	}
}

var urlAttr = regexp.MustCompile(`(?:href|action|src|data-api|data-base)="([^"]*)"`)

func page(t *testing.T, s *Server, path string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	if rec.Code != 200 {
		t.Fatalf("GET %s: %d", path, rec.Code)
	}
	body := rec.Body.String()
	for _, m := range urlAttr.FindAllStringSubmatch(body, -1) {
		if strings.HasPrefix(m[1], "/") && !strings.HasPrefix(m[1], "/leaderboard") {
			t.Errorf("%s: %q is not under the base path", path, m[1])
		}
	}
	return body
}

func TestPublicPages(t *testing.T) {
	s, st := newServer(t)
	send(s, tok1, jsonOf(sampleReport("cs-1", "a", "Sanne de Vries")))
	// A name the protocol would refuse, put straight into the database, to
	// prove the templates escape regardless.
	r := sampleReport("cs-2", "b", "x")
	r.Registration.Name = "<script>alert(1)</script>"
	st.MergeReport("cs-2", &r, t0.Add(time.Hour))

	body := page(t, s, "/leaderboard/")
	if !strings.Contains(body, "Sanne de Vries") {
		t.Error("index does not show the participant")
	}
	if strings.Contains(body, "<script>alert") {
		t.Error("a name was not escaped")
	}
	page(t, s, "/leaderboard/beamer")
	rows, _ := st.Leaderboard(t0.Add(time.Hour))
	var sanne int64
	for _, row := range rows {
		if row.Name == "Sanne de Vries" {
			sanne = row.ParticipantID
		}
	}
	if !strings.Contains(page(t, s, "/leaderboard/p/"+itoa(sanne)), "Score Board") {
		t.Error("participant page lacks the solve")
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/leaderboard/api/v1/leaderboard", nil))
	var b struct {
		Total int `json:"total"`
		Rows  []struct {
			Rank        int    `json:"rank"`
			Participant int64  `json:"participant"`
			Name        string `json:"name"`
			Score       int    `json:"score"`
			Solved      int    `json:"solved"`
			Total       int    `json:"total"`
			Online      bool   `json:"online"`
		} `json:"rows"`
		Recent []map[string]any `json:"recent"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Total != 2 || len(b.Rows) != 2 || b.Rows[0].Score != 100 || !b.Rows[0].Online || len(b.Recent) != 2 {
		t.Errorf("leaderboard JSON = %+v", b)
	}
	if strings.Contains(rec.Body.String(), "cs-1") || strings.Contains(rec.Body.String(), "cheat") {
		t.Error("the public JSON leaks instance ids or cheat scores")
	}

	st.SetHidden(sanne, true)
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/leaderboard/api/v1/leaderboard", nil))
	if strings.Contains(rec.Body.String(), "Sanne") {
		t.Error("a hidden participant is in the JSON")
	}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/leaderboard/p/"+itoa(sanne), nil))
	if rec.Code != 404 {
		t.Errorf("hidden participant page: %d", rec.Code)
	}
}

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// admin GETs the admin page and returns its body and the CSRF cookie.
func admin(t *testing.T, s *Server) (string, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/leaderboard/admin/", nil))
	if rec.Code != 200 {
		t.Fatalf("admin: %d", rec.Code)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == adminCSRFCookie {
			return rec.Body.String(), c
		}
	}
	t.Fatal("no admin csrf cookie")
	return "", nil
}

func adminPost(s *Server, path string, form url.Values, c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest("POST", path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if c != nil {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

var tokenRE = regexp.MustCompile(`ctf_[0-9a-f]{64}`)

func TestAdmin(t *testing.T) {
	s, st := newServer(t)
	_, c := admin(t, s)

	if rec := adminPost(s, "/leaderboard/admin/instances", url.Values{"id": {"cs-9"}}, nil); rec.Code != 403 {
		t.Errorf("POST without CSRF: %d", rec.Code)
	}
	rec := adminPost(s, "/leaderboard/admin/instances", url.Values{"csrf": {c.Value}, "id": {"cs-9"}, "label": {"VPS 9"}}, c)
	tok := tokenRE.FindString(rec.Body.String())
	if rec.Code != 200 || tok == "" {
		t.Fatalf("create: %d, token shown: %v", rec.Code, tok != "")
	}
	if id, _ := st.InstanceForToken(tok); id != "cs-9" {
		t.Error("the shown token does not work")
	}
	body, _ := admin(t, s)
	if tokenRE.MatchString(body) {
		t.Error("the token is readable after creation")
	}
	if !strings.Contains(body, "cs-9") {
		t.Error("new instance not listed")
	}

	// Reset queues exactly one command, however often it is clicked.
	send(s, tok1, jsonOf(sampleReport("cs-1", "a", "Sanne de Vries")))
	rows, _ := st.Leaderboard(t0.Add(time.Hour))
	pid := itoa(rows[0].ParticipantID)
	for i := 0; i < 3; i++ {
		if rec := adminPost(s, "/leaderboard/admin/participants/"+pid+"/reset", url.Values{"csrf": {c.Value}}, c); rec.Code != 303 {
			t.Fatalf("reset: %d", rec.Code)
		}
	}
	cmds, _ := st.PendingCommands("cs-1", t0)
	if len(cmds) != 1 {
		t.Errorf("%d commands queued, want 1", len(cmds))
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/leaderboard/admin/export.csv", nil))
	recs, err := csv.NewReader(rec.Body).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(recs) != 2 || recs[0][0] != "participant" || recs[1][1] != "Sanne de Vries" {
		t.Errorf("csv = %v", recs)
	}

	if rec := adminPost(s, "/leaderboard/admin/scoring", url.Values{"csrf": {c.Value}, "points1": {"5"}}, c); rec.Code != 303 {
		t.Errorf("scoring: %d", rec.Code)
	}
	if sc, _ := st.Scoring(); sc[1] != 5 {
		t.Error("scoring not saved")
	}
}

func TestCSVSafe(t *testing.T) {
	for in, want := range map[string]string{"-1": "'-1", "Ana": "Ana", "": ""} {
		if got := csvSafe(in); got != want {
			t.Errorf("csvSafe(%q) = %q", in, got)
		}
	}
}
