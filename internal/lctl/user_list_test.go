package lctl

import (
	"testing"
	"time"
)

func TestUserList(t *testing.T) {
	h := newHarness(t)
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	h.env.Now = func() time.Time { return now }
	bob := h.user("bob")
	h.exec(`UPDATE users SET last_login = $2, registered = $3, verified = true WHERE id = $1`,
		bob, now.Add(-3*24*time.Hour), now.Add(-30*24*time.Hour))
	h.conn("b1", bob, `{"Host":"a.com","Port":6667,"Nick":"b"}`)
	amy := h.user("Amy") // never logged in, unverified; sorts before bob despite the capital
	h.exec(`UPDATE users SET verified = false, last_login = NULL WHERE id = $1`, amy)
	cy := h.user("cy")
	h.exec(`UPDATE users SET last_login = $2, verified = NULL WHERE id = $1`, cy, now.Add(-400*24*time.Hour))

	if err := h.run("user-list"); err != nil {
		t.Fatal(err)
	}
	want := amy + "  Amy  Amy@example.com (unverified)  last login never  registered unknown  0 connections\n" +
		bob + "  bob  bob@example.com  last login 2026-10-30 12:00 UTC (3 days ago)  registered 2026-10-03 12:00 UTC (30 days ago)  1 connections\n" +
		cy + "  cy  cy@example.com (grandfathered)  last login 2025-09-28 12:00 UTC (400 days ago)  registered unknown  0 connections\n"
	if h.out() != want {
		t.Fatalf("stdout:\n%s\nwant:\n%s", h.out(), want)
	}
	if got := h.stderr.String(); got != "3 user(s)\n" {
		t.Errorf("stderr %q", got)
	}
}

func TestUserListUsage(t *testing.T) {
	h := newHarness(t)
	var u *UsageError
	if err := h.run("user-list", "extra"); !asUsage(err, &u) {
		t.Errorf("got %v, want a usage error", err)
	}
}
