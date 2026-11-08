package lctl

import (
	"context"
	"errors"
	"testing"
)

func seedSeen(h *harness) {
	u := h.user("ann")
	testenv_conn(h, "live", u, cfg)
	h.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1, 'live', '#now', 1)`, u)
	h.exec(`INSERT INTO seen ("user", connection, channel, message_id, updated) VALUES ($1, 'live', '#old', 1, now() - interval '2 months')`, u)
	h.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1, 'gone', '#a', 1)`, u)
}

func TestPruneSeenDryRun(t *testing.T) {
	h := newHarness(t)
	seedSeen(h)
	if err := h.run("prune-seen"); err != nil {
		t.Fatal(err)
	}
	h.has("2 seen row(s) to delete; run with --delete to delete them\n")
	if n := h.count(`SELECT count(*) FROM seen`); n != 3 {
		t.Errorf("%d rows, want 3", n)
	}
}

func TestPruneSeenDelete(t *testing.T) {
	h := newHarness(t)
	seedSeen(h)
	if err := h.run("prune-seen", "--delete"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 2 seen row(s)\n")
	if n := h.count(`SELECT count(*) FROM seen WHERE channel = '#now'`); n != 1 || h.count(`SELECT count(*) FROM seen`) != 1 {
		t.Error("wrong rows deleted")
	}
}

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

func TestPruneLogsDryRun(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	if err := h.run("prune-logs"); err != nil {
		t.Fatal(err)
	}
	h.has("5 log row(s) to delete; run with --delete to delete them\n", "2 archive row(s) to delete\n")
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 3 {
		t.Errorf("%d archive rows, want 3", n)
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 7 {
		t.Errorf("%d rows, want 7", n)
	}
}

func TestPruneLogsDeleteInBatches(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	if err := h.run("prune-logs", "--delete", "--batch", "2"); err != nil {
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

func TestPruneLogsCancelled(t *testing.T) {
	h := newHarness(t)
	seedLogs(h)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, c := range Commands {
		if c.Name == "prune-logs" {
			if err := c.Run(ctx, h.env, []string{"--delete", "--batch", "2"}); !errors.Is(err, context.Canceled) {
				t.Fatalf("got %v", err)
			}
		}
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 7 {
		t.Errorf("%d rows after cancel, want 7", n)
	}
}

func TestPruneLogsBadBatch(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("prune-logs", "--batch", "0"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
}

// Rows whose user is gone are orphans too, even when their connection row
// still exists (a connection whose owner was deleted without it).
func TestPruneLogsDeletedUsers(t *testing.T) {
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
	if err := h.run("prune-logs"); err != nil {
		t.Fatal(err)
	}
	h.has("4 log row(s) to delete; run with --delete to delete them\n", "1 archive row(s) to delete\n")
	if err := h.run("prune-logs", "--delete", "--batch", "2"); err != nil {
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
