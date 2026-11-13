package lctl

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// addArchive adds an archive row for a connection.
func addArchive(h *harness, user, conn string) {
	h.exec(`INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
	  VALUES ($1, $2, '#a', 1, 2, now(), now(), 2, '[]')`, conn, user)
}

// seedUser adds a user with two connections and a row in every table.
func seedUser(h *harness, name string) string {
	u := h.user(name)
	h.conn(name+"1", u, `{"Host":"ergo","Port":6667,"Nick":"x"}`)
	h.conn(name+"2", u, `{"Host":"ergo","Port":6667,"Nick":"y"}`)
	h.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ($1, 'gone', '#a', 1)`, u)
	h.exec(`INSERT INTO pref ("user", name, value) VALUES ($1, 'theme', 'dark')`, u)
	h.exec(`INSERT INTO web_push (endpoint, key, auth, "user") VALUES ('https://push/x', 'k', 'a', $1)`, u)
	return u
}

func (h *harness) userRows(u, c1, c2 string) int {
	return h.count(`SELECT (SELECT count(*) FROM log WHERE connection IN ($2, $3))
	  + (SELECT count(*) FROM seen WHERE "user" = $1 OR connection IN ($2, $3))
	  + (SELECT count(*) FROM private WHERE connection IN ($2, $3))
	  + (SELECT count(*) FROM pref WHERE "user" = $1)
	  + (SELECT count(*) FROM web_push WHERE "user" = $1)
	  + (SELECT count(*) FROM connection WHERE "user" = $1)
	  + (SELECT count(*) FROM "user" WHERE id = $1)`, u, c1, c2)
}

func TestDeleteUsersDryRun(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	if err := h.run("delete-users", u); err != nil {
		t.Fatal(err)
	}
	h.has(u+" (ann, ann@example.com): log=2 seen=3 private=2 pref=1 web_push=1 connection=2 user=1\n",
		"dry run: nothing deleted; run with --delete to delete\n")
	if n := h.userRows(u, "ann1", "ann2"); n != 12 {
		t.Errorf("%d rows, want 12", n)
	}
	if len(h.liercd.Calls()) != 0 {
		t.Error("dry run called liercd")
	}
}

func TestDeleteUsersDelete(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	other := seedUser(h, "bob")
	addArchive(h, u, "ann1")
	addArchive(h, other, "bob1")
	if err := h.run("delete-users", "--delete", "--settle", "2", u); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE connection IN ('ann1', 'ann2')`); n != 0 {
		t.Errorf("%d archive rows left for the deleted user", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE connection = 'bob1'`); n != 1 {
		t.Errorf("other user has %d archive rows, want 1", n)
	}
	h.has(u+": deleted\n", "deleted 1 user(s), skipped 0\n")
	if n := h.userRows(u, "ann1", "ann2"); n != 0 {
		t.Errorf("%d rows left for the deleted user", n)
	}
	if n := h.userRows(other, "bob1", "bob2"); n != 12 {
		t.Errorf("other user has %d rows, want 12", n)
	}
	if got := strings.Join(h.calls("POST", "/destroy"), ","); got != "ann1,ann2" {
		t.Errorf("destroyed %s", got)
	}
	if len(h.slept) != 1 || h.slept[0].Seconds() != 2 {
		t.Errorf("slept %v", h.slept)
	}
}

func TestDeleteUsersNoConnectionsDoesntWait(t *testing.T) {
	h := newHarness(t)
	u := h.user("solo")
	if err := h.run("delete-users", "--delete", u); err != nil {
		t.Fatal(err)
	}
	h.has(u + ": deleted\n")
	if len(h.slept) != 0 {
		t.Errorf("waited with no connections")
	}
}

func TestDeleteUsersUnknown(t *testing.T) {
	h := newHarness(t)
	if err := h.run("delete-users", "nobody"); err != nil {
		t.Fatal(err)
	}
	h.has("nobody: no such user, skipped\n")
}

func TestDeleteUsersLiercdRefuses(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	v := seedUser(h, "bob")
	h.liercd.Respond("POST", "/ann2/destroy", 500, "nok")
	if err := h.run("delete-users", "--delete", u, v); err == nil {
		t.Fatal("no error when a user was skipped")
	}
	h.has(u+": connection ann2: liercd said 500 Internal Server Error, skipping this user\n",
		v+": deleted\n", "deleted 1 user(s), skipped 1\n")
	if n := h.userRows(u, "ann1", "ann2"); n != 12 {
		t.Errorf("refused user has %d rows, want 12", n)
	}
}

func TestDeleteUsersCancelledDuringSettle(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	h.sleepErr = context.Canceled
	if err := h.run("delete-users", "--delete", u); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if n := h.userRows(u, "ann1", "ann2"); n != 12 {
		t.Errorf("%d rows after cancel, want 12", n)
	}
}

func TestDeleteUsersStdin(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader(u + "\n")
	if err := h.run("delete-users"); err != nil {
		t.Fatal(err)
	}
	h.has(u + " (ann, ann@example.com): ")
}

func TestDeleteUsersNeedsIDs(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("delete-users"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
}

// A QUIT the logger writes after a user's delete commits is swept up at the
// end; the sweep only looks at rows newer than the command's start.
func TestDeleteUsersSweepsLateLogRows(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	// stands in for the logger: a log row for ann1 lands as ann's delete commits
	h.exec(`CREATE FUNCTION late_quit() RETURNS trigger AS $$
	  BEGIN
	    INSERT INTO log (connection, "user", channel, time, message) VALUES ('ann1', OLD.id, 'status', now(), '{"Command":"QUIT"}');
	    RETURN OLD;
	  END $$ LANGUAGE plpgsql`)
	h.exec(`CREATE TRIGGER late_quit AFTER DELETE ON "user" FOR EACH ROW EXECUTE FUNCTION late_quit()`)
	if err := h.run("delete-users", "--delete", "--settle", "0", u); err != nil {
		t.Fatal(err)
	}
	h.has(u+": deleted\n", u+": removed 1 late log row(s)\n", "deleted 1 user(s), skipped 0\n")
	if n := h.count(`SELECT count(*) FROM log WHERE connection IN ('ann1', 'ann2')`); n != 0 {
		t.Errorf("%d log rows left", n)
	}
}

func TestDeleteUsersArchiveAfterLog(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	addOld(h, u, "ann1", "#a", 6)
	// stands in for the archiver: a block lands in the archive just after the
	// user's log rows are deleted (once), and every trigger notes its transaction
	h.exec(`CREATE TABLE trace (kind text, tx bigint)`)
	h.exec(`CREATE FUNCTION move_block() RETURNS trigger AS $$
	  BEGIN
	    IF NOT EXISTS (SELECT 1 FROM trace WHERE kind = 'moved') THEN
	      INSERT INTO trace VALUES ('moved', txid_current());
	      INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
	        VALUES ('ann1', '` + u + `', '#a', 1, 2, now(), now(), 2, '[]');
	    END IF;
	    RETURN NULL;
	  END $$ LANGUAGE plpgsql`)
	h.exec(`CREATE TRIGGER move_block AFTER DELETE ON log FOR EACH STATEMENT EXECUTE FUNCTION move_block()`)
	h.exec(`CREATE FUNCTION note_delete() RETURNS trigger AS $$
	  BEGIN
	    INSERT INTO trace VALUES ('deleted', txid_current());
	    RETURN OLD;
	  END $$ LANGUAGE plpgsql`)
	h.exec(`CREATE TRIGGER note_delete BEFORE DELETE ON log_archive FOR EACH ROW EXECUTE FUNCTION note_delete()`)
	if err := h.run("delete-users", "--delete", "--settle", "0", u); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM log_archive`); n != 0 {
		t.Errorf("%d archive rows left", n)
	}
	// the block must be deleted by the same transaction that deleted the log
	// rows, after them, not by a later sweep
	if n := h.count(`SELECT count(*) FROM trace d JOIN trace m ON m.kind = 'moved' AND m.tx = d.tx WHERE d.kind = 'deleted'`); n != 1 {
		t.Errorf("the moved block was not deleted in the transaction that deleted the log rows (%d)", n)
	}
}

