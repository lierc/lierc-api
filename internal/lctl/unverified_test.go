package lctl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/admin"
)

// unverifiedSetup: ursula and uma haven't confirmed their email; ursula
// registered 5 hours ago and her last stream ended 4 hours ago, with u1
// running on main and u2 on next; uma registered 4 days ago and never had
// a stream, with m1 running. val is verified and gil from before
// verification (NULL), both idle for days, with v1 and g1 running; nell is
// unverified but registered an hour ago, with n1 running.
func unverifiedSetup(m *instancesHarness) map[string]string {
	ids := map[string]string{}
	for _, name := range []string{"ursula", "uma", "val", "gil", "nell"} {
		ids[name] = m.user(name)
	}
	m.exec(`UPDATE users SET verified = false, registered = now() - interval '5 hours', last_login = now() - interval '4 hours' WHERE id = $1`, ids["ursula"])
	m.exec(`UPDATE users SET verified = false, registered = now() - interval '4 days', last_login = NULL WHERE id = $1`, ids["uma"])
	m.exec(`UPDATE users SET verified = true, registered = now() - interval '9 days', last_login = now() - interval '9 days' WHERE id = $1`, ids["val"])
	m.exec(`UPDATE users SET verified = NULL, registered = now() - interval '9 days', last_login = now() - interval '9 days' WHERE id = $1`, ids["gil"])
	m.exec(`UPDATE users SET verified = false, registered = now() - interval '1 hour', last_login = NULL WHERE id = $1`, ids["nell"])
	m.conn("u1", ids["ursula"], `{"Host":"irc.u","Port":6697}`)
	m.conn("u2", ids["ursula"], `{"Host":"irc.n","Port":6667}`)
	m.conn("m1", ids["uma"], `{"Host":"irc.m","Port":6697}`)
	m.conn("v1", ids["val"], `{"Host":"irc.v","Port":6697}`)
	m.conn("g1", ids["gil"], `{"Host":"irc.g","Port":6697}`)
	m.conn("n1", ids["nell"], `{"Host":"irc.x","Port":6697}`)
	m.exec(`UPDATE connections SET liercd='next' WHERE id='u2'`)
	for _, id := range []string{"u1", "m1", "v1", "g1", "n1"} {
		m.main.MarkRunning(id)
	}
	m.next.MarkRunning("u2")
	return ids
}

// streaming points the harness's admin API at one that sees users as
// having a stream open.
func (m *instancesHarness) streaming(users ...string) {
	open := map[string]bool{}
	for _, u := range users {
		open[u] = true
	}
	srv := httptest.NewServer(admin.New(m.db, m.announce, admin.Streaming(func(u string) bool { return open[u] })))
	m.t.Cleanup(srv.Close)
	m.env.Admin = &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
}

func TestStopUnverifiedDryRun(t *testing.T) {
	m := newInstancesHarness(t)
	unverifiedSetup(m)
	if err := m.run("stop-unverified", "--since", "3h"); err != nil {
		t.Fatal(err)
	}
	m.has(
		"u1  ursula  irc.u:6697  main\n",
		"u2  ursula  irc.n:6667  next\n",
		"m1  uma  irc.m:6697  main\n",
		"would stop 3 connection(s) of 2 unverified user(s)\n",
		"run again with --stop to stop them\n",
	)
	m.lacks("v1", "g1", "n1")
	if strings.Contains(m.calls(), "destroy") {
		t.Errorf("dry run destroyed: %s", m.calls())
	}
}

func TestStopUnverifiedStop(t *testing.T) {
	m := newInstancesHarness(t)
	unverifiedSetup(m)
	if err := m.run("stop-unverified", "--stop", "--since", "3h"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"u1", "m1"} {
		if m.main.Running(id) {
			t.Errorf("%s still running", id)
		}
	}
	if m.next.Running("u2") {
		t.Error("u2 still running on next")
	}
	for _, id := range []string{"v1", "g1", "n1"} {
		if !m.main.Running(id) {
			t.Errorf("%s stopped", id)
		}
	}
	if n := m.count(`SELECT count(*) FROM connections`); n != 6 {
		t.Errorf("%d connections, want 6: nothing is deleted", n)
	}
	m.has("stopped 3 connection(s) of 2 unverified user(s)\n")
}

func TestStopUnverifiedSkipsOpenStreams(t *testing.T) {
	m := newInstancesHarness(t)
	ids := unverifiedSetup(m)
	m.streaming(ids["ursula"])
	if err := m.run("stop-unverified", "--stop", "--since", "3h"); err != nil {
		t.Fatal(err)
	}
	if !m.main.Running("u1") || !m.next.Running("u2") {
		t.Error("ursula has a stream open, but her connections were stopped")
	}
	if m.main.Running("m1") {
		t.Error("m1 still running")
	}
	m.has("stopped 1 connection(s) of 1 unverified user(s)\n")
}

func TestStopUnverifiedUsage(t *testing.T) {
	m := newInstancesHarness(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--since"},
		{[]string{"--since", "bogus"}, "duration"},
		{[]string{"--since", "3h", "extra"}, "no arguments"},
	} {
		var u *UsageError
		err := m.run("stop-unverified", tc.args...)
		if !asUsage(err, &u) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want UsageError with %q", tc.args, err, tc.want)
		}
	}
	m.calledNothing()
}

func TestDeleteUnverifiedDryRun(t *testing.T) {
	m := newInstancesHarness(t)
	ids := unverifiedSetup(m)
	if err := m.run("delete-unverified", "--since", "3d"); err != nil {
		t.Fatal(err)
	}
	m.has(ids["uma"]+" (uma, uma@example.com): ", "dry run: nothing deleted")
	m.lacks("ursula", "val", "gil", "nell")
	if n := m.count(`SELECT count(*) FROM users`); n != 5 {
		t.Errorf("%d users, want 5", n)
	}
}

func TestDeleteUnverifiedDelete(t *testing.T) {
	m := newInstancesHarness(t)
	ids := unverifiedSetup(m)
	if err := m.run("delete-unverified", "--delete", "--since", "3d"); err != nil {
		t.Fatal(err)
	}
	m.has(ids["uma"] + ": deleted\n")
	if n := m.count(`SELECT count(*) FROM users WHERE id = $1`, ids["uma"]); n != 0 {
		t.Error("uma wasn't deleted")
	}
	if m.main.Running("m1") {
		t.Error("m1 still running")
	}
	if n := m.count(`SELECT count(*) FROM users`); n != 4 {
		t.Errorf("%d users, want 4", n)
	}
}

func TestDeleteUnverifiedNone(t *testing.T) {
	m := newInstancesHarness(t)
	unverifiedSetup(m)
	if err := m.run("delete-unverified", "--delete", "--since", "30d"); err != nil {
		t.Fatal(err)
	}
	m.has("no unverified users registered before ")
	m.calledNothing()
}

func TestDeleteUnverifiedUsage(t *testing.T) {
	m := newInstancesHarness(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--since"},
		{[]string{"--since", "3h"}, "at least 1d"},
		{[]string{"--since", "bogus"}, "duration"},
		{[]string{"--since", "3d", "extra"}, "no arguments"},
	} {
		var u *UsageError
		err := m.run("delete-unverified", tc.args...)
		if !asUsage(err, &u) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want UsageError with %q", tc.args, err, tc.want)
		}
	}
	m.calledNothing()
}
