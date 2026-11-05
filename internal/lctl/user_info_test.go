package lctl

import (
	"strings"
	"testing"
	"time"
)

func TestUserInfo(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return now }
	u := h.user("Ann")
	h.exec(`UPDATE "user" SET last_login = $2, verified = true, password = NULL, password_hash = '$2a$12$x', registered = $3 WHERE id = $1`,
		u, now.Add(-3*24*time.Hour-2*time.Hour), now.Add(-40*24*time.Hour))
	h.conn("c1", u, `{"Host":"irc.libera.chat","Port":6697,"Nick":"ann","Ssl":true,"SASL":true,"User":"ann","Pass":"hunter2","Alias":"Libera","Channels":["#a","#b"]}`)
	h.conn("c2", u, `{"Host":"irc.relaychat.party","Port":6667,"Nick":"ann_"}`)
	h.conn("c3", u, `"not an object"`)
	h.exec(`UPDATE connection SET liercd='next' WHERE id='c2'`)
	h.exec(`UPDATE connection SET liercd='' WHERE id='c3'`) // '' is main, as NULL
	if _, err := h.env.Liercd.Do(h.t.Context(), "POST", "c1/create", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	h.liercd.SetStatus("c1", `{"Nick":"ann","Status":{"Connected":true}}`)
	if _, err := h.env.Liercd.Do(h.t.Context(), "POST", "c2/create", []byte("{}")); err != nil {
		t.Fatal(err)
	}
	h.liercd.SetStatus("c2", `{"Nick":"ann_","Status":{"Connected":false}}`)

	for _, who := range []string{"ann", "ANN@example.com", u} {
		if err := h.run("user-info", who); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		h.has(
			"Ann ("+u+")\n",
			"  email:       Ann@example.com (verified)\n",
			"  last login:  2026-10-30 10:00 UTC (3 days ago)\n",
			"  registered:  2026-09-23 12:00 UTC (40 days ago)\n",
			"  password:    bcrypt\n",
			"  connections: 3\n",
			"    c1  irc.libera.chat:6697  nick ann  ssl sasl  alias Libera  2 channels  server password set  connected as ann\n      liercd: main\n",
			"    c2  irc.relaychat.party:6667  nick ann_  0 channels  running, not connected\n      liercd: next\n",
			"    c3  config isn't a JSON object  not running\n      liercd: main\n",
		)
		h.lacks("hunter2", "$2a$")
	}
}

func TestUserInfoAges(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		5 * time.Minute: "5 minutes ago", 3 * time.Hour: "3 hours ago", 47 * time.Hour: "47 hours ago",
		50 * time.Hour: "2 days ago", -time.Minute: "in the future",
	} {
		if got := ago(now, now.Add(-d)); got != want {
			t.Errorf("%v: %q, want %q", d, got, want)
		}
	}
}

func TestUserInfoUnknownAndLegacy(t *testing.T) {
	h := newHarness(t)
	u := h.user("bob") // testenv users have a legacy plain password
	h.exec(`UPDATE "user" SET verified = false WHERE id = $1`, u)
	if err := h.run("user-info", "bob", "nobody"); err == nil {
		t.Fatal("no error for an unknown user")
	}
	h.has("bob ("+u+")\n", "  email:       bob@example.com (unverified)\n", "  password:    legacy (not yet moved to bcrypt)\n",
		"  connections: 0\n", "nobody: no such user\n")
	if !strings.Contains(h.out(), "  last login:  ") {
		t.Error("no last login line")
	}
}

func TestUserInfoLiercdDown(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("cy"), cfg)
	h.env.Liercd.BaseURL = "http://127.0.0.1:1"
	if err := h.run("user-info", "cy"); err != nil {
		t.Fatal(err)
	}
	h.has("    c1  ergo:6667  nick x  0 channels  liercd unreachable\n")
}

func TestUserInfoUsage(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("user-info"); err == nil || !asUsage(err, &ue) {
		t.Fatalf("got %v", err)
	}
}

func TestUserInfoGrandfathered(t *testing.T) {
	h := newHarness(t)
	u := h.user("Gus")
	h.exec(`UPDATE "user" SET verified = NULL WHERE id = $1`, u)
	if err := h.run("user-info", u); err != nil {
		t.Fatal(err)
	}
	h.has("  email:       Gus@example.com (grandfathered)\n")
}

// Production has users with no last_login (from before the column was
// NOT NULL); user-info shows them rather than failing.
func TestUserInfoNeverLoggedIn(t *testing.T) {
	h := newHarness(t)
	u := h.user("Nel")
	h.exec(`ALTER TABLE "user" ALTER COLUMN last_login DROP NOT NULL`)
	h.exec(`UPDATE "user" SET last_login = NULL WHERE id = $1`, u)
	if err := h.run("user-info", u); err != nil {
		t.Fatal(err)
	}
	h.has("  last login:  never\n", "  registered:  unknown\n")
}
