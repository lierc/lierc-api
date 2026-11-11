package lctl

import (
	"fmt"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/login"
)

// addLogin makes a login for user with the given agent and ip, started and
// last used the given number of hours ago, and returns its id.
func (h *harness) addLogin(user, agent, ip string, startedHoursAgo, usedHoursAgo int) int64 {
	h.t.Helper()
	if _, err := login.Create(h.t.Context(), h.db, user, agent, ip); err != nil {
		h.t.Fatal(err)
	}
	var id int64
	if err := h.db.Pool.QueryRow(h.t.Context(), `SELECT max(id) FROM login`).Scan(&id); err != nil {
		h.t.Fatal(err)
	}
	h.exec(`UPDATE login SET created = '2026-11-01 12:00:00+00'::timestamptz - $2 * interval '1 hour',
	  last_used = now() - $3 * interval '1 hour' WHERE id = $1`, id, startedHoursAgo, usedHoursAgo)
	return id
}

func (h *harness) logins(user string) int {
	return h.count(`SELECT count(*) FROM login WHERE "user" = $1`, user)
}

func TestLogins(t *testing.T) {
	h := newHarness(t)
	u := h.user("Ann")
	old := h.addLogin(u, "Firefox", "192.0.2.7", 5, 50)
	recent := h.addLogin(u, "Safari", "", 3, 1)
	expired := h.addLogin(u, "Ancient", "192.0.2.9", 400, 8*24)

	for _, who := range []string{"ann", "ANN@example.com", u} {
		if err := h.run("logins", who); err != nil {
			t.Fatalf("%s: %v", who, err)
		}
		lines := strings.Split(strings.TrimRight(h.out(), "\n"), "\n")
		if len(lines) != 2 {
			t.Fatalf("%s: want 2 lines:\n%s", who, h.out())
		}
		if !strings.HasPrefix(lines[0], fmt.Sprintf("%d  started 2026-11-01T09:00:00Z  last used ", recent)) ||
			!strings.HasSuffix(lines[0], `  -  "Safari"`) {
			t.Errorf("first line: %q", lines[0])
		}
		if !strings.HasPrefix(lines[1], fmt.Sprintf("%d  started 2026-11-01T07:00:00Z  last used ", old)) ||
			!strings.HasSuffix(lines[1], `  192.0.2.7  "Firefox"`) {
			t.Errorf("second line: %q", lines[1])
		}
		h.lacks(fmt.Sprintf("%d  ", expired), "Ancient")
	}

	h.user("Bob")
	if err := h.run("logins", "bob"); err != nil {
		t.Fatal(err)
	}
	h.has("no logins\n")

	if err := h.run("logins", "nobody"); err == nil {
		t.Error("unknown user: no error")
	}
	h.has("nobody: no such user\n")

	var ue *UsageError
	if err := h.run("logins"); !asUsage(err, &ue) {
		t.Errorf("no user: %v, want UsageError", err)
	}
}

func TestEndLogin(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	other := h.user("bob")
	a := h.addLogin(u, "A", "", 1, 1)
	h.addLogin(u, "B", "", 1, 1)
	b := h.addLogin(other, "C", "", 1, 1)

	if err := h.run("end-login", "ann", fmt.Sprint(a)); err != nil {
		t.Fatal(err)
	}
	h.has("ended 1 login\n")
	h.lacks("logins")
	if h.logins(u) != 1 || h.count(`SELECT count(*) FROM login WHERE id = $1`, a) != 0 {
		t.Error("login not deleted, or the other one with it")
	}

	if err := h.run("end-login", "ann", fmt.Sprint(b)); err == nil {
		t.Error("another user's login: no error")
	}
	h.has("no such login\n")
	if h.logins(other) != 1 {
		t.Error("another user's login was deleted")
	}
	if err := h.run("end-login", "ann", "99999"); err == nil {
		t.Error("unknown id: no error")
	}
	h.has("no such login\n")

	if err := h.run("end-login", "nobody", "1"); err == nil {
		t.Error("unknown user: no error")
	}
	h.has("nobody: no such user\n")

	h.addLogin(u, "D", "", 1, 1)
	if err := h.run("end-login", "ann", "--all"); err != nil {
		t.Fatal(err)
	}
	h.has("ended 2 logins\n")
	if h.logins(u) != 0 || h.logins(other) != 1 {
		t.Error("--all didn't end exactly ann's logins")
	}

	var ue *UsageError
	for _, args := range [][]string{{}, {"ann"}, {"ann", "x"}, {"ann", "1", "--all"}, {"ann", "--all", "1"}} {
		if err := h.run("end-login", args...); !asUsage(err, &ue) {
			t.Errorf("%v: %v, want UsageError", args, err)
		}
	}
	if h.logins(other) != 1 {
		t.Error("usage errors deleted something")
	}
}

func TestPruneLogins(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.addLogin(u, "old", "", 1, 8*24)
	live := h.addLogin(u, "new", "", 1, 1)
	if err := h.run("prune-logins"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 1 expired login\n")
	if h.count(`SELECT count(*) FROM login WHERE id = $1`, live) != 1 || h.logins(u) != 1 {
		t.Error("live login not kept, or expired one not deleted")
	}
	if err := h.run("prune-logins"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 0 expired logins\n")
}
