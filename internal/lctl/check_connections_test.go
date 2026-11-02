package lctl

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const nickErr = "Nick must be up to 32 letters, digits, - or [ ] \\ ` _ ^ { | }, not starting with a digit or -"

// seedConnections adds the dev-stack e2e's six connections for one user.
func seedConnections(h *harness) string {
	h.vars["DEFAULT_NETWORK_HOST"] = "ergo"
	h.hosts["ergo"] = []string{"172.18.0.2"}
	u := h.user("cc")
	h.conn("good", u, `{"Host":"ergo","Port":6667,"Nick":"cc"}`)
	h.conn("bn", u, `{"Host":"ergo","Port":6667,"Nick":"2cool"}`)
	h.conn("bh", u, `{"Host":"127.0.0.1","Port":5005,"Nick":"x"}`)
	h.conn("bu", u, `{"Host":"nosuch.invalid","Port":6667,"Nick":"x"}`)
	h.conn("bj", u, `"not an object"`)
	h.conn("ba", u, `{"Host":"ergo","Port":6667,"Nick":"cca","Alias":"a.b"}`)
	return u
}

func TestCheckDryRun(t *testing.T) {
	h := newHarness(t)
	u := seedConnections(h)
	if err := h.run("check-connections"); err != nil {
		t.Fatal(err)
	}
	h.has(
		"bn (user "+u+", cc, ergo:6667): "+nickErr+"\n",
		"bh (user "+u+", cc, 127.0.0.1:5005): Host 127.0.0.1 points to a private address\n",
		"bu (user "+u+", cc, nosuch.invalid:6667): Couldn't find host nosuch.invalid\n",
		"bj (user "+u+", cc, ?): config isn't a JSON object\n",
		"ba (user "+u+", cc, ergo:6667): Network alias can only use letters, digits and spaces, up to 32 characters\n",
		"6 connection(s) checked: 1 valid, 5 invalid\n",
		"dry run: nothing deleted; run with --delete to delete the invalid ones\n",
	)
	h.lacks("good (")
	if n := h.count(`SELECT count(*) FROM connection`); n != 6 {
		t.Errorf("%d connections left, want 6", n)
	}
	if len(h.liercd.Calls()) != 0 {
		t.Errorf("dry run called liercd: %v", h.liercd.Calls())
	}
	// invalid lines come in id order
	if strings.Index(h.out(), "ba (") > strings.Index(h.out(), "bh (") {
		t.Errorf("not in id order:\n%s", h.out())
	}
}

func TestCheckNoDNS(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	if err := h.run("check-connections", "--no-dns"); err != nil {
		t.Fatal(err)
	}
	h.has("6 connection(s) checked: 3 valid, 3 invalid (field formats only, no DNS)\n")
	h.lacks("bh (", "bu (")
}

func TestCheckIDs(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	if err := h.run("check-connections", "bn", "good", "missing"); err != nil {
		t.Fatal(err)
	}
	h.has("missing: no such connection\n", "2 connection(s) checked: 1 valid, 1 invalid\n")
	h.lacks("bh (")
}

func TestCheckIDsFromStdin(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("bn\n\nbh\n")
	if err := h.run("check-connections"); err != nil {
		t.Fatal(err)
	}
	h.has("2 connection(s) checked: 0 valid, 2 invalid\n")
}

func TestCheckAllValid(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	if err := h.run("check-connections", "good"); err != nil {
		t.Fatal(err)
	}
	h.has("1 connection(s) checked: 1 valid, 0 invalid\n")
	h.lacks("dry run")
}

func TestCheckUserMissing(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "orphan", "nobody", `{"Host":"ergo","Port":6667,"Nick":"2x"}`)
	if err := h.run("check-connections", "--no-dns"); err != nil {
		t.Fatal(err)
	}
	h.has("orphan (user nobody, no such user, ergo:6667): " + nickErr + "\n")
}

func testenv_conn(h *harness, id, user, config string) {
	h.exec(`INSERT INTO connection (id, "user", config) VALUES ($1, $2, $3)`, id, user, config)
}

func TestCheckDisplaysRawHostPort(t *testing.T) {
	h := newHarness(t)
	u := h.user("cc")
	testenv_conn(h, "c1", u, `{"Host":"a b","Port":"66","Nick":"x"}`)
	testenv_conn(h, "c2", u, `{"Host":["x"],"Nick":"x"}`)
	testenv_conn(h, "c3", u, `{"Host":"a.com","Port":6667.0,"Nick":"2"}`)
	if err := h.run("check-connections", "--no-dns"); err != nil {
		t.Fatal(err)
	}
	h.has("c1 (user "+u+", cc, a b:66): ", "c2 (user "+u+", cc, ?:?): ", "c3 (user "+u+", cc, a.com:6667.0): ")
}

