package store

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

var t0 = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)

func open(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "central.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func at(min int) *time.Time { t := t0.Add(time.Duration(min) * time.Minute); return &t }

func regID(c byte) string { return strings.Repeat(string(c), 32) }

func report(instance, rid, name string, solved map[string]*time.Time) *protocol.Report {
	r := &protocol.Report{
		Protocol: 1, Instance: instance, SentAt: t0.Add(time.Hour), AgentVersion: "test",
		JuiceShop: protocol.JuiceShop{Version: "20.3.0", Reachable: true},
		Challenges: []protocol.Challenge{
			{Key: "scoreBoardChallenge", Name: "Score Board", Category: "Miscellaneous", Difficulty: 1},
			{Key: "loginAdminChallenge", Name: "Login Admin", Category: "Injection", Difficulty: 2},
			{Key: "xssChallenge", Name: "DOM XSS", Category: "XSS", Difficulty: 3},
			{Key: "xxeChallenge", Name: "XXE", Category: "XXE", Difficulty: 3, Disabled: true},
		},
	}
	if rid != "" {
		r.Registration = &protocol.Registration{ID: rid, Name: name, RegisteredAt: t0}
	}
	for i := range r.Challenges {
		r.Challenges[i].SolvedAt = solved[r.Challenges[i].Key]
	}
	return r
}

func mustSeed(t *testing.T, s *Store, ids ...string) {
	t.Helper()
	for _, id := range ids {
		if err := s.SeedInstance(id, "", "token-for-"+id+"-xxxxxxxx", t0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrateTwiceIsNoop(t *testing.T) {
	s, path := open(t)
	v1, _ := s.SchemaVersion()
	s.Close()
	s2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	v2, _ := s2.SchemaVersion()
	if v1 != len(migrations) || v2 != v1 {
		t.Errorf("versions %d then %d, want %d twice", v1, v2, len(migrations))
	}
	sc, _ := s2.Scoring()
	for d, p := range DefaultScoring {
		if sc[d] != p {
			t.Errorf("scoring[%d] = %d, want %d", d, sc[d], p)
		}
	}
	if len(sc) != 6 {
		t.Errorf("scoring has %d rows after two migrations, want 6", len(sc))
	}
}

func TestMergeRules(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1")

	// First report: participant and solves are created.
	pid, err := s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", map[string]*time.Time{
		"scoreBoardChallenge": at(1),
	}), t0.Add(time.Hour))
	if err != nil || pid == 0 {
		t.Fatalf("merge: %d %v", pid, err)
	}
	board, _ := s.Leaderboard(t0.Add(time.Hour))
	if len(board) != 1 || board[0].Score != 100 || board[0].Solved != 1 || board[0].Total != 3 {
		t.Fatalf("board after first report: %+v", board)
	}

	// Same report again: nothing changes.
	pid2, _ := s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", map[string]*time.Time{
		"scoreBoardChallenge": at(1),
	}), t0.Add(time.Hour))
	if pid2 != pid {
		t.Error("the same registration became a second participant")
	}

	// An EARLIER time for an existing solve does not replace it; a missing
	// solve is not removed; a new solve is added.
	s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", map[string]*time.Time{
		"scoreBoardChallenge": at(-30),
	}), t0.Add(time.Hour))
	s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", map[string]*time.Time{
		"loginAdminChallenge": at(5),
	}), t0.Add(time.Hour))
	d, err := s.ParticipantDetail(pid, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Solves) != 2 || d.Row.Score != 350 {
		t.Fatalf("solves %+v score %d, want 2 solves and 350", d.Solves, d.Row.Score)
	}
	for _, sv := range d.Solves {
		if sv.Challenge == "Score Board" && !sv.SolvedAt.Equal(*at(1)) {
			t.Errorf("first recorded time was replaced: %v", sv.SolvedAt)
		}
	}

	// A disabled challenge does not count even if reported solved.
	s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", map[string]*time.Time{
		"xxeChallenge": at(6),
	}), t0.Add(time.Hour))
	board, _ = s.Leaderboard(t0.Add(time.Hour))
	if board[0].Score != 350 || board[0].Solved != 2 {
		t.Errorf("disabled challenge counted: %+v", board[0])
	}

	// A new registration id ends the old participant and starts at zero.
	pid3, _ := s.MergeReport("cs-1", report("cs-1", regID('b'), "Second", nil), t0.Add(2*time.Hour))
	if pid3 == pid {
		t.Fatal("new registration id did not create a new participant")
	}
	board, _ = s.Leaderboard(t0.Add(2 * time.Hour))
	if len(board) != 1 || board[0].Name != "Second" || board[0].Score != 0 {
		t.Errorf("board after re-registration: %+v", board)
	}
	all, _ := s.AllParticipants(t0.Add(2 * time.Hour))
	if len(all) != 2 {
		t.Errorf("all participants: %d, want 2", len(all))
	}
}

