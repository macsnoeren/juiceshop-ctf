// SPDX-License-Identifier: GPL-3.0-or-later
// Copyright (C) 2026 Maurice Snoeren

// Package names holds the one rule for a participant's display name.
//
// The name is shown on a public leaderboard and a beamer, so it is restricted
// to what a person's name needs: letters (any script), digits, spaces and
// . - ' _. Everything else, including every character that means something in
// HTML, is refused rather than escaped, so a name never has to be trusted by a
// template to be safe. The templates escape anyway.
//
// A name is also in ONE SCRIPT (Latin, Cyrillic, Greek, …; Japanese and
// Korean may combine theirs with Han, as they are written): "Аnna" with a
// Cyrillic А looks exactly like "Anna" on the board, and would let one
// student pose as another. A whole name in a look-alike script is still
// possible, and rare; the operator can hide a participant.
//
// The status page normalises what the student typed; the protocol validation
// checks that a name in a report is ALREADY normalised (Normalize(n) == n), so
// the central server never stores two spellings of the same name.
package names

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

const (
	MinLen = 2
	MaxLen = 40
)

var (
	ErrLength = errors.New("a name must be 2 to 40 characters")
	ErrChars  = errors.New("a name may only contain letters, digits, spaces and . - ' _")
	ErrMixed  = errors.New("a name is written in one script: no Latin and Cyrillic (or Greek) letters together")
)

// together are the scripts that are written together in one name.
var together = map[string]string{
	"Han": "CJK", "Hiragana": "CJK", "Katakana": "CJK", "Hangul": "CJK", "Bopomofo": "CJK",
}

// scriptOf is the script of a letter, "" for Common and Inherited (digits,
// combining marks), which go with any script.
func scriptOf(r rune) string {
	for name, t := range unicode.Scripts {
		if name == "Common" || name == "Inherited" {
			continue
		}
		if unicode.Is(t, r) {
			if g, ok := together[name]; ok {
				return g
			}
			return name
		}
	}
	return ""
}

// Normalize trims, collapses runs of white space to one space, converts to NFC
// and checks the length and the characters. It returns the name as it will be
// stored and shown.
func Normalize(s string) (string, error) {
	s = norm.NFC.String(strings.Join(strings.Fields(s), " "))
	n := utf8.RuneCountInString(s)
	if n < MinLen || n > MaxLen {
		return "", ErrLength
	}
	script := ""
	for _, r := range s {
		switch {
		case unicode.IsLetter(r), unicode.IsDigit(r), unicode.IsMark(r):
			// IsMark: some scripts keep combining marks even after NFC.
		case r == ' ', r == '.', r == '-', r == '\'', r == '_':
		default:
			return "", ErrChars
		}
		if r < 0x80 && !unicode.IsLetter(r) {
			continue
		}
		switch sc := scriptOf(r); {
		case sc == "":
		case script == "":
			script = sc
		case sc != script:
			return "", ErrMixed
		}
	}
	return s, nil
}
