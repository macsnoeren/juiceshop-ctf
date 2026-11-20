// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package protocol is the language between ctf-node and ctf-central, version
// 2.
//
// It is the ONE place this format is defined. Both programs compile against
// it, so they cannot disagree about a field name.
//
// Three requests, all from the node, all answered at once, so a VM needs
// nothing but outbound HTTP(S):
//
//	POST /api/v2/join     name + join code       → participant id and TOKEN
//	POST /api/v2/report   Bearer token, snapshot → standing, game, commands
//	POST /api/v2/leave    Bearer token           → 204, the token stops working
//
// Every report is a COMPLETE snapshot of the participant's solves, not a
// delta: a lost or duplicated report is harmless, and a VM that was offline
// for an hour catches up with its next report. That is why nothing here has a
// sequence number.
//
// The central server knows the challenge catalogue itself (it is embedded,
// internal/juiceshop/catalogue.go). A report therefore carries only keys and
// times: a VM the student controls cannot change what a challenge is worth for
// anybody.
package protocol

import "time"

// Version is the protocol version this package speaks. 1.0's agents spoke 1
// on /api/v1/report; the central server answers those with 410 Gone.
const Version = 2

// Limits. MaxBodyBytes is enforced by ctf-central before decoding; the others
// by the Validate methods. Juice Shop 20.3 has 116 challenges, 35 of them
// with a coding challenge (70 phases), so 500 is room for growth, not a
// target.
const (
	MaxBodyBytes       = 256 << 10
	MaxSolves          = 500
	MaxCoding          = 500
	MaxCommandsApplied = 100
)

// The states of a game. StateLeft is never a game's state:
// it is what a report answer says to a participant who is no longer in the
// game, so the node can show that.
const (
	StateDraft   = "draft"
	StateOpen    = "open"
	StateRunning = "running"
	StateClosed  = "closed"
	StateLeft    = "left"
)

// CmdForget tells the node to do what the student's own Forget button does:
// leave, wipe node.db, restart Juice Shop empty.
const CmdForget = "forget"

// The reasons of a leave.
const (
	LeaveForget  = "forget"  // the student pressed Forget
	LeaveCommand = "command" // the node carries out a forget command
)

// Node describes the VM in every join and report.
type Node struct {
	// UID is random, made when the student joins (never at first boot, so an
	// exported VM that never joined carries none): 32 hex characters.
	UID       string `json:"uid"`
	Version   string `json:"version"`
	JuiceShop string `json:"juiceshop"`
}

// JoinRequest is POST /api/v2/join. It carries no token: the join code is
// what admits the node.
type JoinRequest struct {
	Protocol int    `json:"protocol"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	// JoinedAt is when the student pressed join. For a join that waited in
	// the node's outbox it is earlier than the moment
	// the central server receives it, and the solves made in between are
	// already the student's.
	JoinedAt *time.Time `json:"joined_at,omitempty"`
	Node     Node       `json:"node"`
}

// Game is a game as the node sees it.
type Game struct {
	ID       int64      `json:"id,omitempty"`
	Name     string     `json:"name,omitempty"`
	Slug     string     `json:"slug,omitempty"`
	State    string     `json:"state"`
	StartsAt *time.Time `json:"starts_at,omitempty"`
	EndsAt   *time.Time `json:"ends_at,omitempty"`
}

// JoinResponse is the 201 answer to a join. The token is in it ONCE; the
// central server keeps only its hash.
type JoinResponse struct {
	Participant int64  `json:"participant"`
	Token       string `json:"token"`
	// Name is the display name as the central server stored it (normalised).
	Name  string `json:"name"`
	Game  Game   `json:"game"`
	Board string `json:"board"`
}

// ErrorResponse is every non-2xx answer: 400, 401, 403, 409, 410, 413, 429.
type ErrorResponse struct {
	Error string `json:"error"`
}

// Solve is one solved challenge: the first moment the node saw it solved,
// and Juice Shop's own cheat estimate from its webhook, when it sent one.
type Solve struct {
	Key      string    `json:"key"`
	SolvedAt time.Time `json:"solved_at"`
	Cheat    *float64  `json:"cheat,omitempty"`
}

// The two phases of a coding challenge: Juice Shop's "Find it" (pick the
// vulnerable lines) and "Fix it" (pick the right fix).
const (
	PhaseFindIt = "find_it"
	PhaseFixIt  = "fix_it"
)

// PhaseName is a phase as Juice Shop's score board names it.
func PhaseName(phase string) string {
	if phase == PhaseFixIt {
		return "Fix it"
	}
	return "Find it"
}

// CodingSolve is one phase of a coding challenge done: the first moment the
// node saw it. Key is the key of the hacking challenge the code belongs to.
type CodingSolve struct {
	Key      string    `json:"key"`
	Phase    string    `json:"phase"`
	SolvedAt time.Time `json:"solved_at"`
}

// Report is POST /api/v2/report, Authorization: Bearer <participant token>.
type Report struct {
	Protocol int       `json:"protocol"`
	SentAt   time.Time `json:"sent_at"`
	// Boot identifies the running Juice Shop (its start time). A restart
	// gives a new one that never comes back; one token reporting two boots in
	// alternation is a copied VM.
	Boot          string  `json:"boot"`
	Node          Node    `json:"node"`
	Reachable     bool    `json:"juiceshop_reachable"`
	RestoreFailed bool    `json:"restore_failed"`
	Solves        []Solve `json:"solves"`
	// Coding is every coding-challenge phase done since the join. It is a
	// list of its own, not a field of Solve: Juice Shop can be configured
	// to open coding challenges before the hacking challenge is solved.
	Coding          []CodingSolve `json:"coding"`
	CommandsApplied []string      `json:"commands_applied"`
}

// Standing is a participant's place on their game's board.
type Standing struct {
	Rank  int `json:"rank"`
	Of    int `json:"of"`
	Score int `json:"score"`
}

// Command is an instruction from the central server for ONE participant.
type Command struct {
	ID   string `json:"id"`
	Type string `json:"type"`
}

// Known reports whether the node knows how to carry out this command. An
// unknown type is ignored, not an error, so a newer central server can add a
// command without breaking older VMs.
func (c Command) Known() bool { return c.Type == CmdForget }

// ReportResponse is the 200 answer to a report.
type ReportResponse struct {
	// Standing is absent when the participant is not on the board (hidden,
	// left, or a game whose board shows nobody yet).
	Standing *Standing `json:"standing"`
	Game     Game      `json:"game"`
	Commands []Command `json:"commands"`
}

// LeaveRequest is POST /api/v2/leave, Authorization: Bearer <token>. The
// answer is 204, and the token stops working.
type LeaveRequest struct {
	Protocol int    `json:"protocol"`
	Reason   string `json:"reason"`
}
