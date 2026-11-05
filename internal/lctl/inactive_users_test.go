package lctl

import (
	"strings"
	"testing"
	"time"
)

func TestInactiveUsers(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return now }
	login := func(id string, d time.Duration, verified bool) {
		h.exec(`UPDATE "user" SET last_login = $2, verified = $3 WHERE id = $1`, id, now.Add(-d), verified)
	}
	old := h.user("old")
	login(old, 400*24*time.Hour, true)
	h.conn("o1", old, `{"Host":"a.com","Port":6667,"Nick":"o"}`)
	h.conn("o2", old, `{"Host":"b.com","Port":6667,"Nick":"o"}`)
	older := h.user("older")
	login(older, 500*24*time.Hour, false)
	recent := h.user("recent")
	login(recent, 3*24*time.Hour, true)
	edge := h.user("edge") // logged in just inside the window
	login(edge, 90*24*time.Hour-time.Minute, true)

	if err := h.run("inactive-users", "--since", "90d"); err != nil {
		t.Fatal(err)
	}
	want := older + "  older  older@example.com (unverified)  last login 2025-06-20 12:00 UTC (500 days ago)  0 connections\n" +
		old + "  old  old@example.com  last login 2025-09-28 12:00 UTC (400 days ago)  2 connections\n"
	if h.out() != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", h.out(), want)
	}
	if got := h.stderr.String(); got != "2 user(s) haven't logged in since 2026-08-04 12:00 UTC (90d)\n" {
		t.Errorf("stderr %q", got)
	}

	// Go durations work too
	if err := h.run("inactive-users", "--since", "72h"); err != nil {
		t.Fatal(err)
	}
	h.has(older+"  ", old+"  ", edge+"  ")
	h.lacks(recent)
}

func TestInactiveUsersNone(t *testing.T) {
	h := newHarness(t)
	h.user("ann")
	if err := h.run("inactive-users", "--since", "30d"); err != nil {
		t.Fatal(err)
	}
	if h.out() != "" || !strings.HasPrefix(h.stderr.String(), "0 user(s) haven't logged in since ") {
		t.Fatalf("stdout %q stderr %q", h.out(), h.stderr.String())
	}
}

func TestInactiveUsersUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{{}, {"--since", "soon"}, {"--since", "0d"}, {"--since", "-5d"}, {"--since", "1.5d"}, {"--since", "30d", "extra"}} {
		var u *UsageError
		if err := h.run("inactive-users", args...); !asUsage(err, &u) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
}

func TestInactiveUsersGrandfathered(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return now }
	g := h.user("gus")
	h.exec(`UPDATE "user" SET last_login = $2, verified = NULL WHERE id = $1`, g, now.Add(-200*24*time.Hour))
	if err := h.run("inactive-users", "--since", "90d"); err != nil {
		t.Fatal(err)
	}
	h.has("  gus  gus@example.com (grandfathered)  last login ")
	h.lacks("(unverified)")
}

// A user with no last_login (old accounts in production) is listed first,
// as the least recently seen, rather than left out.
func TestInactiveUsersNeverLoggedIn(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return now }
	h.exec(`ALTER TABLE "user" ALTER COLUMN last_login DROP NOT NULL`)
	old := h.user("old")
	h.exec(`UPDATE "user" SET last_login = $2 WHERE id = $1`, old, now.Add(-400*24*time.Hour))
	never := h.user("nel")
	h.exec(`UPDATE "user" SET last_login = NULL WHERE id = $1`, never)
	if err := h.run("inactive-users", "--since", "90d"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSuffix(h.out(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %q, want two users", h.out())
	}
	if want := never + "  nel  nel@example.com  last login never  0 connections"; lines[0] != want {
		t.Errorf("first line %q, want %q", lines[0], want)
	}
	if !strings.HasPrefix(lines[1], old+"  old  ") {
		t.Errorf("second line %q, want old", lines[1])
	}
}
