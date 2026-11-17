package lctl

import (
	"strings"
	"testing"
)

// The header is used as given (not reformatted) and left-aligned; wide
// characters take two columns.
func TestWriteTable(t *testing.T) {
	var b strings.Builder
	if err := writeTable(&b, []string{"ID", "HOST:PORT"}, [][]string{{"a1", "irc.example.com:6697"}, {"b22", "名前:1"}}); err != nil {
		t.Fatal(err)
	}
	want := "┌─────┬──────────────────────┐\n" +
		"│ ID  │ HOST:PORT            │\n" +
		"├─────┼──────────────────────┤\n" +
		"│ a1  │ irc.example.com:6697 │\n" +
		"│ b22 │ 名前:1               │\n" +
		"└─────┴──────────────────────┘\n"
	if b.String() != want {
		t.Errorf("got:\n%s\nwant:\n%s", b.String(), want)
	}
}
