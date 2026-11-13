package lctl

import (
	"strings"
	"testing"
)

// stopSetup: alice last logged in 40 days ago, with a1 running on main, a2
// dormant on main and a3 running on next; bob logged in yesterday, with b1
// running on main; carol never logged in, with c1 running on main.
func stopSetup(m *instancesHarness) {
	alice, bob, carol := m.user("alice"), m.user("bob"), m.user("carol")
	m.exec(`UPDATE "user" SET last_login = now() - interval '40 days' WHERE id = $1`, alice)
	m.exec(`UPDATE "user" SET last_login = now() - interval '1 day' WHERE id = $1`, bob)
	m.exec(`ALTER TABLE "user" ALTER COLUMN last_login DROP NOT NULL`)
	m.exec(`UPDATE "user" SET last_login = NULL WHERE id = $1`, carol)
	m.conn("a1", alice, `{"Host":"irc.a","Port":6697}`)
	m.conn("a2", alice, `{"Host":"irc.a","Port":6697}`)
	m.conn("a3", alice, `{"Host":"irc.n","Port":6667}`)
	m.conn("b1", bob, `{"Host":"irc.b","Port":6697}`)
	m.conn("c1", carol, `{"Host":"irc.c","Port":6697}`)
	m.exec(`UPDATE connection SET liercd='next' WHERE id='a3'`)
	for _, id := range []string{"a1", "b1", "c1"} {
		m.main.MarkRunning(id)
	}
	m.next.MarkRunning("a3")
}

func TestStopInactiveDryRun(t *testing.T) {
	m := newInstancesHarness(t)
	stopSetup(m)
	if err := m.run("stop-inactive", "--since", "30d"); err != nil {
		t.Fatal(err)
	}
	m.has(
		"a1  alice  irc.a:6697  main\n",
		"a3  alice  irc.n:6667  next\n",
		"c1  carol  irc.c:6697  main\n",
		"would stop 3 connection(s) of 2 inactive user(s)\n",
		"run again with --stop to stop them\n",
	)
	m.lacks("a2", "b1")
	if strings.Contains(m.calls(), "destroy") {
		t.Errorf("dry run destroyed: %s", m.calls())
	}
	for _, id := range []string{"a1", "b1", "c1"} {
		if !m.main.Running(id) {
			t.Errorf("%s stopped", id)
		}
	}
	if !m.next.Running("a3") {
		t.Error("a3 stopped")
	}
}

func TestStopInactiveStop(t *testing.T) {
	m := newInstancesHarness(t)
	stopSetup(m)
	if err := m.run("stop-inactive", "--stop", "--since", "30d"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a1", "c1"} {
		if m.main.Running(id) {
			t.Errorf("%s still running", id)
		}
	}
	if m.next.Running("a3") {
		t.Error("a3 still running on next")
	}
	if !m.main.Running("b1") {
		t.Error("b1 (active user) stopped")
	}
	if strings.Contains(m.calls(), "/b1/") {
		t.Errorf("liercd asked about b1: %s", m.calls())
	}
	if got := m.column("a3"); got != "next" {
		t.Errorf("a3 column = %q", got)
	}
	if n := m.count(`SELECT count(*) FROM connection`); n != 5 {
		t.Errorf("%d connections, want 5", n)
	}
	m.has("a1  alice  irc.a:6697  main\n", "stopped 3 connection(s) of 2 inactive user(s)\n")
	m.lacks("a2  ")
}

func TestStopInactiveFailureCarriesOn(t *testing.T) {
	m := newInstancesHarness(t)
	stopSetup(m)
	m.main.Respond("POST", "/a1/destroy", 500, "boom")
	if err := m.run("stop-inactive", "--stop", "--since", "30d"); err == nil {
		t.Fatal("want error")
	}
	m.has("a1: liercd said 500")
	if m.main.Running("c1") || m.next.Running("a3") {
		t.Error("the others weren't stopped")
	}
	m.has("stopped 2 connection(s) of 2 inactive user(s)\n")
}

func TestStopInactiveUsage(t *testing.T) {
	m := newInstancesHarness(t)
	stopSetup(m)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "--since"},
		{[]string{"--since", "3d"}, "revive"},
		{[]string{"--since", "bogus"}, "duration"},
		{[]string{"--since", "30d", "extra"}, "no arguments"},
	} {
		var u *UsageError
		err := m.run("stop-inactive", tc.args...)
		if !asUsage(err, &u) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want UsageError with %q", tc.args, err, tc.want)
		}
	}
	m.calledNothing()
}
