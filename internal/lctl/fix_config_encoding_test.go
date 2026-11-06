package lctl

import (
	"encoding/json"
	"strings"
	"testing"
)

// twice is s encoded to UTF-8 twice, as the Perl API stored configs: each
// byte of the UTF-8 encoding read as a Latin-1 character.
func twice(s string) string {
	var b strings.Builder
	for _, c := range []byte(s) {
		b.WriteRune(rune(c))
	}
	return b.String()
}

func configOf(t *testing.T, h *harness, id string) map[string]any {
	t.Helper()
	var raw string
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT config::text FROM connection WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

func fixSetup(t *testing.T, h *harness) {
	u := h.user("alice")
	bad, _ := json.Marshal(map[string]any{
		"Host": "irc.example.com", "Port": 6697, "Nick": "lee",
		"Highlight": []string{twice("lee🍰"), "lee"},
		"Alias":     twice("réseau"),
	})
	nick, _ := json.Marshal(map[string]any{"Host": "irc.example.com", "Nick": twice("léo"), "Channels": []string{twice("#café")}})
	good, _ := json.Marshal(map[string]any{"Host": "irc.example.com", "Nick": "ann", "Highlight": []string{"ann🍰", "café"}})
	h.conn("c1", u, string(bad))
	h.conn("c2", u, string(nick))
	h.conn("c3", u, string(good))
}

func TestFixConfigEncodingDryRun(t *testing.T) {
	h := newHarness(t)
	fixSetup(t, h)
	if err := h.run("fix-config-encoding"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 (alice)\n", "  Highlight: lee🍰\n", "  Alias: réseau\n",
		"c2 (alice), needs lctl reconnect c2\n", "  Nick: léo\n", "  Channels: #café\n",
		"would fix 2 connection(s)\n", "run again with --fix")
	h.lacks("c3")
	if got := configOf(t, h, "c1")["Alias"]; got != twice("réseau") {
		t.Errorf("dry run changed c1: Alias = %q", got)
	}
}

func TestFixConfigEncodingFix(t *testing.T) {
	h := newHarness(t)
	fixSetup(t, h)
	if err := h.run("fix-config-encoding", "--fix"); err != nil {
		t.Fatal(err)
	}
	c1 := configOf(t, h, "c1")
	if hl := c1["Highlight"].([]any); hl[0] != "lee🍰" || hl[1] != "lee" {
		t.Errorf("c1 Highlight = %q", hl)
	}
	if c1["Alias"] != "réseau" || c1["Host"] != "irc.example.com" || c1["Port"] != float64(6697) {
		t.Errorf("c1 = %v", c1)
	}
	if c2 := configOf(t, h, "c2"); c2["Nick"] != "léo" || c2["Channels"].([]any)[0] != "#café" {
		t.Errorf("c2 = %v", c2)
	}
	if c3 := configOf(t, h, "c3"); c3["Highlight"].([]any)[0] != "ann🍰" || c3["Highlight"].([]any)[1] != "café" {
		t.Errorf("c3 changed: %v", c3)
	}
	h.has("fixed 2 connection(s)\n", "reconnect them now, before liercd's live state is saved over the repair:\n  lctl reconnect c2\n")

	// run again: nothing left to fix
	h.stdout.Reset()
	if err := h.run("fix-config-encoding", "--fix"); err != nil {
		t.Fatal(err)
	}
	h.has("fixed 0 connection(s)\n")
}

// HTML-ish characters stay as written, not < escapes.
func TestFixConfigEncodingKeepsText(t *testing.T) {
	h := newHarness(t)
	u := h.user("alice")
	h.conn("c1", u, `{"Host":"irc.example.com","Pass":"a<b>&c","Highlight":["`+twice("é")+`"]}`)
	if err := h.run("fix-config-encoding", "--fix"); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM connection WHERE id = 'c1' AND config::text LIKE '%a<b>&c%'`); n != 1 {
		t.Error("the password was re-escaped")
	}
	if c := configOf(t, h, "c1"); c["Highlight"].([]any)[0] != "é" {
		t.Errorf("c1 = %v", c)
	}
}

func TestFixConfigEncodingUsage(t *testing.T) {
	h := newHarness(t)
	var u *UsageError
	if err := h.run("fix-config-encoding", "x"); !asUsage(err, &u) {
		t.Errorf("err = %v, want UsageError", err)
	}
}

// A repaired password is named but not printed.
func TestFixConfigEncodingHidesPass(t *testing.T) {
	h := newHarness(t)
	u := h.user("alice")
	h.conn("c1", u, `{"Host":"irc.example.com","Pass":"`+twice("pässwörd")+`"}`)
	if err := h.run("fix-config-encoding"); err != nil {
		t.Fatal(err)
	}
	h.has("  Pass: repaired (not shown)\n")
	h.lacks("pässwörd")
}