func TestFutureSolveIsClamped(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1")
	r := report("cs-1", regID('a'), "Sanne", map[string]*time.Time{"scoreBoardChallenge": at(24 * 60)})
	received := t0.Add(time.Hour + time.Second)
	pid, _ := s.MergeReport("cs-1", r, received)
	d, _ := s.ParticipantDetail(pid, received)
	if !d.Solves[0].SolvedAt.Equal(received.Truncate(time.Millisecond)) {
		t.Errorf("future solve stored as %v, want %v", d.Solves[0].SolvedAt, received)
	}
}

func TestCatalogueIsUpserted(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1")
	r := report("cs-1", "", "", nil)
	s.MergeReport("cs-1", r, t0)
	r.Challenges[0].Name = "Score Board (renamed)"
	r.Challenges[0].Difficulty = 2
	s.MergeReport("cs-1", r, t0)
	var name string
	var diff int
	s.db.QueryRow(`SELECT name, difficulty FROM challenges WHERE key = 'scoreBoardChallenge'`).Scan(&name, &diff)
	if name != "Score Board (renamed)" || diff != 2 {
		t.Errorf("catalogue not updated: %q %d", name, diff)
	}
}

func TestRankingTiesAndScoringTable(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1", "cs-2", "cs-3", "cs-4")
	now := t0.Add(time.Hour)
	// Three with the same score, reached at different times; one who reached
	// it at the same moment as the first.
	s.MergeReport("cs-1", report("cs-1", regID('a'), "Late", map[string]*time.Time{"scoreBoardChallenge": at(30)}), now)
	s.MergeReport("cs-2", report("cs-2", regID('b'), "Early", map[string]*time.Time{"scoreBoardChallenge": at(10)}), now)
	s.MergeReport("cs-3", report("cs-3", regID('c'), "Middle", map[string]*time.Time{"scoreBoardChallenge": at(20)}), now)
	s.MergeReport("cs-4", report("cs-4", regID('d'), "Also Early", map[string]*time.Time{"scoreBoardChallenge": at(10)}), now)

	board, _ := s.Leaderboard(now)
	var got []string
	var ranks []int
	for _, r := range board {
		got = append(got, r.Name)
		ranks = append(ranks, r.Rank)
	}
	if strings.Join(got, ",") != "Also Early,Early,Middle,Late" {
		t.Errorf("order = %v", got)
	}
	if ranks[0] != 1 || ranks[1] != 1 || ranks[2] != 3 || ranks[3] != 4 {
		t.Errorf("ranks = %v, want [1 1 3 4]", ranks)
	}
	st, _ := s.Standing(board[2].ParticipantID, now)
	if st == nil || st.Rank != 3 || st.Of != 4 || st.Score != 100 {
		t.Errorf("standing = %+v", st)
	}

	s.SetScoring(1, 10)
	board, _ = s.Leaderboard(now)
	if board[0].Score != 10 {
		t.Errorf("changing the scoring table did not change the score: %d", board[0].Score)
	}
}

