package password

import (
	"strings"
	"testing"
)

func TestSaltPadsByBytes(t *testing.T) {
	salt, err := saltFor(strings.Repeat("é", 8)) // 16 bytes, 8 runes
	if err != nil || len(salt) != 16 {
		t.Fatalf("got %q, %v", salt, err)
	}
	salt, err = saltFor("ab")
	if err != nil || string(salt) != strings.Repeat(" ", 14)+"ab" {
		t.Fatalf("got %q, %v", salt, err)
	}
	if _, err = saltFor(strings.Repeat("é", 9)); err == nil || err.Error() != "Salt must be exactly 16 octets long" {
		t.Fatalf("got %v", err)
	}
}
