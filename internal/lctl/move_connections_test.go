package lctl

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// moveSetup adds connections c1..c5 on main, one on next (n1) and one
// with an empty column (e1, which counts as main), all running.
func moveSetup(m *moveHarness) {
	u := m.user("alice")
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "e1", "n1"} {
		m.conn(id, u, `{"Host":"x"}`)
	}
	m.exec(`UPDATE connection SET liercd='next' WHERE id='n1'`)
	m.exec(`UPDATE connection SET liercd='' WHERE id='e1'`)
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "e1"} {
		m.main.MarkRunning(id)
	}
	m.next.MarkRunning("n1")
}

func TestMoveConnectionsDryRun(t *testing.T) {
	m := newMoveHarness(t)
	moveSetup(m)
	if err := m.run("move-connections", "main", "next"); err != nil {
		t.Fatal(err)
	}
	m.has(
		"would move 6 connection(s) from main to next:\n",
		"  c1 (alice)\n", "  c5 (alice)\n", "  e1 (alice)\n",
		"run again with --move",
	)
	m.lacks("n1")
	m.calledNothing()
	if got := m.column("c1"); got != "" {
		t.Errorf("column = %q", got)
	}
}

func TestMoveConnectionsInBatches(t *testing.T) {
	m := newMoveHarness(t)
	moveSetup(m)
	if err := m.run("move-connections", "--move", "--batch", "2", "--pause", "10s", "main", "next"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "e1"} {
		if got := m.column(id); got != "next" {
			t.Errorf("%s column = %q", id, got)
		}
		if m.main.Running(id) || !m.next.Running(id) {
			t.Errorf("%s running: main %v, next %v", id, m.main.Running(id), m.next.Running(id))
		}
	}
	if got := m.column("n1"); got != "next" {
		t.Errorf("n1 column = %q", got)
	}
	// six in batches of two: a pause after the first and second batches
	if len(m.slept) != 2 || m.slept[0] != 10*time.Second || m.slept[1] != 10*time.Second {
		t.Errorf("slept %v, want two 10s pauses", m.slept)
	}
	m.has("moved c1 from main to next\n", "moved e1 from main to next\n", "moved 6 connection(s) from main to next\n")
}

func TestMoveConnectionsLimit(t *testing.T) {
	m := newMoveHarness(t)
	moveSetup(m)
	if err := m.run("move-connections", "--move", "--limit", "3", "main", "next"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"c1": "next", "c2": "next", "c3": "next", "c4": "", "c5": "", "e1": ""} {
		if got := m.column(id); got != want {
			t.Errorf("%s column = %q, want %q", id, got, want)
		}
	}
	m.has("moved 3 connection(s) from main to next (3 left on main)\n")
	if len(m.slept) != 0 {
		t.Errorf("slept %v; three fit in one batch of five", m.slept)
	}

	// a dry run with --limit lists only that many
	if err := m.run("move-connections", "--limit", "2", "main", "next"); err != nil {
		t.Fatal(err)
	}
	m.has("would move 2 of 3 connection(s) from main to next:\n", "  c4 (alice)\n", "  c5 (alice)\n")
	m.lacks("e1")
}

// A failed move stops the run, so a broken target doesn't take every
// connection down with it; moved connections stay moved.
func TestMoveConnectionsStopsOnFailure(t *testing.T) {
	m := newMoveHarness(t)
	moveSetup(m)
	// c3 is already running on next, which move-connection refuses
	m.next.MarkRunning("c3")
	err := m.run("move-connections", "--move", "main", "next")
	if err == nil || !strings.Contains(err.Error(), "c3 is already running on next") {
		t.Fatalf("err = %v", err)
	}
	m.has("stopped after moving 2 of 6 connection(s)")
	for id, want := range map[string]string{"c1": "next", "c2": "next", "c3": "", "c4": "", "c5": ""} {
		if got := m.column(id); got != want {
			t.Errorf("%s column = %q, want %q", id, got, want)
		}
	}
}

func TestMoveConnectionsBackToMain(t *testing.T) {
	m := newMoveHarness(t)
	moveSetup(m)
	if err := m.run("move-connections", "--move", "next", "main"); err != nil {
		t.Fatal(err)
	}
	if n := m.count(`SELECT count(*) FROM connection WHERE id='n1' AND liercd IS NULL`); n != 1 {
		t.Errorf("n1 isn't on main")
	}
	m.has("moved n1 from next to main\n", "moved 1 connection(s) from next to main\n")
}

// Dormant connections move without starting, so they don't count toward
// a batch: only started connections are paced.
func TestMoveConnectionsDormantArentPaced(t *testing.T) {
	m := newMoveHarness(t)
	u := m.user("alice")
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		m.conn(id, u, `{"Host":"x"}`)
	}
	// c2, c4 and c5 are running; the rest are dormant
	for _, id := range []string{"c2", "c4", "c5"} {
		m.main.MarkRunning(id)
	}
	if err := m.run("move-connections", "--move", "--batch", "2", "--pause", "10s", "main", "next"); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"c1", "c2", "c3", "c4", "c5", "c6"} {
		if got := m.column(id); got != "next" {
			t.Errorf("%s column = %q", id, got)
		}
	}
	for id, want := range map[string]bool{"c1": false, "c2": true, "c3": false, "c4": true, "c5": true, "c6": false} {
		if got := m.next.Running(id); got != want {
			t.Errorf("%s running on next = %v, want %v", id, got, want)
		}
	}
	// three started in batches of two: one pause, after c4
	if len(m.slept) != 1 {
		t.Errorf("slept %v, want one pause", m.slept)
	}
	m.has("started 2, pausing 10s\n", "moved 6 connection(s) from main to next\n", "started 3 on next; 3 weren't running and will start when their users' streams next open\n")
}

