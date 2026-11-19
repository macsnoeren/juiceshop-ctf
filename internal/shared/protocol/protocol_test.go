package protocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"math"
	"math/rand/v2"
	"strings"
	"testing"
	"time"
)

// The examples of VERSION-1-1.md §2.2 and docs/PROTOCOL.md, as the programs
// write them. If one of these stops round-tripping, a field was renamed, and
// every VM in the field still sends the old name.
var examples = map[string]struct {
	json string
	into any
}{
	"join":          {`{"protocol":2,"name":"Zoë","code":"4ffpq7tq9f","joined_at":"2026-11-19T09:00:00Z","node":{"uid":"0123456789abcdef0123456789abcdef","version":"v1.1.0","juiceshop":"20.3.0"}}`, &JoinRequest{}},
	"join-answer":   {`{"participant":41,"token":"ctfp_00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff","name":"Zoë","game":{"id":7,"name":"Class 2A","slug":"class-2a","state":"running","ends_at":"2026-11-19T11:00:00Z"},"board":"https://neo.jmnl.nl/juiceshop-ctf/g/class-2a/"}`, &JoinResponse{}},
	"error":         {`{"error":"unknown join code"}`, &ErrorResponse{}},
	"report":        {`{"protocol":2,"sent_at":"2026-11-19T09:30:00Z","boot":"2026-11-19T08:59:12.431Z","node":{"uid":"0123456789abcdef0123456789abcdef","version":"v1.1.0","juiceshop":"20.3.0"},"juiceshop_reachable":true,"restore_failed":false,"solves":[{"key":"scoreBoardChallenge","solved_at":"2026-11-19T09:05:00Z","cheat":0.1},{"key":"directoryListingChallenge","solved_at":"2026-11-19T09:20:00Z"}],"commands_applied":["c-1a2b3c"]}`, &Report{}},
	"report-answer": {`{"standing":{"rank":2,"of":18,"score":1350},"game":{"state":"running","ends_at":"2026-11-19T11:00:00Z"},"commands":[{"id":"c-1a2b3c","type":"forget"}]}`, &ReportResponse{}},
	"leave":         {`{"protocol":2,"reason":"forget"}`, &LeaveRequest{}},
}

func TestExamplesRoundTrip(t *testing.T) {
	for name, ex := range examples {
		if err := json.Unmarshal([]byte(ex.json), ex.into); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		out, err := json.Marshal(ex.into)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out, []byte(ex.json)) {
			t.Errorf("%s does not round-trip:\n got %s\nwant %s", name, out, ex.json)
		}
	}
}

func validReport() Report {
	var r Report
	if err := json.Unmarshal([]byte(examples["report"].json), &r); err != nil {
		panic(err)
	}
	return r
}

func validJoin() JoinRequest {
	var j JoinRequest
	if err := json.Unmarshal([]byte(examples["join"].json), &j); err != nil {
		panic(err)
	}
	return j
}

func TestExamplesAreValid(t *testing.T) {
	r := validReport()
	if err := r.Validate(); err != nil {
		t.Error("report:", err)
	}
	j := validJoin()
	if err := j.Normalize(); err != nil {
		t.Error("join:", err)
	}
	l := LeaveRequest{Protocol: 2, Reason: LeaveForget}
	if err := l.Validate(); err != nil {
		t.Error("leave:", err)
	}
}

func TestReportValidation(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	cases := []struct {
		name string
		edit func(*Report)
	}{
		{"protocol 1", func(r *Report) { r.Protocol = 1 }},
		{"no sent_at", func(r *Report) { r.SentAt = time.Time{} }},
		{"node uid", func(r *Report) { r.Node.UID = "cybersecurity-1" }},
		{"long version", func(r *Report) { r.Node.Version = strings.Repeat("v", 65) }},
		{"long boot", func(r *Report) { r.Boot = strings.Repeat("b", 65) }},
		{"empty key", func(r *Report) { r.Solves[0].Key = "" }},
		{"key with a quote", func(r *Report) { r.Solves[0].Key = `a"b` }},
		{"duplicate key", func(r *Report) { r.Solves[1].Key = r.Solves[0].Key }},
		{"no solved_at", func(r *Report) { r.Solves[0].SolvedAt = time.Time{} }},
		{"cheat above 1", func(r *Report) { r.Solves[0].Cheat = f(1.5) }},
		{"cheat below 0", func(r *Report) { r.Solves[0].Cheat = f(-0.1) }},
		{"cheat NaN", func(r *Report) { r.Solves[0].Cheat = f(math.NaN()) }},
		{"501 solves", func(r *Report) {
			r.Solves = nil
			for i := range MaxSolves + 1 {
				r.Solves = append(r.Solves, Solve{Key: "k" + strings.Repeat("x", i%70) + string(rune('a'+i%26)) + itoa(i), SolvedAt: r.SentAt})
			}
		}},
		{"command id", func(r *Report) { r.CommandsApplied = []string{"bad id!"} }},
		{"too many commands", func(r *Report) { r.CommandsApplied = make([]string, MaxCommandsApplied+1) }},
	}
	for _, c := range cases {
		r := validReport()
		c.edit(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", c.name, err)
		}
	}
	r := validReport()
	r.Solves = append(r.Solves, Solve{Key: "edgeCheat", SolvedAt: r.SentAt, Cheat: f(1)})
	if err := r.Validate(); err != nil {
		t.Errorf("a cheat score of exactly 1 is valid: %v", err)
	}
}

