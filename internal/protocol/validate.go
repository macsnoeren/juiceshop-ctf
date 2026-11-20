// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

package protocol

import (
	"errors"
	"fmt"
	"math"
	"regexp"

	"github.com/macsnoeren/juiceshop-ctf/internal/names"
)

// ErrInvalid wraps every validation failure. The message after it is safe to
// send back to the node and to log: it never repeats a token, a code or a
// name.
var ErrInvalid = errors.New("invalid request")

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, a...))
}

var (
	nodeUIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
	keyRE     = regexp.MustCompile(`^[A-Za-z0-9_]{1,80}$`)
	commandRE = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

// ValidNodeUID reports whether uid looks like one a node makes.
func ValidNodeUID(uid string) bool { return nodeUIDRE.MatchString(uid) }

// NewNodeUID returns a fresh node uid.
func NewNodeUID() string { return randomHex(16) }

func checkProtocol(v int) error {
	if v != Version {
		return invalid("protocol %d is not supported (want %d)", v, Version)
	}
	return nil
}

func (n Node) validate() error {
	if !ValidNodeUID(n.UID) {
		return invalid("node uid")
	}
	if len(n.Version) > 64 || len(n.JuiceShop) > 64 {
		return invalid("version string too long")
	}
	return nil
}

// Normalize checks a join and puts its name and code in their stored form,
// in place. The name follows 1.0's rule (names.Normalize); the code is
// lower-cased with spaces and dashes removed, so "4FFP-Q7TQ9F" is accepted.
func (j *JoinRequest) Normalize() error {
	if err := checkProtocol(j.Protocol); err != nil {
		return err
	}
	n, err := names.Normalize(j.Name)
	if err != nil {
		return invalid("name: %v", err)
	}
	j.Name = n
	c, ok := NormalizeJoinCode(j.Code)
	if !ok {
		return invalid("a join code is %d letters and digits", JoinCodeLength)
	}
	j.Code = c
	if j.JoinedAt != nil && j.JoinedAt.IsZero() {
		j.JoinedAt = nil
	}
	return j.Node.validate()
}

// Validate checks a report as the central server receives it.
func (r *Report) Validate() error {
	if err := checkProtocol(r.Protocol); err != nil {
		return err
	}
	if r.SentAt.IsZero() {
		return invalid("sent_at missing")
	}
	if len(r.Boot) > 64 {
		return invalid("boot too long")
	}
	if err := r.Node.validate(); err != nil {
		return err
	}
	if len(r.Solves) > MaxSolves {
		return invalid("%d solves, at most %d", len(r.Solves), MaxSolves)
	}
	seen := make(map[string]bool, len(r.Solves))
	for i, s := range r.Solves {
		if !keyRE.MatchString(s.Key) {
			return invalid("solve %d: key", i)
		}
		if seen[s.Key] {
			return invalid("solve %q listed twice", s.Key)
		}
		seen[s.Key] = true
		if s.SolvedAt.IsZero() {
			return invalid("solve %q: solved_at missing", s.Key)
		}
		if c := s.Cheat; c != nil && (math.IsNaN(*c) || *c < 0 || *c > 1) {
			return invalid("solve %q: cheat score must be 0 to 1", s.Key)
		}
	}
	if len(r.Coding) > MaxCoding {
		return invalid("%d coding phases, at most %d", len(r.Coding), MaxCoding)
	}
	seenPhase := make(map[CodingSolve]bool, len(r.Coding))
	for i, c := range r.Coding {
		if !keyRE.MatchString(c.Key) {
			return invalid("coding %d: key", i)
		}
		if c.Phase != PhaseFindIt && c.Phase != PhaseFixIt {
			return invalid("coding %q: phase must be %q or %q", c.Key, PhaseFindIt, PhaseFixIt)
		}
		k := CodingSolve{Key: c.Key, Phase: c.Phase}
		if seenPhase[k] {
			return invalid("coding %q %s listed twice", c.Key, c.Phase)
		}
		seenPhase[k] = true
		if c.SolvedAt.IsZero() {
			return invalid("coding %q %s: solved_at missing", c.Key, c.Phase)
		}
	}
	if len(r.CommandsApplied) > MaxCommandsApplied {
		return invalid("too many commands_applied")
	}
	for _, id := range r.CommandsApplied {
		if !commandRE.MatchString(id) {
			return invalid("command id")
		}
	}
	return nil
}

// Validate checks a leave.
func (l *LeaveRequest) Validate() error {
	if err := checkProtocol(l.Protocol); err != nil {
		return err
	}
	if l.Reason != LeaveForget && l.Reason != LeaveCommand {
		return invalid("reason must be %q or %q", LeaveForget, LeaveCommand)
	}
	return nil
}
