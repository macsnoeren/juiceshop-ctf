package protocolv1

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// exampleReport is the example of DESIGN.md §7.2. If this test fails after a
// change to the format, update the design document in the same commit.
const exampleReport = `{
  "protocol": 1,
  "instance": "cybersecurity-1",
  "sent_at": "2026-11-18T09:15:00Z",
  "agent_version": "0.1.0",
  "juiceshop": {"version": "20.3.0", "reachable": true, "restore_failed": false},
  "registration": {"id": "9f2c4e1a0b3d5f6e7a8b9c0d1e2f3a4b", "name": "Sanne de Vries", "registered_at": "2026-11-18T09:01:12Z"},
  "challenges": [
    {"key": "scoreBoardChallenge", "name": "Score Board", "category": "Miscellaneous",
     "difficulty": 1, "disabled": false, "solved_at": "2026-11-18T09:14:03Z", "cheat": 0.0},
    {"key": "loginAdminChallenge", "name": "Login Admin", "category": "Injection",
     "difficulty": 2, "disabled": false, "solved_at": null}
  ],
  "commands_applied": ["c-17"]
}`

func decode(t *testing.T, s string) Report {
	t.Helper()
	var r Report
	if err := json.Unmarshal([]byte(s), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestExampleRoundTrip(t *testing.T) {
	r := decode(t, exampleReport)
	if err := r.Validate(); err != nil {
		t.Fatalf("example does not validate: %v", err)
	}
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	json.Unmarshal([]byte(exampleReport), &a)
	json.Unmarshal(out, &b)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("round trip differs:\nwant %v\ngot  %v", a, b)
	}
}

func TestNullRegistrationRoundTrip(t *testing.T) {
	r := decode(t, exampleReport)
	r.Registration = nil
	out, _ := json.Marshal(r)
	if !strings.Contains(string(out), `"registration":null`) {
		t.Errorf("registration not encoded as null: %s", out)
	}
	back := decode(t, string(out))
	if back.Registration != nil {
		t.Error("null registration came back non-nil")
	}
	if err := back.Validate(); err != nil {
		t.Errorf("report without registration must validate: %v", err)
	}
}

func TestValidateRejects(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Report)
	}{
		{"unknown protocol", func(r *Report) { r.Protocol = 2 }},
		{"instance with capitals", func(r *Report) { r.Instance = "Cyber-1" }},
		{"instance with slash", func(r *Report) { r.Instance = "a/b" }},
		{"no sent_at", func(r *Report) { r.SentAt = time.Time{} }},
		{"bad registration id", func(r *Report) { r.Registration.ID = "xyz" }},
		{"bad name", func(r *Report) { r.Registration.Name = "<b>" }},
		{"unnormalised name", func(r *Report) { r.Registration.Name = "Sanne  de Vries" }},
		{"no registered_at", func(r *Report) { r.Registration.RegisteredAt = time.Time{} }},
		{"difficulty 0", func(r *Report) { r.Challenges[0].Difficulty = 0 }},
		{"difficulty 7", func(r *Report) { r.Challenges[0].Difficulty = 7 }},
		{"bad key", func(r *Report) { r.Challenges[0].Key = "a b" }},
		{"duplicate key", func(r *Report) { r.Challenges[1].Key = r.Challenges[0].Key }},
		{"empty name", func(r *Report) { r.Challenges[0].Name = "" }},
		{"too many challenges", func(r *Report) {
			c := r.Challenges[0]
			r.Challenges = nil
			for i := 0; i <= MaxChallenges; i++ {
				c.Key = "k" + strings.Repeat("x", i%70) + string(rune('a'+i%26)) + string(rune('a'+i/26%26))
				r.Challenges = append(r.Challenges, c)
			}
		}},
		{"bad command id", func(r *Report) { r.CommandsApplied = []string{"a b"} }},
	}
	for _, tt := range tests {
		r := decode(t, exampleReport)
		tt.mutate(&r)
		if err := r.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: Validate() = %v, want ErrInvalid", tt.name, err)
		}
	}
}

func TestUnknownCommandIsIgnored(t *testing.T) {
	var resp Response
	err := json.Unmarshal([]byte(`{"ok":true,"commands":[{"id":"c-1","type":"reset_registration"},{"id":"c-2","type":"self_destruct"}]}`), &resp)
	if err != nil {
		t.Fatalf("a response with an unknown command must decode: %v", err)
	}
	var known []string
	for _, c := range resp.Commands {
		if c.Known() {
			known = append(known, c.ID)
		}
	}
	if !reflect.DeepEqual(known, []string{"c-1"}) {
		t.Errorf("known commands = %v, want [c-1]", known)
	}
}
