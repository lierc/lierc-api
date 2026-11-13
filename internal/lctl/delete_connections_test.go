package lctl

import (
	"context"

	"errors"
	"github.com/lierc/lierc-api/internal/db"
	"strings"
	"testing"
)

func TestDeleteConnectionsDryRun(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.conn("c1", u, `{"Host":"irc.libera.chat","Port":6697,"Nick":"ann"}`)
	if err := h.run("delete-connections", "c1", "nope"); err != nil {
		t.Fatal(err)
	}
	h.has("nope: no such connection\n",
		"c1 (user "+u+", ann, irc.libera.chat:6697): log=1 seen=1 private=1 connection=1\n",
		"dry run: nothing deleted; run with --delete to delete\n")
	if n := h.count(`SELECT count(*) FROM connection`); n != 1 {
		t.Errorf("%d connections, want 1", n)
	}
	if len(h.liercd.Calls()) != 0 {
		t.Error("dry run called liercd")
	}
}

func TestDeleteConnectionsDelete(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.conn("c1", u, cfg)
	h.conn("c2", u, cfg)
	addArchive(h, u, "c1")
	addArchive(h, u, "c2")
	h.liercd.Respond("POST", "/*/destroy", 200, "ok")
	if err := h.run("delete-connections", "--delete", "--settle", "3", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("c1: deleted (log=1 seen=1 private=1 connection=1)\n", "deleted 1 connection(s), skipped 0\n")
	if got := h.announced; len(got) != 1 || got[0] != (db.ConnectionChange{User: u, Connection: "c1", Action: "delete"}) {
		t.Errorf("announced %+v, want one delete of c1", got)
	}
	if got := strings.Join(h.calls("POST", "/destroy"), ","); got != "c1" {
		t.Errorf("destroyed %s", got)
	}
	if len(h.slept) != 1 || h.slept[0].Seconds() != 3 {
		t.Errorf("slept %v", h.slept)
	}
	for _, table := range []string{"log", "seen", "private", "log_archive"} {
		if n := h.count(`SELECT count(*) FROM ` + table + ` WHERE connection = 'c1'`); n != 0 {
			t.Errorf("%s: %d rows left", table, n)
		}
		if n := h.count(`SELECT count(*) FROM ` + table + ` WHERE connection = 'c2'`); n != 1 {
			t.Errorf("%s: other connection's row gone", table)
		}
	}
	if n := h.count(`SELECT count(*) FROM "user" WHERE id = $1`, u); n != 1 {
		t.Error("user deleted")
	}
}

func TestDeleteConnectionsLiercdRefuses(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.conn("c1", u, cfg)
	h.liercd.Respond("POST", "/c1/destroy", 500, "nok")
	if err := h.run("delete-connections", "--delete", "c1"); err == nil {
		t.Fatal("no error")
	}
	h.has("c1: liercd said 500 Internal Server Error, skipped\n", "deleted 0 connection(s), skipped 1\n")
	if n := h.count(`SELECT count(*) FROM connection WHERE id = 'c1'`); n != 1 {
		t.Error("refused connection deleted")
	}
}

func TestDeleteConnectionsCancelledDuringSettle(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("ann"), cfg)
	h.sleepErr = context.Canceled
	if err := h.run("delete-connections", "--delete", "c1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if n := h.count(`SELECT count(*) FROM connection`); n != 1 {
		t.Error("deleted after cancel")
	}
}

func TestDeleteConnectionsUnknownOnlyDeletesNothing(t *testing.T) {
	h := newHarness(t)
	if err := h.run("delete-connections", "--delete", "nope"); err != nil {
		t.Fatal(err)
	}
	h.has("nope: no such connection\n")
	if len(h.liercd.Calls()) != 0 || len(h.slept) != 0 {
		t.Error("destroyed or waited for an unknown id")
	}
}

func TestDeleteConnectionsStdinAndUsage(t *testing.T) {
	h := newHarness(t)
	h.conn("c1", h.user("ann"), cfg)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("c1\n")
	if err := h.run("delete-connections"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 (user ")
	var ue *UsageError
	h.env.StdinIsTerminal = true
	if err := h.run("delete-connections"); !errors.As(err, &ue) {
		t.Errorf("no ids: %v", err)
	}
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("")
	if err := h.run("delete-connections", "--delete"); !errors.As(err, &ue) {
		t.Errorf("empty stdin: %v", err)
	}
}

// Each deleted connection is announced to the stream server, with its
// owner, once the delete has committed; a skipped one isn't.
func TestDeleteConnectionsNotifies(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.conn("c1", u, cfg)
	h.conn("c2", u, cfg)
	h.liercd.Respond("POST", "/c1/destroy", 200, "ok")
	h.liercd.Respond("POST", "/c2/destroy", 500, "no")
	if err := h.run("delete-connections", "--delete", "--settle", "0", "c1", "c2"); err == nil || !strings.Contains(err.Error(), "1 connection(s) skipped") {
		t.Fatalf("err %v, want c2 skipped", err)
	}
	if got := h.announced; len(got) != 1 || got[0] != (db.ConnectionChange{User: u, Connection: "c1", Action: "delete"}) {
		t.Errorf("announced %+v", got)
	}
}
