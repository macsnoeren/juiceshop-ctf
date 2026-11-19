package store

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/shared/protocol"
)

var t0 = time.Date(2026, 11, 19, 9, 0, 0, 0, time.UTC)

func at(min int) time.Time { return t0.Add(time.Duration(min) * time.Minute) }

func newStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "central.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// game makes a game in the given state and returns it with its code.
func game(t *testing.T, st *Store, name, state string) (*Game, string) {
	t.Helper()
	g, code, err := st.CreateGame(GameInput{Name: name}, at(-60))
	if err != nil {
		t.Fatal(err)
	}
	if state != protocol.StateDraft {
		if err := st.SetState(g.ID, state, at(-60)); err != nil {
			t.Fatal(err)
		}
	}
	g, _ = st.Game(g.ID)
	return g, code
}

type member struct {
	id    int64
	token string
	game  *Game
}

func join(t *testing.T, st *Store, code, name string, when time.Time) member {
	t.Helper()
	req := &protocol.JoinRequest{Protocol: 2, Name: name, Code: code, Node: protocol.Node{UID: protocol.NewNodeUID(), Version: "v1.1.0-test", JuiceShop: "20.3.0"}}
	if err := req.Normalize(); err != nil {
		t.Fatal(err)
	}
	res, _, err := st.Join(req, when)
	if err != nil {
		t.Fatalf("join %s: %v", name, err)
	}
	return member{res.Participant.ID, res.Token, res.Game}
}

// rep sends a report with the given solves (key → minute of solving).
func rep(t *testing.T, st *Store, m member, when time.Time, boot string, solves map[string]int, applied ...string) (protocol.ReportResponse, ReportResult) {
	t.Helper()
	r := &protocol.Report{Protocol: 2, SentAt: when, Boot: boot, Reachable: true,
		Node: protocol.Node{UID: strings.Repeat("a", 32), Version: "v1.1.0-test", JuiceShop: "20.3.0"}, CommandsApplied: applied}
	for k, min := range solves {
		r.Solves = append(r.Solves, protocol.Solve{Key: k, SolvedAt: at(min)})
	}
	if err := r.Validate(); err != nil {
		t.Fatal(err)
	}
	resp, res, err := st.Report(protocol.Hash(m.token), r, when, "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	return resp, res
}

func scores(t *testing.T, st *Store, g *Game, now time.Time) map[string]int {
	t.Helper()
	b, err := st.Board(g, now)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]int{}
	for _, r := range b.Rows {
		out[r.Name] = r.Score
	}
	return out
}

// ---- T1.1-11: migrating a 1.0 database --------------------------------