// All dormant: nothing is started, so nothing pauses.
func TestMoveConnectionsAllDormant(t *testing.T) {
	m := newMoveHarness(t)
	u := m.user("alice")
	for _, id := range []string{"c1", "c2", "c3"} {
		m.conn(id, u, `{}`)
	}
	if err := m.run("move-connections", "--move", "--batch", "1", "main", "next"); err != nil {
		t.Fatal(err)
	}
	if len(m.slept) != 0 {
		t.Errorf("slept %v", m.slept)
	}
	m.has("started 0 on next; 3 weren't running")
}

// dormantSetup adds c1..c4 on main, with only c2 running, and n1 on next.
func dormantSetup(m *moveHarness) {
	u := m.user("alice")
	for _, id := range []string{"c1", "c2", "c3", "c4", "n1"} {
		m.conn(id, u, `{}`)
	}
	m.exec(`UPDATE connection SET liercd='next' WHERE id='n1'`)
	m.main.MarkRunning("c2")
}

// --dormant lists only the connections that aren't running, asking FROM.
func TestMoveConnectionsDormantDryRun(t *testing.T) {
	m := newMoveHarness(t)
	dormantSetup(m)
	if err := m.run("move-connections", "--dormant", "main", "next"); err != nil {
		t.Fatal(err)
	}
	m.has("would move 3 dormant connection(s) from main to next (1 running left alone):\n", "  c1 (alice)\n", "  c3 (alice)\n", "  c4 (alice)\n")
	m.lacks("c2 (", "n1")
	if got := m.calls(); got != "main GET /c1/status|main GET /c2/status|main GET /c3/status|main GET /c4/status" {
		t.Errorf("calls = %q", got)
	}
}

// --dormant --move moves only dormant connections, and they stay dormant;
// the running one stays where it is, running.
func TestMoveConnectionsDormantOnly(t *testing.T) {
	m := newMoveHarness(t)
	dormantSetup(m)
	if err := m.run("move-connections", "--move", "--dormant", "--batch", "1", "main", "next"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"c1": "next", "c2": "", "c3": "next", "c4": "next"} {
		if got := m.column(id); got != want {
			t.Errorf("%s column = %q, want %q", id, got, want)
		}
	}
	for _, id := range []string{"c1", "c2", "c3", "c4"} {
		if m.next.Running(id) {
			t.Errorf("%s started on next", id)
		}
	}
	if !m.main.Running("c2") {
		t.Error("c2 stopped on main")
	}
	if len(m.slept) != 0 {
		t.Errorf("slept %v; nothing was started", m.slept)
	}
	m.has("moved 3 connection(s) from main to next\n", "left 1 running connection(s) on main\n")
}

// --limit counts only the dormant connections.
func TestMoveConnectionsDormantLimit(t *testing.T) {
	m := newMoveHarness(t)
	dormantSetup(m)
	if err := m.run("move-connections", "--move", "--dormant", "--limit", "2", "main", "next"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"c1": "next", "c2": "", "c3": "next", "c4": ""} {
		if got := m.column(id); got != want {
			t.Errorf("%s column = %q, want %q", id, got, want)
		}
	}
	m.has("moved 2 connection(s) from main to next (1 dormant left on main)\n")
}

// If FROM can't say whether a connection is running, nothing moves.
func TestMoveConnectionsDormantStatusFails(t *testing.T) {
	m := newMoveHarness(t)
	dormantSetup(m)
	m.main.Respond("GET", "/c3/status", 500, "boom")
	err := m.run("move-connections", "--move", "--dormant", "main", "next")
	if err == nil || !strings.Contains(err.Error(), "c3") {
		t.Fatalf("err = %v", err)
	}
	if got := m.column("c1"); got != "" {
		t.Errorf("c1 column = %q", got)
	}
}

func TestMoveConnectionsNothingToMove(t *testing.T) {
	m := newMoveHarness(t)
	m.conn("n1", m.user("alice"), `{}`)
	m.exec(`UPDATE connection SET liercd='next' WHERE id='n1'`)
	if err := m.run("move-connections", "--move", "main", "next"); err != nil {
		t.Fatal(err)
	}
	m.has("no connections on main\n")
	m.calledNothing()
}

func TestMoveConnectionsUsage(t *testing.T) {
	m := newMoveHarness(t)
	for _, tc := range []struct {
		args []string
		want string
	}{
		{nil, "give the instance to move from and the one to move to"},
		{[]string{"main"}, "give the instance to move from and the one to move to"},
		{[]string{"main", "main"}, "the instances must differ"},
		{[]string{"main", "other"}, `instances are main or next, not "other"`},
		{[]string{"--batch", "0", "main", "next"}, "--batch must be at least 1"},
		{[]string{"--limit", "-1", "main", "next"}, "--limit can't be negative"},
		{[]string{"--pause", "-1s", "main", "next"}, "--pause can't be negative"},
	} {
		err := m.run("move-connections", tc.args...)
		var usage *UsageError
		if err == nil || !errors.As(err, &usage) || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: err = %v, want usage error %q", tc.args, err, tc.want)
		}
	}
	m.env.Config.LiercdNextHost = ""
	m.conn("c1", m.user("alice"), `{}`)
	err := m.run("move-connections", "--move", "main", "next")
	if err == nil || !strings.Contains(err.Error(), "LIERCD_NEXT_HOST") {
		t.Errorf("unset host: err = %v", err)
	}
	m.calledNothing()
}