func TestHiddenAndEndedAreNotOnTheBoard(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1", "cs-2")
	now := t0.Add(time.Hour)
	p1, _ := s.MergeReport("cs-1", report("cs-1", regID('a'), "Hidden", map[string]*time.Time{"scoreBoardChallenge": at(1)}), now)
	p2, _ := s.MergeReport("cs-2", report("cs-2", regID('b'), "Reset", map[string]*time.Time{"scoreBoardChallenge": at(2)}), now)
	s.SetHidden(p1, true)
	if err := s.ResetParticipant(p2, now); err != nil {
		t.Fatal(err)
	}
	board, _ := s.Leaderboard(now)
	if len(board) != 0 {
		t.Errorf("board = %+v, want empty", board)
	}
	recent, _ := s.Recent(10)
	if len(recent) != 0 {
		t.Errorf("recent shows hidden/ended: %+v", recent)
	}
	if _, err := s.ParticipantDetail(p1, now); err != ErrNotFound {
		t.Error("a hidden participant's page must not be public")
	}
	if st, _ := s.Standing(p1, now); st != nil {
		t.Error("a hidden participant has a standing")
	}
}

func TestResetQueuesOneCommandUntilConfirmed(t *testing.T) {
	s, _ := open(t)
	mustSeed(t, s, "cs-1")
	now := t0.Add(time.Hour)
	pid, _ := s.MergeReport("cs-1", report("cs-1", regID('a'), "Sanne", nil), now)
	s.ResetParticipant(pid, now)
	s.ResetParticipant(pid, now) // twice
	cmds, _ := s.PendingCommands("cs-1", now)
	if len(cmds) != 1 || cmds[0].Type != protocol.CmdResetRegistration {
		t.Fatalf("pending = %+v, want one reset", cmds)
	}
	again, _ := s.PendingCommands("cs-1", now)
	if len(again) != 1 || again[0].ID != cmds[0].ID {
		t.Error("an unconfirmed command must be sent again")
	}
	r := report("cs-1", "", "", nil)
	r.CommandsApplied = []string{cmds[0].ID}
	s.MergeReport("cs-1", r, now)
	if left, _ := s.PendingCommands("cs-1", now); len(left) != 0 {
		t.Errorf("confirmed command still pending: %+v", left)
	}
}

func TestTokens(t *testing.T) {
	s, _ := open(t)
	tok, err := s.CreateInstance("cs-1", "VPS 1", t0)
	if err != nil || !strings.HasPrefix(tok, "ctf_") {
		t.Fatalf("create: %q %v", tok, err)
	}
	if _, err := s.CreateInstance("cs-1", "", t0); err != ErrExists {
		t.Errorf("duplicate: %v, want ErrExists", err)
	}
	if _, err := s.CreateInstance("Bad ID", "", t0); err == nil {
		t.Error("invalid id accepted")
	}
	if id, err := s.InstanceForToken(tok); err != nil || id != "cs-1" {
		t.Errorf("lookup: %q %v", id, err)
	}
	var stored string
	s.db.QueryRow(`SELECT token_hash FROM instances WHERE id = 'cs-1'`).Scan(&stored)
	if strings.Contains(stored, tok) || stored == tok {
		t.Error("the token is stored in the clear")
	}
	tok2, _ := s.RotateToken("cs-1")
	if _, err := s.InstanceForToken(tok); err != ErrNotFound {
		t.Error("the old token still works after rotation")
	}
	if id, _ := s.InstanceForToken(tok2); id != "cs-1" {
		t.Error("the new token does not work")
	}
	if err := s.SeedInstance("cs-1", "", "short", t0); err == nil {
		t.Error("a short seed token was accepted")
	}
	if err := s.SeedInstance("cs-2", "", "a-seed-token-long-enough", t0); err != nil {
		t.Fatal(err)
	}
	if err := s.SeedInstance("cs-2", "", "a-seed-token-long-enough", t0); err != nil {
		t.Errorf("seed is not idempotent: %v", err)
	}
}
