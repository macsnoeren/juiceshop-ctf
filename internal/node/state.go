// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package node is ctf-node: the CTF half of the juiceshop-ctf container,
// in one program:
//
//   - the student's page at /ctf/: join, progress, rank, the two buttons
//     (page.go);
//   - the reverse proxy to Juice Shop at /juiceshop/ and /assets/, which
//     refuses Juice Shop's continue-code apply (proxy.go);
//   - the watcher: it polls Juice Shop, restores a restarted one from the
//     continue codes and records first-seen solve times (watcher.go);
//   - the reporter: join, report and leave against the central server, with
//     an outbox when it is unreachable (central.go);
//   - node.db: everything above remembers in SQLite (store.go).
//
// It runs as uid 10001 with no capabilities, started by the keeper
// (internal/node/keeper), which alone may start and stop Juice Shop.
package node

import (
	"time"

	"github.com/macsnoeren/juiceshop-ctf/internal/juiceshop"
	"github.com/macsnoeren/juiceshop-ctf/internal/protocol"
)

// The statuses of a membership.
const (
	// StatusLocal: no central server is configured; the student gave a name
	// and plays on their own.
	StatusLocal = "local"
	// StatusPending: the join waits in the outbox for the central server.
	StatusPending = "pending"
	// StatusJoined: the central server issued a token.
	StatusJoined = "joined"
	// StatusRefused: the central server refused the queued join (unknown
	// code, game closed). The page asks for the code again; the solves stay.
	StatusRefused = "refused"
	// StatusRejected: the central server no longer accepts the token (401).
	// The page asks to join again; the solves stay.
	StatusRejected = "rejected"
)

// Membership is the student on this VM. There is at most one.
type Membership struct {
	Status   string
	Name     string
	NodeUID  string // made at the first join, never at boot
	JoinedAt time.Time
	// CentralURL is the server this membership belongs to: a token is only
	// good there, so changing the server needs Forget first.
	CentralURL    string
	ParticipantID int64
	Token         string
	Game          protocol.Game
	BoardURL      string
	Problem       string
	// ReturnCodeHash is the optional gate's personal code (CTF_GATE=session).
	ReturnCodeHash string
}

// Outbox is a join waiting for the central server. It is the ONLY
// place the join code is ever stored, and only until the join is answered.
type Outbox struct {
	Name      string
	Code      string
	JoinedAt  time.Time
	Attempts  int
	NextAt    time.Time
	LastError string
}

// Standing is the last answer of the central server about this student.
type Standing struct {
	Rank, Of, Score int
	At              time.Time
}

// CodingKey is one phase (protocol.PhaseFindIt or PhaseFixIt) of the coding
// challenge of one hacking challenge.
type CodingKey struct {
	Key   string
	Phase string
}

// State is everything the node remembers about the student.
type State struct {
	Membership *Membership
	Outbox     *Outbox
	// Solved is the first time each challenge was seen solved. A time is
	// never moved and a key never removed, except by Forget.
	Solved map[string]time.Time
	// Cheat is Juice Shop's own cheat estimate, from its webhook.
	Cheat map[string]float64
	// Coding is the first time each coding-challenge phase was seen done,
	// kept by the same rules as Solved.
	Coding        map[CodingKey]time.Time
	ContinueCodes juiceshop.Codes
	// Baseline and CodingBaseline are what Juice Shop had done when the
	// student joined; they never count, and apply only to BaselineBoot, the
	// boot they were taken in.
	Baseline        []string
	CodingBaseline  []CodingKey
	BaselineBoot    string
	RestoreFailed   bool
	CommandsApplied []string
	Standing        *Standing
	LastReportAt    *time.Time
}

func newState() *State {
	return &State{Solved: map[string]time.Time{}, Cheat: map[string]float64{}, Coding: map[CodingKey]time.Time{}}
}

func (s *State) clone() *State {
	cp := *s
	if s.Membership != nil {
		m := *s.Membership
		cp.Membership = &m
	}
	if s.Outbox != nil {
		o := *s.Outbox
		cp.Outbox = &o
	}
	if s.Standing != nil {
		st := *s.Standing
		cp.Standing = &st
	}
	cp.Solved = make(map[string]time.Time, len(s.Solved))
	for k, v := range s.Solved {
		cp.Solved[k] = v
	}
	cp.Cheat = make(map[string]float64, len(s.Cheat))
	for k, v := range s.Cheat {
		cp.Cheat[k] = v
	}
	cp.Coding = make(map[CodingKey]time.Time, len(s.Coding))
	for k, v := range s.Coding {
		cp.Coding[k] = v
	}
	cp.Baseline = append([]string(nil), s.Baseline...)
	cp.CodingBaseline = append([]CodingKey(nil), s.CodingBaseline...)
	cp.CommandsApplied = append([]string(nil), s.CommandsApplied...)
	return &cp
}

// recording reports whether new solves belong to someone: there is a student,
// and their game has not ended. A node keeps quiet about solves after the end
// — an honest one never sends them.
func (s *State) recording() bool {
	m := s.Membership
	return m != nil && m.Game.State != protocol.StateClosed && m.Game.State != protocol.StateLeft
}
