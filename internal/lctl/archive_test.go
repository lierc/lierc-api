package lctl

import (
	"strings"
	"testing"
	"time"
)

// seedArchive adds users and connections c1 and c2, each with 10 old
// messages in #a (ids come in insertion order), and returns the user.
func seedArchive(h *harness) string {
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	testenv_conn(h, "c2", u, cfg)
	addOld(h, u, "c1", "#a", 10)
	addOld(h, u, "c2", "#a", 10)
	return u
}

func addOld(h *harness, user, conn, channel string, n int) {
	h.exec(`INSERT INTO log (connection, "user", channel, time, message)
	  SELECT $1, $2, $3, now() - interval '60 days' + g * interval '1 second', '{}' FROM generate_series(1, $4::int) g`,
		conn, user, channel, n)
}

func TestArchiveDryRun(t *testing.T) {
	h := newHarness(t)
	u := seedArchive(h)
	addOld(h, u, "c1", "status", 8)
	if err := h.run("archive", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + h.out())
	h.has("c1 #a 7\n", "c1 status 5 to delete\n",
		"would move 3 block(s), 6 message(s); would delete 5 status message(s)\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Errorf("%d archive rows after a dry run", n)
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 28 {
		t.Errorf("%d log rows after a dry run, want 28", n)
	}
}

func TestArchiveMove(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + h.out())
	var first int
	if err := h.db.Pool.QueryRow(t.Context(), `SELECT min(id) FROM log WHERE connection = 'c1'`).Scan(&first); err != nil {
		t.Fatal(err)
	}
	h.has("c1 #a 1-2 2\n", "c1 #a 3-4 2\n", "c1 #a 5-6 2\n", "moved 3 block(s), 6 message(s); deleted 0 status message(s)\n")
	if first != 7 {
		t.Errorf("oldest remaining c1 row is %d, want 7", first)
	}
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 3 {
		t.Errorf("%d archive rows, want 3", n)
	}
}

func TestArchiveStatus(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	addOld(h, u, "c1", "status", 10)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "3"); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + h.out())
	h.has("c1 status 1-3 3 deleted\n", "c1 status 4-6 3 deleted\n", "c1 status 7-7 1 deleted\n",
		"moved 0 block(s), 0 message(s); deleted 7 status message(s)\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Errorf("%d archive rows from a status window", n)
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 3 {
		t.Errorf("%d log rows left, want 3", n)
	}
}

func TestArchivePauses(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1", "--pause", "7s"); err != nil {
		t.Fatal(err)
	}
	if len(h.slept) != 2 || h.slept[0] != 7*time.Second || h.slept[1] != 7*time.Second {
		t.Errorf("slept %v, want two 7s pauses for three blocks", h.slept)
	}
}

func TestArchiveDefaultPause(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	if len(h.slept) != 2 || h.slept[0] != time.Second || h.slept[1] != time.Second {
		t.Errorf("slept %v, want two 1s pauses for three blocks", h.slept)
	}
}

func TestArchiveDefaultBudget(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	clock := time.Now()
	h.env.Now = func() time.Time { return clock }
	h.beforeSleep = func() { clock = clock.Add(20 * time.Minute) }
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("moved 2 block(s), 4 message(s)", "budget")
}

func TestArchiveNoPauseNoBudget(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	clock := time.Now()
	h.env.Now = func() time.Time { return clock }
	h.beforeSleep = func() { clock = clock.Add(20 * time.Minute) }
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1", "--pause", "0", "--budget", "0"); err != nil {
		t.Fatal(err)
	}
	h.has("moved 3 block(s), 6 message(s)")
	if len(h.slept) != 0 {
		t.Errorf("slept %v, want none", h.slept)
	}
}

func TestArchiveMaxBlocks(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--max-blocks", "2"); err != nil {
		t.Fatal(err)
	}
	h.has("moved 2 block(s), 4 message(s); deleted 0 status message(s)\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 2 {
		t.Errorf("%d archive rows, want 2", n)
	}
}

func TestArchiveConnection(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 #a 7\n")
	h.lacks("c2")
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM log WHERE connection = 'c2'`); n != 10 {
		t.Errorf("c2 has %d log rows, want 10", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE connection = 'c1'`); n != 3 {
		t.Errorf("c1 has %d blocks, want 3", n)
	}
	// both, with a repeated id listed once
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1,c2,c2"); err != nil {
		t.Fatal(err)
	}
	h.has("c2 #a 11-12 2\n", "moved 3 block(s), 6 message(s)")
	if n := h.count(`SELECT count(*) FROM log_archive WHERE connection = 'c2'`); n != 3 {
		t.Errorf("c2 has %d blocks, want 3", n)
	}
}

func TestArchiveUnknownConnection(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1,nope")
	var ue *UsageError
	if err == nil || !asUsage(err, &ue) || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("got %v, want a usage error naming nope", err)
	}
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Errorf("%d archive rows after an unknown id", n)
	}
}

func TestArchiveBudget(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	clock := time.Now()
	h.env.Now = func() time.Time { return clock }
	h.beforeSleep = func() { clock = clock.Add(40 * time.Second) }
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1", "--pause", "1s", "--budget", "1m"); err != nil {
		t.Fatal(err)
	}
	t.Log("\n" + h.out())
	h.has("moved 2 block(s), 4 message(s)", "budget")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 2 {
		t.Errorf("%d archive rows, want 2", n)
	}
}

func TestArchiveUsage(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"--min-age", "soon"}, {"--min-age", "-3d"}, {"--keep", "0"}, {"--block", "0"},
		{"--pause", "-1s"}, {"--budget", "-1s"}, {"--max-blocks", "-1"}, {"extra"},
	} {
		var ue *UsageError
		if err := h.run("archive", args...); err == nil || !asUsage(err, &ue) {
			t.Errorf("%v: got %v, want a usage error", args, err)
		}
	}
}
