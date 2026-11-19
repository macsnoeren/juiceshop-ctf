package names

import (
	"errors"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := []struct {
		in, want string
		err      error
	}{
		{in: "Zoë", want: "Zoë"},
		{in: "Ağaoğlu", want: "Ağaoğlu"},
		{in: "  Sanne de Vries  ", want: "Sanne de Vries"},
		{in: "Sanne   de\tVries", want: "Sanne de Vries"},
		{in: "O'Neill-Smith_2", want: "O'Neill-Smith_2"},
		{in: "J. Jansen", want: "J. Jansen"},
		{in: "Zoë", want: "Zoë"}, // NFD in, NFC out
		{in: "Ana 🦊", err: ErrChars},
		{in: "<script>", err: ErrChars},
		{in: "a&b", err: ErrChars},
		{in: "x", err: ErrLength},
		{in: "   ", err: ErrLength},
		{in: strings.Repeat("a", 41), err: ErrLength},
		{in: strings.Repeat("a", 40), want: strings.Repeat("a", 40)},
		{in: strings.Repeat("ë", 40), want: strings.Repeat("ë", 40)}, // runes, not bytes
	}
	for _, tt := range tests {
		got, err := Normalize(tt.in)
		if !errors.Is(err, tt.err) {
			t.Errorf("Normalize(%q) error = %v, want %v", tt.in, err, tt.err)
			continue
		}
		if err == nil && got != tt.want {
			t.Errorf("Normalize(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestNormalizeIsIdempotent(t *testing.T) {
	for _, in := range []string{"Zoë van  Dam", "  Ağaoğlu "} {
		once, err := Normalize(in)
		if err != nil {
			t.Fatal(err)
		}
		twice, _ := Normalize(once)
		if once != twice {
			t.Errorf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}
