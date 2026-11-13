package lctl

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
)

// listSetup adds a1 (main, connected), a2 (main, empty column, not
// running), b1 (next, running but not connected) and c1 (next, its owner
// gone).
func listSetup(m *moveHarness) {
	u := m.user("alice")
	m.conn("a1", u, `{"Host":"irc.libera.chat","Port":6697}`)
	m.conn("a2", u, `{"Host":"irc.oftc.net","Port":6667}`)
	m.conn("b1", u, `"not an object"`)
	testenv_conn(m.harness, "c1", "nobody", `{"Host":"irc.relaychat.party","Port":6697}`)
	m.exec(`UPDATE connection SET liercd='' WHERE id='a2'`)
	m.exec(`UPDATE connection SET liercd='next' WHERE id IN ('b1', 'c1')`)
	m.main.MarkRunning("a1")
	m.main.SetStatus("a1", `{"Nick":"alice","Status":{"Connected":true}}`)
	m.next.MarkRunning("b1")
	m.next.SetStatus("b1", `{"Nick":"alice_","Status":{"Connected":false}}`)
}

func TestConnectionList(t *testing.T) {
	m := newMoveHarness(t)
	listSetup(m)
	if err := m.run("connection-list"); err != nil {
		t.Fatal(err)
	}
	want := "a1  main  alice  irc.libera.chat:6697  connected as alice\n" +
		"a2  main  alice  irc.oftc.net:6667  not running\n" +
		"b1  next  alice  ?  running, not connected\n" +
		"c1  next  nobody (no such user)  irc.relaychat.party:6697  not running\n"
	if got := m.out(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if got := m.stderr.String(); got != "4 connection(s)\n" {
		t.Errorf("stderr = %q", got)
	}
}

func TestConnectionListInstance(t *testing.T) {
	m := newMoveHarness(t)
	listSetup(m)
	if err := m.run("connection-list", "--instance", "main"); err != nil {
		t.Fatal(err)
	}
	m.has("a1  main", "a2  main")
	m.lacks("b1", "c1")
	if err := m.run("connection-list", "--instance", "next"); err != nil {
		t.Fatal(err)
	}
	m.has("b1  next", "c1  next")
	m.lacks("a1", "a2")
}

func TestConnectionListUsage(t *testing.T) {
	m := newMoveHarness(t)
	var ue *UsageError
	if err := m.run("connection-list", "--instance", "other"); !asUsage(err, &ue) {
		t.Errorf("bad instance: %v", err)
	}
	if err := m.run("connection-list", "x"); !asUsage(err, &ue) {
		t.Errorf("argument: %v", err)
	}
}

// countingStore counts ConnectionCounts calls (the four row counts a
// connection delete's dry run needs).
type countingStore struct {
	db.Store
	counts atomic.Int32
}

func (s *countingStore) ConnectionCounts(ctx context.Context, id string) (map[string]int64, error) {
	s.counts.Add(1)
	return s.Store.ConnectionCounts(ctx, id)
}

// Routing liercd calls through the admin API (as the admin commands do,
// having no database) looks up each connection's instance without counting
// its rows.
func TestRoutingSkipsCounts(t *testing.T) {
	m := newMoveHarness(t)
	listSetup(m)
	cs := &countingStore{Store: m.db}
	srv := httptest.NewServer(admin.New(cs, m.announce))
	t.Cleanup(srv.Close)
	m.env.Admin = &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
	m.env.Liercd = liercd.NewRoutedForTest(m.mainURL, m.nextURL, m.env.Admin.instanceOf)
	if err := m.run("connection-list"); err != nil {
		t.Fatal(err)
	}
	want := "a1  main  alice  irc.libera.chat:6697  connected as alice\n" +
		"a2  main  alice  irc.oftc.net:6667  not running\n" +
		"b1  next  alice  ?  running, not connected\n" +
		"c1  next  nobody (no such user)  irc.relaychat.party:6697  not running\n"
	if got := m.out(); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if n := cs.counts.Load(); n != 0 {
		t.Errorf("%d ConnectionCounts calls while routing, want 0", n)
	}
}
