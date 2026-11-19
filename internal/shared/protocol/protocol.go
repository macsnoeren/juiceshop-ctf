// Package protocol is the report format between ctf-agent and ctf-central
// (DESIGN.md §7.2), version 1.
//
// It is the ONE place this format is defined. Both binaries compile against
// it, so they cannot disagree about a field name. A change here that is not
// backwards compatible must bump Version, and ctf-central must then accept
// both versions until every agent is upgraded: agents on CTF nodes are updated
// by firmware bundle, one node at a time, never all at once.
//
// Every report is a COMPLETE snapshot of one instance, not a delta (§7.1).
// That is what makes a lost or duplicated report harmless, and it is why
// nothing here has a sequence number.
package protocol

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/shared/names"
)

// Version is the protocol version this package speaks.
const Version = 1

// Limits. MaxBodyBytes is enforced by ctf-central before decoding; the others
// by Validate. Juice Shop 20.3 has 116 challenges, so 500 is room for growth,
// not a target.
const (
	MaxBodyBytes       = 256 << 10
	MaxChallenges      = 500
	MaxCommandsApplied = 100
)

// CmdResetRegistration tells the agent to forget its registration (§8.4).
const CmdResetRegistration = "reset_registration"

// Report is what the agent POSTs to /api/v1/report.
type Report struct {
	Protocol     int           `json:"protocol"`
	Instance     string        `json:"instance"`
	SentAt       time.Time     `json:"sent_at"`
	AgentVersion string        `json:"agent_version"`
	JuiceShop    JuiceShop     `json:"juiceshop"`
	Registration *Registration `json:"registration"`
	Challenges   []Challenge   `json:"challenges"`
	// CommandsApplied lists the ids of commands the agent has carried out, so
	// the central server can mark them confirmed (§7.2).
	CommandsApplied []string `json:"commands_applied,omitempty"`
}

// JuiceShop describes the Juice Shop the agent watches.
type JuiceShop struct {
	Version       string `json:"version"`
	Reachable     bool   `json:"reachable"`
	RestoreFailed bool   `json:"restore_failed"`
}

// Registration is the student who signed up on the instance (§5.4).
type Registration struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	RegisteredAt time.Time `json:"registered_at"`
}

// Challenge is one entry of the catalogue, with the participant's solve time.
type Challenge struct {
	Key        string     `json:"key"`
	Name       string     `json:"name"`
	Category   string     `json:"category"`
	Difficulty int        `json:"difficulty"`
	Disabled   bool       `json:"disabled"`
	SolvedAt   *time.Time `json:"solved_at"`
	Cheat      *float64   `json:"cheat,omitempty"`
}

// Response is what ctf-central answers to a report.
type Response struct {
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Commands []Command `json:"commands,omitempty"`
	// Standing is the participant's place on the board, so the status page can
	// show it without the agent knowing the scoring table. Absent when the
	// instance has no registration.
	Standing *Standing `json:"standing,omitempty"`
}

// Command is an instruction from the central server, carried on a response.
type Command struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Standing is a participant's score and rank.
type Standing struct {
	Score int `json:"score"`
	Rank  int `json:"rank"`
	Of    int `json:"of"`
}

// Known reports whether the agent knows how to carry out this command. An
// unknown type is ignored, not an error, so a newer central server can add a
// command without breaking older agents.
func (c Command) Known() bool { return c.Type == CmdResetRegistration }

var (
	instanceRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	regIDRE    = regexp.MustCompile(`^[0-9a-f]{32}$`)
	keyRE      = regexp.MustCompile(`^[A-Za-z0-9_]{1,80}$`)
	commandRE  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidInstanceID reports whether id is an acceptable instance id: lower case
// letters, digits and dashes, starting with a letter or digit, at most 63.
func ValidInstanceID(id string) bool { return instanceRE.MatchString(id) }

// ValidRegistrationID reports whether id looks like one the agent generates.
func ValidRegistrationID(id string) bool { return regIDRE.MatchString(id) }

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("invalid report")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

// Validate checks a report as the central server receives it.
func (r *Report) Validate() error {
	if r.Protocol != Version {
		return invalid("protocol %d is not supported (want %d)", r.Protocol, Version)
	}
	if !ValidInstanceID(r.Instance) {
		return invalid("instance id %q", r.Instance)
	}
	if r.SentAt.IsZero() {
		return invalid("sent_at missing")
	}
	if len(r.AgentVersion) > 64 || len(r.JuiceShop.Version) > 64 {
		return invalid("version string too long")
	}
	if reg := r.Registration; reg != nil {
		if !ValidRegistrationID(reg.ID) {
			return invalid("registration id")
		}
		n, err := names.Normalize(reg.Name)
		if err != nil {
			return invalid("registration name: %v", err)
		}
		if n != reg.Name {
			return invalid("registration name is not normalised")
		}
		if reg.RegisteredAt.IsZero() {
			return invalid("registered_at missing")
		}
	}
	if len(r.Challenges) > MaxChallenges {
		return invalid("%d challenges, at most %d", len(r.Challenges), MaxChallenges)
	}
	seen := make(map[string]bool, len(r.Challenges))
	for i, c := range r.Challenges {
		if !keyRE.MatchString(c.Key) {
			return invalid("challenge %d: key %q", i, c.Key)
		}
		if seen[c.Key] {
			return invalid("challenge %q listed twice", c.Key)
		}
		seen[c.Key] = true
		if c.Name == "" || len(c.Name) > 200 || len(c.Category) > 100 {
			return invalid("challenge %q: name or category", c.Key)
		}
		if c.Difficulty < 1 || c.Difficulty > 6 {
			return invalid("challenge %q: difficulty %d", c.Key, c.Difficulty)
		}
		if c.Cheat != nil && (math.IsNaN(*c.Cheat) || math.IsInf(*c.Cheat, 0)) {
			return invalid("challenge %q: cheat score", c.Key)
		}
	}
	if len(r.CommandsApplied) > MaxCommandsApplied {
		return invalid("too many commands_applied")
	}
	for _, id := range r.CommandsApplied {
		if !commandRE.MatchString(id) {
			return invalid("command id %q", id)
		}
	}
	return nil
}