func TestCheckDeleteKeepsUnresolved(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.liercd.Respond("POST", "/*/destroy", 200, "ok")
	if err := h.run("check-connections", "--delete", "--settle", "7"); err != nil {
		t.Fatal(err)
	}
	h.has(
		"1 unresolved host(s) kept; add --delete-unresolved to delete them\n",
		"ba: deleted (log=1 seen=1 private=1 connection=1)\n",
		"bn: deleted (log=1 seen=1 private=1 connection=1)\n",
		"deleted 4 connection(s), skipped 0\n",
	)
	if got := strings.Join(h.calls("POST", "/destroy"), ","); got != "ba,bh,bj,bn" {
		t.Errorf("destroyed %s", got)
	}
	if len(h.slept) != 1 || h.slept[0].Seconds() != 7 {
		t.Errorf("slept %v, want 7s once", h.slept)
	}
	for _, table := range []string{"log", "seen", "private"} {
		if n := h.count(`SELECT count(*) FROM ` + table + ` WHERE connection NOT IN ('good', 'bu')`); n != 0 {
			t.Errorf("%s: %d rows left for deleted connections", table, n)
		}
		if n := h.count(`SELECT count(*) FROM ` + table); n != 2 {
			t.Errorf("%s: %d rows, want 2 (good, bu)", table, n)
		}
	}
	if n := h.count(`SELECT count(*) FROM connection WHERE id IN ('good', 'bu')`); n != 2 {
		t.Errorf("valid or unresolved connection deleted")
	}
}

func TestCheckDeleteUnresolved(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	if err := h.run("check-connections", "--delete", "--delete-unresolved", "bu"); err != nil {
		t.Fatal(err)
	}
	h.has("bu: deleted (log=1 seen=1 private=1 connection=1)\n", "deleted 1 connection(s), skipped 0\n")
}

func TestCheckLiercdRefuses(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.liercd.Respond("POST", "/bn/destroy", 500, "nok")
	err := h.run("check-connections", "--delete", "bn", "bh")
	if err == nil {
		t.Fatal("no error when a connection was skipped")
	}
	h.has("bn: liercd said 500 Internal Server Error, skipped\n", "bh: deleted (", "deleted 1 connection(s), skipped 1\n")
	if n := h.count(`SELECT count(*) FROM connection WHERE id = 'bn'`); n != 1 {
		t.Error("refused connection was deleted")
	}
}

func TestCheckLiercdUnreachable(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.env.Liercd.BaseURL = "http://127.0.0.1:1"
	if err := h.run("check-connections", "--delete"); err == nil {
		t.Fatal("no error")
	}
	h.has("bn: liercd said ", "deleted 0 connection(s), skipped 4\n")
	if len(h.slept) != 0 {
		t.Error("waited with nothing to delete")
	}
	if n := h.count(`SELECT count(*) FROM connection`); n != 6 {
		t.Errorf("%d connections left, want 6", n)
	}
}

func TestCheckCancelledDuringSettle(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.sleepErr = context.Canceled
	if err := h.run("check-connections", "--delete"); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if n := h.count(`SELECT count(*) FROM connection`); n != 6 {
		t.Errorf("%d connections left after cancel, want 6", n)
	}
	if n := h.count(`SELECT count(*) FROM log`); n != 6 {
		t.Errorf("%d log rows left after cancel, want 6", n)
	}
}

func TestCheckLogDuringSettleIsDeleted(t *testing.T) {
	h := newHarness(t)
	u := seedConnections(h)
	// the QUIT a destroyed connection logs while the command waits
	h.beforeSleep = func() {
		h.exec(`INSERT INTO log (connection, "user", channel, time, message) VALUES ('bn', $1, 'status', now(), '{}')`, u)
	}
	if err := h.run("check-connections", "--delete", "bn"); err != nil {
		t.Fatal(err)
	}
	h.has("bn: deleted (log=2 seen=1 private=1 connection=1)\n")
}

func TestCheckFlagErrors(t *testing.T) {
	h := newHarness(t)
	var u *UsageError
	if err := h.run("check-connections", "--delete-unresolved"); !errors.As(err, &u) || u.Msg != "--delete-unresolved needs --delete" {
		t.Errorf("--delete-unresolved alone: %v", err)
	}
	if err := h.run("check-connections", "--delete", "--no-dns"); !errors.As(err, &u) ||
		u.Msg != "--delete with --no-dns would skip the address check; pass connection ids or drop --no-dns" {
		t.Errorf("--delete --no-dns: %v", err)
	}
	if err := h.run("check-connections", "--settle", "-1"); !errors.As(err, &u) {
		t.Errorf("negative settle: %v", err)
	}
	if err := h.run("check-connections", "--delete", "--no-dns", "x"); err != nil {
		t.Errorf("--delete --no-dns with ids: %v", err)
	}
}

// `grep … | lctl check-connections --delete` with no matches must not check
// (and delete) every connection.
func TestCheckEmptyStdinRefuses(t *testing.T) {
	h := newHarness(t)
	seedConnections(h)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("")
	var ue *UsageError
	if err := h.run("check-connections", "--delete"); !errors.As(err, &ue) || !strings.Contains(ue.Msg, "no ids on stdin") {
		t.Fatalf("got %v", err)
	}
	if n := h.count(`SELECT count(*) FROM connection`); n != 6 {
		t.Errorf("%d connections left, want 6", n)
	}
	if len(h.liercd.Calls()) != 0 {
		t.Error("called liercd")
	}
}
