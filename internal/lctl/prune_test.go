package lctl

import "testing"

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
