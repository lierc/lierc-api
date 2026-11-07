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
	// no counts: a dry run only says which channels have a block or more to
	// move and which status windows have old messages to delete
	h.has("c1 #a\n", "c1 status to delete\n",
		"would archive 1 channel(s); would delete from 1 status window(s)\n")
	h.lacks("c1 #a 7", "block(s)")
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
	// a pause after every block moved: it can't know which is the last
	if len(h.slept) != 3 || h.slept[0] != 7*time.Second || h.slept[2] != 7*time.Second {
		t.Errorf("slept %v, want a 7s pause after each of three blocks", h.slept)
	}
}

func TestArchiveDefaultPause(t *testing.T) {
	h := newHarness(t)
	seedArchive(h)
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	if len(h.slept) != 3 || h.slept[0] != time.Second || h.slept[2] != time.Second {
		t.Errorf("slept %v, want a 1s pause after each of three blocks", h.slept)
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
	h.has("c1 #a\n")
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

// By default messages older than 7 days can be archived; newer ones stay.
func TestArchiveDefaultMinAge(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	for _, ch := range []struct {
		name string
		age  string
	}{{"#old", "10 days"}, {"#new", "3 days"}} {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message)
		  SELECT 'c1', $1, $2, now() - $3::interval + g * interval '1 second', '{}' FROM generate_series(1, 10) g`,
			u, ch.name, ch.age)
	}
	if err := h.run("archive", "--move", "--keep", "3", "--block", "2", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE channel = '#old'`); n != 3 {
		t.Errorf("%d #old blocks archived, want 3 (10-day-old messages are past the default age)", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE channel = '#new'`); n != 0 {
		t.Errorf("%d #new blocks archived, want 0 (3-day-old messages are within the default age)", n)
	}
}

// By default a channel keeps its newest 1,000 messages: of 1,600 old ones,
// one block of 500 is archived and 1,100 stay.
func TestArchiveDefaultKeep(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	addOld(h, u, "c1", "#a", 1600)
	if err := h.run("archive", "--move", "--connection", "c1", "--pause", "0"); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT coalesce(sum(count), 0) FROM log_archive WHERE connection = 'c1'`); n != 500 {
		t.Errorf("%d messages archived, want 500", n)
	}
	if n := h.count(`SELECT count(*) FROM log WHERE connection = 'c1'`); n != 1100 {
		t.Errorf("%d messages left in log, want 1100", n)
	}
}

// ZNC's *status is deleted like the status window, and named in the output.
func TestArchiveStarStatus(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	addOld(h, u, "c1", "*status", 10)
	if err := h.run("archive", "--keep", "3", "--block", "3", "--connection", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 *status to delete\n", "would delete from 1 status window(s)\n")
	if err := h.run("archive", "--move", "--keep", "3", "--block", "3", "--connection", "c1", "--pause", "0"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 *status 1-3 3 deleted\n", "deleted 7 status message(s)\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Errorf("%d archive rows from *status", n)
	}
}