// lab10 builds a database as 1.0 left it after the lab: one instance shared
// by every VM, five students, the first four ended by the fifth's sign-up
// (VERSION-1-1.md §1.1), each with solves.
func lab10(t *testing.T, path string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, q := range []string{
		`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at TEXT NOT NULL)`,
		migrations[0],
		`INSERT INTO schema_migrations VALUES (1, '2026-11-18T10:00:00.000Z')`,
		`INSERT INTO instances (id, token_hash, created_at, last_report_at, agent_version) VALUES ('lab-1', 'h', '2026-11-18T10:00:00.000Z', '2026-11-18T12:00:00.000Z', '0.1.0')`,
		`INSERT INTO challenges (key, name, category, difficulty) VALUES ('scoreBoardChallenge', 'Score Board', 'Miscellaneous', 1)`,
		`INSERT INTO scoring VALUES (1, 111) ON CONFLICT (difficulty) DO UPDATE SET points = 111`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	names := []string{"Anna", "Bram", "Chloé", "Daan", "Eva"}
	for i, n := range names {
		ended := "'2026-11-18T10:5" + fmt.Sprint(i) + ":00.000Z'"
		if i == len(names)-1 {
			ended = "NULL"
		}
		q := fmt.Sprintf(`INSERT INTO participants (id, instance_id, registration_id, name, registered_at, ended_at) VALUES (%d, 'lab-1', 'r%d', '%s', '2026-11-18T10:0%d:00.000Z', %s)`, i+1, i, n, i, ended)
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"scoreBoardChallenge", "directoryListingChallenge"}[:1+i%2] {
			if _, err := db.Exec(`INSERT INTO solves VALUES (?, ?, ?, NULL)`, i+1, k, fmt.Sprintf("2026-11-18T11:0%d:00.000Z", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestMigrationBringsTheLabBack(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lab.db")
	lab10(t, path)
	st, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	g, err := st.GameBySlug("1-0")
	if err != nil {
		t.Fatal("no 1.0 game:", err)
	}
	if g.State != protocol.StateClosed || g.HasCode {
		t.Errorf("1.0 game is %s, code %v; want closed without a code", g.State, g.HasCode)
	}
	if g.Scoring[1] != 111 {
		t.Errorf("1.0's own scoring was not kept: %v", g.Scoring)
	}
	b, err := st.Board(g, at(0))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Rows) != 5 {
		t.Fatalf("%d students on the 1.0 board, want all 5: %+v", len(b.Rows), b.Rows)
	}
	for _, r := range b.Rows {
		if r.Solved == 0 {
			t.Errorf("%s lost their solves", r.Name)
		}
	}
	ps, _ := st.GameParticipants(g, at(0))
	if ps[0].LegacyInstance != "lab-1" {
		t.Error("the 1.0 instance id is not kept for reference")
	}
	for _, tbl := range []string{"instances", "scoring"} {
		var n int
		st.db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE name = ?`, tbl).Scan(&n)
		if n != 0 {
			t.Errorf("table %s still exists", tbl)
		}
	}
	v1, _ := st.SchemaVersion()
	st.Close()

	st, err = Open(path) // twice: a no-op
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	v2, _ := st.SchemaVersion()
	var games, parts, solves int
	st.db.QueryRow(`SELECT COUNT(*) FROM games`).Scan(&games)
	st.db.QueryRow(`SELECT COUNT(*) FROM participants`).Scan(&parts)
	st.db.QueryRow(`SELECT COUNT(*) FROM solves`).Scan(&solves)
	if v1 != len(migrations) || v2 != v1 || games != 1 || parts != 5 || solves != 7 {
		t.Errorf("second open changed things: schema %d→%d, %d games, %d participants, %d solves", v1, v2, games, parts, solves)
	}
}

func TestFreshDatabaseHasNoLegacyGame(t *testing.T) {
	st := newStore(t)
	if _, err := st.GameBySlug("1-0"); !errors.Is(err, ErrNotFound) {
		t.Error("a fresh database must not get an empty 1.0 game")
	}
}

// ---- T1.1-12: events and retention -------------------------------------

func TestPruneDeletesOldEvents(t *testing.T) {
	st := newStore(t)
	now := t0
	for _, age := range []int{40, 31, 29, 1} {
		st.AddEvent(Event{At: now.Add(-time.Duration(age) * 24 * time.Hour), Kind: EvAdmin, Text: fmt.Sprint(age, " days")})
	}
	n, err := st.Prune(now)
	if err != nil {
		t.Fatal(err)
	}
	evs, _ := st.Events(EventFilter{})
	if n != 2 || len(evs) != 2 || evs[0].Text != "1 days" || evs[1].Text != "29 days" {
		t.Errorf("pruned %d, left %+v", n, evs)
	}
	st.SaveSettings(Settings{Title: "t", Timezone: "UTC", RetentionDays: 7})
	st.Prune(now)
	evs, _ = st.Events(EventFilter{})
	if len(evs) != 1 {
		t.Errorf("a 7-day retention left %d events", len(evs))
	}
}

// ---- T1.1-13: games ------------------------------------------------------

func TestGameTransitions(t *testing.T) {
	st := newStore(t)
	states := []string{protocol.StateDraft, protocol.StateOpen, protocol.StateRunning, protocol.StateClosed}
	allowed := map[[2]string]bool{
		{"draft", "open"}: true, {"draft", "running"}: true, {"draft", "closed"}: true,
		{"open", "running"}: true, {"open", "closed"}: true,
		{"running", "closed"}: true,
		{"closed", "open"}:    true, {"closed", "running"}: true,
	}
	for _, from := range states {
		for _, to := range states {
			g, _ := game(t, st, "T "+from+" "+to, protocol.StateDraft)
			// walk to `from` through an allowed path
			for _, step := range map[string][]string{"draft": nil, "open": {"open"}, "running": {"running"}, "closed": {"closed"}}[from] {
				if err := st.SetState(g.ID, step, t0); err != nil {
					t.Fatal(err)
				}
			}
			err := st.SetState(g.ID, to, t0)
			if allowed[[2]string{from, to}] != (err == nil) {
				t.Errorf("%s → %s: err %v", from, to, err)
			}
		}
	}
}

func TestSlugsAreUnique(t *testing.T) {
	st := newStore(t)
	a, _ := game(t, st, "Class 2A — 19 Nov", protocol.StateDraft)
	b, _ := game(t, st, "Class 2A — 19 Nov", protocol.StateDraft)
	c, _ := game(t, st, "Klas Zoë", protocol.StateDraft)
	if a.Slug != "class-2a-19-nov" || b.Slug != "class-2a-19-nov-2" || c.Slug != "klas-zoe" {
		t.Errorf("slugs %q %q %q", a.Slug, b.Slug, c.Slug)
	}
	if _, _, err := st.CreateGame(GameInput{Name: "x", StartsAt: ptr(at(10)), EndsAt: ptr(at(5))}, t0); err == nil {
		t.Error("an end before the start was accepted")
	}
}

func ptr(t time.Time) *time.Time { return &t }

// ---- T1.1-14: join -------------------------------------------------------

func TestJoin(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Open", protocol.StateOpen)
	d, dcode := game(t, st, "Draft", protocol.StateDraft)
	c, ccode := game(t, st, "Closed", protocol.StateClosed)
	_, _ = d, c
	m := join(t, st, code, "Zoë", t0)
	if !protocol.ValidParticipantToken(m.token) || m.game.ID != g.ID {
		t.Errorf("join gave %+v", m)
	}
	var stored string
	st.db.QueryRow(`SELECT token_hash FROM participants WHERE id = ?`, m.id).Scan(&stored)
	if stored != protocol.Hash(m.token) {
		t.Error("the token is not stored as its hash")
	}
	for _, tc := range []struct {
		code string
		want error
	}{{"23456789ab", ErrUnknownCode}, {dcode, ErrGameDraft}, {ccode, ErrGameClosed}} {
		req := &protocol.JoinRequest{Protocol: 2, Name: "Bram", Code: tc.code, Node: protocol.Node{UID: protocol.NewNodeUID()}}
		req.Normalize()
		if _, _, err := st.Join(req, t0); !errors.Is(err, tc.want) {
			t.Errorf("code %s: %v, want %v", tc.code, err, tc.want)
		}
	}
	if _, res := rep(t, st, m, at(1), "b1", map[string]int{"scoreBoardChallenge": 1}); res.Outcome != ReportMerged {
		t.Error("the token from the join does not work for a report")
	}
}

// ---- T1.1-16: reports ----------------------------------------------------

// TestFiveStudentsStayOnTheBoard is the lab's F1 as a test: in 1.0 each new
// sign-up ended everyone before it.
func TestFiveStudentsStayOnTheBoard(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	var ms []member
	for i, n := range []string{"Anna", "Bram", "Chloé", "Daan", "Eva"} {
		ms = append(ms, join(t, st, code, n, at(i)))
	}
	order := []int{4, 0, 3, 1, 2, 2, 4, 0, 1, 3}
	for i, k := range order {
		rep(t, st, ms[k], at(10+i), "boot", map[string]int{"scoreBoardChallenge": 9 + i})
	}
	sc := scores(t, st, g, at(30))
	if len(sc) != 5 {
		t.Fatalf("board has %d students, want 5: %v", len(sc), sc)
	}
	for n, s := range sc {
		if s != 100 {
			t.Errorf("%s has %d points, want 100", n, s)
		}
	}
}

func TestSolvesAreKeptAndClamped(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	m := join(t, st, code, "Anna", t0)
	rep(t, st, m, at(10), "b", map[string]int{"scoreBoardChallenge": 5, "directoryListingChallenge": 6})
	rep(t, st, m, at(11), "b", map[string]int{"scoreBoardChallenge": 9}) // one solve missing, one later time
	rep(t, st, m, at(12), "b", map[string]int{"exposedMetricsChallenge": 600})
	rows, _ := st.db.Query(`SELECT challenge_key, solved_at FROM solves ORDER BY challenge_key`)
	got := map[string]string{}
	for rows.Next() {
		var k, a string
		rows.Scan(&k, &a)
		got[k] = a
	}
	rows.Close()
	if got["scoreBoardChallenge"] != ts(at(5)) {
		t.Errorf("the first solve time was not kept: %s", got["scoreBoardChallenge"])
	}
	if _, ok := got["directoryListingChallenge"]; !ok {
		t.Error("a solve missing from a later report was removed")
	}
	if got["exposedMetricsChallenge"] != ts(at(12)) {
		t.Errorf("a solve 10 h in the future was not clamped to the receipt: %s", got["exposedMetricsChallenge"])
	}
	if sc := scores(t, st, g, at(13)); sc["Anna"] != 300 {
		t.Errorf("score %d, want 300", sc["Anna"])
	}
}

func TestSolveAfterTheEndIsStoredButNotCounted(t *testing.T) {
	st := newStore(t)
	g, code, _ := st.CreateGame(GameInput{Name: "Timed", EndsAt: ptr(at(30))}, t0)
	st.SetState(g.ID, protocol.StateRunning, t0)
	g, _ = st.Game(g.ID)
	m := join(t, st, code, "Anna", at(1))
	rep(t, st, m, at(20), "b", map[string]int{"scoreBoardChallenge": 19})
	resp, _ := rep(t, st, m, at(31), "b", map[string]int{"scoreBoardChallenge": 19, "directoryListingChallenge": 30})
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM solves`).Scan(&n)
	if n != 2 {
		t.Errorf("%d solves stored, want 2", n)
	}
	if sc := scores(t, st, g, at(32)); sc["Anna"] != 100 {
		t.Errorf("score %d after the end, want 100", sc["Anna"])
	}
	if resp.Game.State != protocol.StateClosed {
		t.Errorf("a game past its end reports state %q, want closed", resp.Game.State)
	}
	req := &protocol.JoinRequest{Protocol: 2, Name: "Late", Code: code, Node: protocol.Node{UID: protocol.NewNodeUID()}}
	req.Normalize()
	if _, _, err := st.Join(req, at(31)); !errors.Is(err, ErrGameClosed) {
		t.Errorf("join after the end: %v", err)
	}
}

func TestUnknownKeysAreIgnored(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	m := join(t, st, code, "Anna", t0)
	rep(t, st, m, at(1), "b", map[string]int{"madeUpChallenge": 1, "scoreBoardChallenge": 1})
	if sc := scores(t, st, g, at(2)); sc["Anna"] != 100 {
		t.Errorf("score %d, want 100", sc["Anna"])
	}
}

// ---- T1.1-17, -18: commands and leave ------------------------------------

func TestForgetReachesOnlyThatParticipant(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	var ms []member
	for i := range 5 {
		ms = append(ms, join(t, st, code, fmt.Sprint("Student ", i), t0))
	}
	if err := st.Remove(ms[2].id, at(1)); err != nil {
		t.Fatal(err)
	}
	for round := range 2 {
		for i, m := range ms {
			resp, res := rep(t, st, m, at(2+round), "b", nil)
			got := len(resp.Commands)
			if i == 2 {
				if got != 1 || resp.Commands[0].Type != protocol.CmdForget || res.Outcome != ReportForgotten {
					t.Errorf("round %d: the removed participant got %+v (%v)", round, resp.Commands, res.Outcome)
				}
			} else if got != 0 {
				t.Errorf("round %d: student %d got a command meant for another", round, i)
			}
		}
	}
	resp, _ := rep(t, st, ms[2], at(5), "b", nil)
	_, res := rep(t, st, ms[2], at(6), "b", nil, resp.Commands[0].ID)
	if res.Outcome != ReportUnknown {
		t.Errorf("after confirming, the removed token still gets %v", res.Outcome)
	}
	var confirmed int
	st.db.QueryRow(`SELECT COUNT(*) FROM events WHERE text LIKE 'forget confirmed%'`).Scan(&confirmed)
	if confirmed != 1 {
		t.Errorf("%d confirmations, want 1", confirmed)
	}
	if sc := scores(t, st, g, at(7)); len(sc) != 4 {
		t.Errorf("board %v, want the 4 others", sc)
	}
}

func TestLeave(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	m := join(t, st, code, "Anna", t0)
	rep(t, st, m, at(1), "b", map[string]int{"scoreBoardChallenge": 1})
	out, p, err := st.Leave(protocol.Hash(m.token), protocol.LeaveForget, at(2), "192.0.2.1")
	if err != nil || out != LeaveLeft || p.ID != m.id {
		t.Fatalf("leave: %v %v", out, err)
	}
	if _, res := rep(t, st, m, at(3), "b", nil); res.Outcome != ReportUnknown {
		t.Error("the token still works after leaving")
	}
	if out, _, _ := st.Leave(protocol.Hash(m.token), protocol.LeaveForget, at(3), ""); out != LeaveUnknown {
		t.Error("a second leave with the same token was accepted")
	}
	ps, _ := st.GameParticipants(g, at(4))
	if len(ps) != 1 || ps[0].Status() != "left" || ps[0].Solved != 1 {
		t.Errorf("admin shows %+v", ps[0])
	}
	if len(scores(t, st, g, at(4))) != 0 {
		t.Error("a participant who left is still on the board")
	}
	evs, _ := st.Events(EventFilter{Kind: EvLeave})
	if len(evs) != 1 || !strings.HasPrefix(evs[0].Text, "left (forget)") {
		t.Errorf("leave events %+v", evs)
	}
}

func TestClearGameTellsEveryVMAndLeavesOthersAlone(t *testing.T) {
	st := newStore(t)
	a, acode := game(t, st, "A", protocol.StateRunning)
	b, bcode := game(t, st, "B", protocol.StateRunning)
	ma := join(t, st, acode, "Anna", t0)
	mb := join(t, st, bcode, "Bram", t0)
	rep(t, st, ma, at(1), "b", map[string]int{"scoreBoardChallenge": 1})
	rep(t, st, mb, at(1), "b", map[string]int{"scoreBoardChallenge": 1})
	n, err := st.ClearGame(a.ID, at(2))
	if err != nil || n != 1 {
		t.Fatalf("clear: %d %v", n, err)
	}
	resp, res := rep(t, st, ma, at(3), "b", nil)
	if res.Outcome != ReportForgotten || len(resp.Commands) != 1 {
		t.Errorf("a VM of the cleared game was not told: %v %+v", res.Outcome, resp)
	}
	if out, _, _ := st.Leave(protocol.Hash(ma.token), protocol.LeaveCommand, at(4), ""); out != LeaveConfirmed {
		t.Errorf("the VM's leave after the forget: %v", out)
	}
	if sc := scores(t, st, b, at(5)); sc["Bram"] != 100 {
		t.Error("clearing A touched B")
	}
	if g, err := st.Game(a.ID); err != nil || !g.HasCode {
		t.Error("clear game must keep the game and its code")
	}
	if err := st.DeleteGame(b.ID, at(6)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Game(b.ID); !errors.Is(err, ErrNotFound) {
		t.Error("the game was not deleted")
	}
	if resp, _ := rep(t, st, mb, at(7), "b", nil); len(resp.Commands) != 1 {
		t.Error("a VM of a deleted game was not told forget")
	}
}

// ---- T1.1-20: standings --------------------------------------------------

func TestTwoGamesRankSeparately(t *testing.T) {
	st := newStore(t)
	a, acode := game(t, st, "A", protocol.StateRunning)
	b, bcode := game(t, st, "B", protocol.StateRunning)
	for i, n := range []string{"A1", "A2", "A3"} {
		m := join(t, st, acode, n, t0)
		sv := map[string]int{"scoreBoardChallenge": 1}
		if i == 0 {
			sv["loginAdminChallenge"] = 2
		}
		rep(t, st, m, at(10-i), "b", sv)
	}
	for _, n := range []string{"B1", "B2", "B3"} {
		join(t, st, bcode, n, t0)
	}
	ba, _ := st.Board(a, at(20))
	bb, _ := st.Board(b, at(20))
	if len(ba.Rows) != 3 || len(bb.Rows) != 3 {
		t.Fatalf("rows %d / %d", len(ba.Rows), len(bb.Rows))
	}
	// A1 most points; A3 then A2 (A3's report was received earlier).
	if ba.Rows[0].Name != "A1" || ba.Rows[1].Name != "A3" || ba.Rows[2].Name != "A2" || ba.Rows[1].Rank != 2 {
		t.Errorf("game A ranks %+v", ba.Rows)
	}
	for _, r := range bb.Rows {
		if r.Rank != 1 || r.Score != 0 {
			t.Errorf("game B: %+v", r)
		}
	}
}

func TestFirstBloodGoesByReceipt(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	early := join(t, st, code, "Early clock", t0)
	honest := join(t, st, code, "Honest", t0)
	rep(t, st, honest, at(10), "b", map[string]int{"scoreBoardChallenge": 10})
	rep(t, st, early, at(11), "b", map[string]int{"scoreBoardChallenge": 1}) // claims minute 1, arrives later
	b, _ := st.Board(g, at(12))
	for _, it := range b.Ticker {
		if it.FirstBlood != (it.Name == "Honest") {
			t.Errorf("first blood on %+v", it)
		}
	}
	if b.Rows[0].Score != b.Rows[1].Score {
		t.Error("first blood changed a score")
	}
}

// ---- T1.1-21: the events of a scripted run --------------------------------

func TestScriptedRunEvents(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	m := join(t, st, code, "Anna", at(0))
	rep(t, st, m, at(1), "b", map[string]int{"scoreBoardChallenge": 1})
	rep(t, st, m, at(1).Add(20*time.Second), "b", map[string]int{"scoreBoardChallenge": 1}) // nothing new, within the minute: collapsed
	rep(t, st, m, at(3), "b", map[string]int{"scoreBoardChallenge": 1})                     // a minute later: one line
	st.Remove(m.id, at(4))
	resp, _ := rep(t, st, m, at(5), "b", nil)
	st.Leave(protocol.Hash(m.token), protocol.LeaveCommand, at(6), "")
	_ = resp
	evs, _ := st.Events(EventFilter{GameID: g.ID})
	var got []string
	for i := len(evs) - 1; i >= 0; i-- {
		txt := evs[i].Text
		if j := strings.Index(txt, " ("); j > 0 && evs[i].Kind == EvCommand {
			txt = txt[:j]
		}
		got = append(got, evs[i].Kind+": "+txt)
	}
	want := []string{
		"solve: solved Score Board (1★, Miscellaneous)",
		"report: report: 1 solves, 1 new; Juice Shop up; node v1.1.0-test",
		"report: report: 1 solves, 0 new; Juice Shop up; node v1.1.0-test",
		"command: forget queued",
		"command: forget delivered",
		"command: forget confirmed by the VM",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("events:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// ---- T1.1-22: flags ------------------------------------------------------

func flagKinds(t *testing.T, st *Store, g *Game) map[string]bool {
	t.Helper()
	fl, _ := st.FlagsOf(g.ID)
	out := map[string]bool{}
	for _, fs := range fl {
		for _, f := range fs {
			out[f.Kind] = true
		}
	}
	return out
}

func TestFlags(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		want string
		run  func(t *testing.T, st *Store, m member)
	}{
		{"prerequisite", FlagPrerequisite, func(t *testing.T, st *Store, m member) {
			rep(t, st, m, at(10), "b", map[string]int{"easterEggLevelOneChallenge": 9})
		}},
		{"burst", FlagBurst, func(t *testing.T, st *Store, m member) {
			sv := map[string]int{}
			for _, k := range []string{"scoreBoardChallenge", "directoryListingChallenge", "exposedMetricsChallenge", "errorHandlingChallenge", "loginAdminChallenge", "privacyPolicyChallenge"} {
				sv[k] = 9
			}
			rep(t, st, m, at(10), "b", sv)
		}},
		{"after end", FlagAfterEnd, func(t *testing.T, st *Store, m member) {
			st.SetState(m.game.ID, protocol.StateClosed, at(20))
			st.SetState(m.game.ID, protocol.StateRunning, at(20)) // reopened; closed_at cleared
			g, _ := st.Game(m.game.ID)
			st.UpdateGame(g.ID, GameInput{Name: g.Name, EndsAt: ptr(at(30))})
			rep(t, st, m, at(45), "b", map[string]int{"scoreBoardChallenge": 40})
		}},
		{"before join", FlagBeforeJoin, func(t *testing.T, st *Store, m member) {
			rep(t, st, m, at(10), "b", map[string]int{"scoreBoardChallenge": -30})
		}},
		{"two boots", FlagTwoBoots, func(t *testing.T, st *Store, m member) {
			rep(t, st, m, at(10), "boot-A", nil)
			rep(t, st, m, at(11), "boot-B", nil)
			rep(t, st, m, at(12), "boot-A", nil)
		}},
		{"cheat score", FlagCheatScore, func(t *testing.T, st *Store, m member) {
			r := &protocol.Report{Protocol: 2, SentAt: at(10), Node: protocol.Node{UID: strings.Repeat("a", 32)}}
			for i, k := range []string{"scoreBoardChallenge", "directoryListingChallenge", "exposedMetricsChallenge", "errorHandlingChallenge", "loginAdminChallenge"} {
				r.Solves = append(r.Solves, protocol.Solve{Key: k, SolvedAt: at(i * 2), Cheat: f(0.97)})
			}
			st.Report(protocol.Hash(m.token), r, at(10), "")
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := newStore(t)
			g, code := game(t, st, "Lab", protocol.StateRunning)
			m := join(t, st, code, "Anna", t0)
			c.run(t, st, m)
			got := flagKinds(t, st, g)
			if !got[c.want] || len(got) != 1 {
				t.Errorf("flags %v, want only %s", got, c.want)
			}
			evs, _ := st.Events(EventFilter{Kind: EvFlag})
			if len(evs) != 1 {
				t.Errorf("%d flag events, want 1", len(evs))
			}
		})
	}
}

func TestHonestPlayRaisesNoFlag(t *testing.T) {
	st := newStore(t)
	g, code := game(t, st, "Lab", protocol.StateRunning)
	m := join(t, st, code, "Anna", t0)
	sv := map[string]int{}
	f := 0.3
	for i, k := range []string{"scoreBoardChallenge", "directoryListingChallenge", "nullByteChallenge", "easterEggLevelOneChallenge", "exposedMetricsChallenge"} {
		sv[k] = 5 + i*3
		rep(t, st, m, at(6+i*3), "boot-1", sv)
	}
	rep(t, st, m, at(30), "boot-2", sv) // Juice Shop restarted: a new boot is no flag
	rep(t, st, m, at(31), "boot-2", sv)
	_ = f
	if got := flagKinds(t, st, g); len(got) != 0 {
		t.Errorf("honest play raised %v", got)
	}
}