// Logs and archive rows are also found by user id: a connection deleted
// before the user (or never in the connection table) leaves rows whose
// connection is gone but whose "user" is still theirs.
func TestDeleteUsersLogsOfGoneConnections(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	other := seedUser(h, "bob")
	for owner, gone := range map[string]string{u: "gone-ann", other: "gone-bob"} {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ($1, $2, '#a', now(), '{}')`, gone, owner)
		addArchive(h, owner, gone)
	}
	if err := h.run("delete-users", u); err != nil {
		t.Fatal(err)
	}
	h.has(u + " (ann, ann@example.com): log=3 ")
	if err := h.run("delete-users", "--delete", "--settle", "0", u); err != nil {
		t.Fatal(err)
	}
	if n := h.count(`SELECT count(*) FROM log WHERE "user" = $1`, u); n != 0 {
		t.Errorf("%d log rows left with the deleted user's id", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE "user" = $1`, u); n != 0 {
		t.Errorf("%d archive rows left with the deleted user's id", n)
	}
	if n := h.count(`SELECT count(*) FROM log WHERE "user" = $1`, other); n != 3 {
		t.Errorf("other user has %d log rows, want 3", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archive WHERE "user" = $1`, other); n != 1 {
		t.Errorf("other user has %d archive rows, want 1", n)
	}
}

// The late sweep also goes by user id.
func TestDeleteUsersSweepsLateRowsByUser(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	h.exec(`CREATE FUNCTION late_row() RETURNS trigger AS $$
	  BEGIN
	    INSERT INTO log (connection, "user", channel, time, message) VALUES ('gone', OLD.id, 'status', now(), '{}');
	    RETURN OLD;
	  END $$ LANGUAGE plpgsql`)
	h.exec(`CREATE TRIGGER late_row AFTER DELETE ON "user" FOR EACH ROW EXECUTE FUNCTION late_row()`)
	if err := h.run("delete-users", "--delete", "--settle", "0", u); err != nil {
		t.Fatal(err)
	}
	h.has(u + ": removed 1 late log row(s)\n")
	if n := h.count(`SELECT count(*) FROM log WHERE "user" = $1`, u); n != 0 {
		t.Errorf("%d log rows left", n)
	}
}

// Deleting a user announces each of their connections as deleted.
func TestDeleteUsersNotifiesEachConnection(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	other := h.user("bob")
	h.conn("c1", u, `{}`)
	h.conn("c2", u, `{}`)
	h.conn("c3", other, `{}`)
	h.liercd.Respond("POST", "/*/destroy", 200, "ok")
	if err := h.run("delete-users", "--delete", "--settle", "0", u); err != nil {
		t.Fatal(err)
	}
	conns := map[string]bool{}
	for _, c := range h.announced {
		if c.User != u || c.Action != "delete" {
			t.Errorf("change %+v", c)
		}
		conns[c.Connection] = true
	}
	if len(h.announced) != 2 || !conns["c1"] || !conns["c2"] {
		t.Errorf("announced %+v, want c1 and c2", h.announced)
	}
}

// interceptAdmin puts a server in front of the harness's admin server. Each
// request goes to intercept first; if that returns false it is passed through.
func interceptAdmin(h *harness, intercept func(w http.ResponseWriter, r *http.Request) bool) {
	real := h.env.Admin
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if intercept(w, r) {
			return
		}
		var body io.Reader
		req, _ := http.NewRequest(r.Method, real.BaseURL+r.URL.RequestURI(), body)
		res, err := real.HTTP.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		w.WriteHeader(res.StatusCode)
		io.Copy(w, res.Body)
	}))
	h.t.Cleanup(proxy.Close)
	h.env.Admin = &Admin{BaseURL: proxy.URL, HTTP: http.DefaultClient}
}

func hangUp(w http.ResponseWriter) {
	if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
		conn.Close()
	}
}

// With the admin server answering the lookups but not /admin/log/last, a
// --delete stops before liercd is touched: nothing destroyed, nothing deleted.
func TestDeleteUsersAdminDown(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	interceptAdmin(h, func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/admin/log/last" {
			hangUp(w)
			return true
		}
		return false
	})
	err := h.run("delete-users", "--delete", u)
	if !errors.Is(err, ErrUnreachable) || !strings.HasSuffix(err.Error(), "); nothing was changed") {
		t.Fatalf("got %v", err)
	}
	h.has(u + " (ann, ann@example.com): ") // the lookups were answered
	if calls := h.liercd.Calls(); len(calls) != 0 {
		t.Errorf("liercd was called: %v", calls)
	}
	if n := h.userRows(u, "ann1", "ann2"); n != 12 {
		t.Errorf("%d rows, want 12", n)
	}
}

// A delete whose request ends without an answer may have been done: the
// error says so rather than "nothing was changed".
func TestDeleteMayHaveFinished(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.conn("c1", u, cfg)
	interceptAdmin(h, func(w http.ResponseWriter, r *http.Request) bool {
		switch {
		case r.Method == http.MethodDelete:
			hangUp(w)
		case r.URL.Path == "/admin/log/last":
			w.Write([]byte(`{"id":7}`))
		default:
			return false
		}
		return true
	})
	h.liercd.Respond("POST", "/*/destroy", 200, "ok")

	const want = "the request ended before the API answered; the delete may have finished"
	for _, c := range [][]string{{"delete-users", "--delete", "--settle", "0", u}, {"delete-connections", "--delete", "--settle", "0", "c1"}} {
		err := h.run(c[0], c[1:]...)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", c[0], err, want)
		}
	}
}
