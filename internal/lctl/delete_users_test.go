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
	h.exec(`INSERT INTO log_archives (connection_id, user_id, channel, first_id, last_id, min_time, max_time, count, messages)
	  VALUES ($1, $2, '#a', 1, 2, now(), now(), 2, '[]')`, conn, user)
}

// seedUser adds a user with two connections and a row in every table.
func seedUser(h *harness, name string) string {
	u := h.user(name)
	h.conn(name+"1", u, `{"Host":"ergo","Port":6667,"Nick":"x"}`)
	h.conn(name+"2", u, `{"Host":"ergo","Port":6667,"Nick":"y"}`)
	h.exec(`INSERT INTO seen_markers (user_id, connection_id, channel, message_id) VALUES ($1, $2, '#x', 1)`, u, name+"1")
	h.exec(`INSERT INTO prefs (user_id, name, value) VALUES ($1, 'theme', 'dark')`, u)
	h.exec(`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES ('https://push/x', 'k', 'a', $1)`, u)
	return u
}

func (h *harness) userRows(u, c1, c2 string) int {
	return h.count(`SELECT (SELECT count(*) FROM logs WHERE connection_id IN ($2, $3))
	  + (SELECT count(*) FROM seen_markers WHERE user_id = $1 OR connection_id IN ($2, $3))
	  + (SELECT count(*) FROM private_chats WHERE connection_id IN ($2, $3))
	  + (SELECT count(*) FROM prefs WHERE user_id = $1)
	  + (SELECT count(*) FROM web_push_subscriptions WHERE user_id = $1)
	  + (SELECT count(*) FROM connections WHERE user_id = $1)
	  + (SELECT count(*) FROM users WHERE id = $1)`, u, c1, c2)
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
	if n := h.count(`SELECT count(*) FROM log_archives WHERE connection_id IN ('ann1', 'ann2')`); n != 0 {
		t.Errorf("%d archive rows left for the deleted user", n)
	}
	if n := h.count(`SELECT count(*) FROM log_archives WHERE connection_id = 'bob1'`); n != 1 {
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

// With the admin server not answering the lookups, a --delete stops before
// liercd is touched: nothing destroyed, nothing deleted.
func TestDeleteUsersAdminDown(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	interceptAdmin(h, func(w http.ResponseWriter, r *http.Request) bool {
		hangUp(w)
		return true
	})
	err := h.run("delete-users", "--delete", u)
	if !errors.Is(err, ErrUnreachable) || !strings.HasSuffix(err.Error(), "); nothing was changed") {
		t.Fatalf("got %v", err)
	}
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

// A user deleted by someone else between the lookup and the delete is
// reported as already gone, not as a failure.
func TestDeleteUsersAlreadyGone(t *testing.T) {
	h := newHarness(t)
	u := seedUser(h, "ann")
	h.beforeSleep = func() {
		h.exec(`DELETE FROM connections WHERE user_id = $1`, u)
		h.exec(`DELETE FROM users WHERE id = $1`, u)
	}
	if err := h.run("delete-users", "--delete", u); err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	h.has(u+": already gone\n", "deleted 1 user(s), skipped 0\n")
	if strings.Contains(h.out(), "failed") {
		t.Errorf("output says failed:\n%s", h.out())
	}
}