func itoa(i int) string {
	const d = "0123456789"
	if i < 10 {
		return d[i : i+1]
	}
	return itoa(i/10) + d[i%10:i%10+1]
}

func TestJoinValidation(t *testing.T) {
	cases := []struct {
		name string
		edit func(*JoinRequest)
	}{
		{"protocol 1", func(j *JoinRequest) { j.Protocol = 1 }},
		{"name too short", func(j *JoinRequest) { j.Name = "Z" }},
		{"name with markup", func(j *JoinRequest) { j.Name = "<b>Zoë</b>" }},
		{"code too short", func(j *JoinRequest) { j.Code = "4ffpq7tq9" }},
		{"code too long", func(j *JoinRequest) { j.Code = "4ffpq7tq9fa" }},
		{"code with an o", func(j *JoinRequest) { j.Code = "offpq7tq9f" }},
		{"code with a 1", func(j *JoinRequest) { j.Code = "1ffpq7tq9f" }},
		{"node uid", func(j *JoinRequest) { j.Node.UID = "" }},
	}
	for _, c := range cases {
		j := validJoin()
		c.edit(&j)
		if err := j.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: got %v, want ErrInvalid", c.name, err)
		}
	}
	j := validJoin()
	j.Name, j.Code = "  Zoë   de  Vries ", "4FFPQ-7TQ9F"
	if err := j.Normalize(); err != nil {
		t.Fatal(err)
	}
	if j.Name != "Zoë de Vries" || j.Code != "4ffpq7tq9f" {
		t.Errorf("normalised to %q / %q", j.Name, j.Code)
	}
}

func TestLeaveValidation(t *testing.T) {
	for _, l := range []LeaveRequest{{Protocol: 1, Reason: LeaveForget}, {Protocol: 2, Reason: "bored"}, {Protocol: 2}} {
		if err := l.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v: got %v", l, err)
		}
	}
}

// TestJoinCodesAreUnbiased draws 10 000 codes from a SEEDED source, so the
// test is deterministic: it checks the mapping from bytes to characters,
// which is where a bias would come from, not the quality of crypto/rand.
func TestJoinCodesAreUnbiased(t *testing.T) {
	src := rand.NewChaCha8([32]byte{1, 2, 3})
	counts := map[rune]int{}
	total := 0
	for range 10000 {
		c := randomCode(src, JoinCodeLength)
		if _, ok := NormalizeJoinCode(c); !ok {
			t.Fatalf("generated code %q is not a valid code", c)
		}
		for _, r := range c {
			counts[r]++
			total++
		}
	}
	if len(counts) != len(JoinCodeAlphabet) {
		t.Fatalf("%d distinct characters, want %d", len(counts), len(JoinCodeAlphabet))
	}
	want := float64(total) / float64(len(JoinCodeAlphabet))
	for r, n := range counts {
		if !strings.ContainsRune(JoinCodeAlphabet, r) {
			t.Errorf("character %q is not in the alphabet", r)
		}
		if dev := math.Abs(float64(n)-want) / want; dev > 0.05 {
			t.Errorf("%q drawn %d times, %.1f %% off the expected %.0f", r, n, dev*100, want)
		}
	}
}

// TestModuloMappingWouldBeCaught shows the test above is not vacuous: the
// naive mapping b % 31 fails it.
func TestModuloMappingWouldBeCaught(t *testing.T) {
	src := rand.NewChaCha8([32]byte{1, 2, 3})
	counts := map[byte]int{}
	buf := make([]byte, 100000)
	src.Read(buf)
	for _, b := range buf {
		counts[JoinCodeAlphabet[int(b)%31]]++
	}
	want := float64(len(buf)) / 31
	worst := 0.0
	for _, n := range counts {
		worst = max(worst, math.Abs(float64(n)-want)/want)
	}
	if worst <= 0.05 {
		t.Errorf("the biased mapping was only %.1f %% off; the check would not catch it", worst*100)
	}
}

func TestTokensAndHashes(t *testing.T) {
	tok := NewParticipantToken()
	if !ValidParticipantToken(tok) {
		t.Fatalf("token %q has the wrong shape", tok)
	}
	if ValidParticipantToken("ctf_"+tok[5:]) || ValidParticipantToken(tok+"0") {
		t.Error("a token of the wrong shape was accepted")
	}
	for _, s := range []string{tok, NewJoinCode(), NewReturnCode(), "x"} {
		h := Hash(s)
		if h == s || len(h) != 64 {
			t.Errorf("hash of %q is %q", s, h)
		}
		if !HashMatches(s, h) || HashMatches(s+"x", h) || HashMatches("", Hash("")) {
			t.Errorf("HashMatches is wrong for %q", s)
		}
	}
	if NewParticipantToken() == tok {
		t.Error("two tokens are equal")
	}
	if got := FormatCode("4ffpq7tq9f"); got != "4ffpq-7tq9f" {
		t.Errorf("FormatCode = %q", got)
	}
	if c, ok := NormalizeJoinCode(FormatCode("4ffpq7tq9f")); !ok || c != "4ffpq7tq9f" {
		t.Error("a formatted code does not normalise back")
	}
	if !ValidNodeUID(NewNodeUID()) {
		t.Error("NewNodeUID makes invalid uids")
	}
}
