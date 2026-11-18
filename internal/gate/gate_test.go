package gate

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var (
	secret = []byte("0123456789abcdef0123456789abcdef")
	now    = time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC)
	id     = "9f2c4e1a0b3d5f6e7a8b9c0d1e2f3a4b"
)

func TestValid(t *testing.T) {
	c := Sign(id, now.Add(Lifetime), secret)
	got, err := Verify(c, secret, now)
	if err != nil || got != id {
		t.Fatalf("Verify = %q, %v", got, err)
	}
}

func TestRejects(t *testing.T) {
	good := Sign(id, now.Add(time.Hour), secret)
	parts := strings.Split(good, ".")
	tests := []struct {
		name   string
		cookie string
		secret []byte
		now    time.Time
		want   error
	}{
		{"expired", good, secret, now.Add(2 * time.Hour), ErrExpired},
		{"expiry exactly now", good, secret, now.Add(time.Hour), ErrExpired},
		{"changed id", "aaaa" + good[4:], secret, now, ErrSignature},
		{"changed expiry", parts[0] + "." + "9999999999" + "." + parts[2], secret, now, ErrSignature},
		{"truncated", good[:len(good)-3], secret, now, ErrSignature},
		{"other secret", good, []byte("another secret, same length....."), now, ErrSignature},
		{"empty", "", secret, now, ErrMalformed},
		{"two parts", parts[0] + "." + parts[1], secret, now, ErrMalformed},
		{"bad base64", parts[0] + "." + parts[1] + ".!!!", secret, now, ErrMalformed},
		{"not a number", parts[0] + ".abc." + parts[2], secret, now, ErrMalformed},
		{"too long", strings.Repeat("a", 300), secret, now, ErrMalformed},
	}
	for _, tt := range tests {
		if _, err := Verify(tt.cookie, tt.secret, tt.now); !errors.Is(err, tt.want) {
			t.Errorf("%s: err = %v, want %v", tt.name, err, tt.want)
		}
	}
}

func FuzzVerify(f *testing.F) {
	f.Add(Sign(id, now.Add(time.Hour), secret))
	f.Add("a.b.c")
	f.Add("..")
	f.Fuzz(func(t *testing.T, cookie string) {
		got, err := Verify(cookie, secret, now)
		if err == nil && got == "" {
			t.Fatal("accepted a cookie with an empty id")
		}
	})
}
