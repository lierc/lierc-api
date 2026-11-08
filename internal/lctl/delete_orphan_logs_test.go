package lctl

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func seedLogs(h *harness) {
	u := h.user("ann")
	testenv_conn(h, "live", u, cfg)
	for i := 0; i < 2; i++ {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ('live', $1, '#a', now(), '{}')`, u)
	}
	for i := 0; i < 5; i++ {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ('gone', $1, '#a', now(), '{}')`, u)
	}
	addArchive(h, u, "gone")
	addArchive(h, u, "gone")
	addArchive(h, u, "live")
}

func TestDeleteOrphanLogsDryRun(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	if err := h.run("delete-orphan-logs"); err != nil {
		t.Fatal(err)
	}
	h.has("1 gone connection(s) and 0 gone user(s) still in log\n", "run with --delete to delete their log rows\n", "2 archive row(s) to delete\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 3 {
		t.Errorf("%d archive rows, want 3", n)
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 7 {
		t.Errorf("%d rows, want 7", n)
	}
}

func TestDeleteOrphanLogsDeleteInBatches(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	if err := h.run("delete-orphan-logs", "--delete", "--batch", "2"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 5 log row(s)\n", "deleted 2 archive row(s)\n")
	if n := h.count(`SELECT count(*) FROM log_archive WHERE connection = 'live'`); n != 1 || h.count(`SELECT count(*) FROM log_archive`) != 1 {
		t.Error("wrong archive rows deleted")
	}
	if n := h.count(`SELECT count(*) FROM log WHERE connection = 'live'`); n != 2 || h.count(`SELECT count(*) FROM log`) != 2 {
		t.Error("wrong rows deleted")
	}
}

func TestDeleteOrphanLogsCancelled(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range Commands {
		if c.Name == "delete-orphan-logs" {
			if err := c.Run(ctx, h.env, []string{"--delete", "--batch", "2"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
		}
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 7 {
		t.Errorf("%d rows after cancel, want 7", n)
	}
}

func TestDeleteOrphanLogsBadBatch(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("delete-orphan-logs", "--batch", "0"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
}

// Rows whose user is gone are orphans too, even when their connection row
// still exists (a connection whose owner was deleted without it).
func TestDeleteOrphanLogsDeletedUsers(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "live", u, cfg)
	testenv_conn(h, "ownerless", "nouser", cfg)
	for _, r := range []struct{ conn, user string }{
		{"live", u}, {"live", u},
		{"ownerless", "nouser"}, {"ownerless", "nouser"}, {"ownerless", "nouser"},
		{"live", "ghost"},
	} {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ($1, $2, '#a', now(), '{}')`, r.conn, r.user)
	}
	addArchive(h, "nouser", "ownerless")
	addArchive(h, u, "live")
	if err := h.run("delete-orphan-logs"); err != nil {
		t.Fatal(err)
	}
	h.has("0 gone connection(s) and 2 gone user(s) still in log\n", "run with --delete to delete their log rows\n", "1 archive row(s) to delete\n")
	if err := h.run("delete-orphan-logs", "--delete", "--batch", "2"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 4 log row(s)\n", "deleted 1 archive row(s)\n")
	if n := h.count(`SELECT count(*) FROM log WHERE "user" = $1`, u); n != 2 || h.count(`SELECT count(*) FROM log`) != 2 {
		t.Error("wrong log rows deleted")
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE "user" = $1`, u); n != 1 || h.count(`SELECT count(*) FROM log_archive`) != 1 {
		t.Error("wrong archive rows deleted")
	}
}

// A row whose connection and user are both gone is deleted once, and each
// gone id is counted once.
func TestDeleteOrphanLogsConnectionAndUserGone(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "live", u, cfg)
	for _, r := range []struct{ conn, user string }{
		{"live", u},
		{"gone", "ghost"}, {"gone", "ghost"},
		{"gone", u},
		{"live", "ghost"},
	} {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ($1, $2, '#a', now(), '{}')`, r.conn, r.user)
	}
	if err := h.run("delete-orphan-logs"); err != nil {
		t.Fatal(err)
	}
	h.has("1 gone connection(s) and 1 gone user(s) still in log\n", "run with --delete to delete their log rows\n")
	if err := h.run("delete-orphan-logs", "--delete", "--batch", "1"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 4 log row(s)\n",
		"connection gone: deleted 3 row(s) (3 so far)\n",
		"user ghost: deleted 1 row(s) (4 so far)\n")
	if n := h.count(`SELECT count(*) FROM log WHERE connection = 'live' AND "user" = $1`, u); n != 1 || h.count(`SELECT count(*) FROM log`) != 1 {
		t.Error("wrong log rows deleted")
	}
}

// A gone id with many rows reports every orphanProgressEvery rows while it
// runs; an id left with no rows prints nothing.
func TestDeleteOrphanLogsProgress(t *testing.T) {
	defer func(n int64) { orphanProgressEvery = n }(orphanProgressEvery)
	orphanProgressEvery = 2
	h := newHarness(t)
	for i := 0; i < 5; i++ {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ('gone', 'ghost', '#a', now(), '{}')`)
	}
	if err := h.run("delete-orphan-logs", "--delete", "--batch", "1"); err != nil {
		t.Fatal(err)
	}
	h.has("connection gone: 2 row(s) so far\n",
		"connection gone: 4 row(s) so far\n",
		"connection gone: deleted 5 row(s) (5 so far)\n",
		"deleted 5 log row(s)\n")
	if strings.Contains(h.out(), "user ghost") {
		t.Errorf("user ghost had no rows left, but was printed:\n%s", h.out())
	}
}
