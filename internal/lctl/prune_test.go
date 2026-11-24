package lctl

import "testing"

func seedSeen(h *harness) {
	u := h.user("ann")
	insertConn(h, "live", u, cfg)
	h.exec(`INSERT INTO seen_markers (channel_id, message_id) VALUES (test_channel_id('live', '#now'), 1)`)
	h.exec(`INSERT INTO seen_markers (channel_id, message_id, updated) VALUES (test_channel_id('live', '#old'), 1, now() - interval '2 months')`)
}

func TestPruneSeenDryRun(t *testing.T) {
	h := newHarness(t)
	seedSeen(h)
	if err := h.run("prune-seen"); err != nil {
		t.Fatal(err)
	}
	h.has("1 seen row(s) to delete; run with --delete to delete them\n")
	if n := h.count(`SELECT count(*) FROM seen_markers`); n != 2 {
		t.Errorf("%d rows, want 2", n)
	}
}

func TestPruneSeenDelete(t *testing.T) {
	h := newHarness(t)
	seedSeen(h)
	if err := h.run("prune-seen", "--delete"); err != nil {
		t.Fatal(err)
	}
	h.has("deleted 1 seen row(s)\n")
	if n := h.count(`SELECT count(*) FROM seen_markers_v WHERE channel = '#now'`); n != 1 || h.count(`SELECT count(*) FROM seen_markers_v`) != 1 {
		t.Error("wrong rows deleted")
	}
}
