package lctl

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

const cfg = `{"Host":"ergo","Port":6667,"Nick":"x"}`

func TestReconnect(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	h.liercd.Respond("POST", "/c1/destroy", 200, "ok")
	if err := h.run("reconnect", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("reconnected c1\n", "reconnected 1, failed 0\n")
	calls := h.liercd.Calls()
	if len(calls) != 2 || calls[0].Path != "/c1/destroy" || calls[1].Path != "/c1/create" || !testenv.SameJSON(calls[1].Body, cfg) {
		t.Fatalf("calls %+v", calls)
	}
}

// Several ids are reconnected in order, each destroyed before it's
// created again.
func TestReconnectSeveral(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	testenv_conn(h, "c2", u, `{"Host":"ergo","Port":6667,"Nick":"y"}`)
	if err := h.run("reconnect", "c1", "c2"); err != nil {
		t.Fatal(err)
	}
	h.has("reconnected c1\n", "reconnected c2\n", "reconnected 2, failed 0\n")
	var got []string
	for _, c := range h.liercd.Calls() {
		got = append(got, c.Path)
	}
	if strings.Join(got, " ") != "/c1/destroy /c1/create /c2/destroy /c2/create" {
		t.Fatalf("calls %v", got)
	}
}

func TestReconnectIDsFromStdin(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	testenv_conn(h, "c2", u, cfg)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("c1\n\nc2\n")
	if err := h.run("reconnect"); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(h.calls("POST", "/create"), ","); got != "c1,c2" {
		t.Fatalf("created %s", got)
	}
}

func TestReconnectNotRunning(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	if err := h.run("reconnect", "c1"); err != nil { // destroy is 404
		t.Fatal(err)
	}
}

// A connection that fails is reported and the rest are still reconnected.
func TestReconnectCarriesOn(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	testenv_conn(h, "c1", u, cfg)
	testenv_conn(h, "c2", u, cfg)
	testenv_conn(h, "c3", u, cfg)
	h.liercd.Respond("POST", "/c1/destroy", 500, "nok")
	h.liercd.Respond("POST", "/c2/create", 400, "nok")
	err := h.run("reconnect", "c1", "nope", "c2", "c3")
	if err == nil || err.Error() != "3 of 4 connection(s) failed" {
		t.Fatalf("got %v", err)
	}
	h.has("destroying c1: liercd said 500 Internal Server Error\n",
		"unknown connection nope\n",
		"creating c2: liercd said 400 Bad Request: nok\n",
		"reconnected c3\n", "reconnected 1, failed 3\n")
	if got := strings.Join(h.calls("POST", "/create"), ","); got != "c2,c3" {
		t.Errorf("created %s; not c1 after its failed destroy", got)
	}
}

func TestReconnectUsage(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("reconnect"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("\n")
	if err := h.run("reconnect"); !errors.As(err, &ue) {
		t.Fatalf("empty stdin: got %v", err)
	}
}

func seedRevive(h *harness) {
	recent, old := h.user("recent"), h.user("old")
	h.exec(`UPDATE "user" SET last_login = now() - interval '1 hour' WHERE id = $1`, recent)
	h.exec(`UPDATE "user" SET last_login = now() - interval '3 days' WHERE id = $1`, old)
	testenv_conn(h, "running", recent, cfg)
	testenv_conn(h, "stopped", recent, cfg)
	testenv_conn(h, "stale", old, cfg)
	if _, err := h.env.Liercd.Do(h.t.Context(), "POST", "running/create", []byte(cfg)); err != nil {
		h.t.Fatal(err)
	}
}

func TestReviveStartsOnlyMissing(t *testing.T) {
	h := newHarness(t)
	seedRevive(h)
	if err := h.run("revive"); err != nil {
		t.Fatal(err)
	}
	h.has("started stopped\n", "started 1, failed 0\n")
	if got := strings.Join(h.calls("POST", "/create"), ","); got != "running,stopped" {
		t.Fatalf("created %s", got)
	}
}

func TestReviveSince(t *testing.T) {
	h := newHarness(t)
	seedRevive(h)
	if err := h.run("revive", "--since", "96h"); err != nil {
		t.Fatal(err)
	}
	h.has("started stale\n", "started stopped\n", "started 2, failed 0\n")
}

func TestReviveCreateFails(t *testing.T) {
	h := newHarness(t)
	seedRevive(h)
	h.liercd.Respond("POST", "/stopped/create", 400, "nok")
	if err := h.run("revive"); err == nil {
		t.Fatal("no error")
	}
	h.has("stopped: 400 Bad Request\n", "started 0, failed 1\n")
}

func TestReviveBadSince(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("revive", "--since", "soon"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
	}
}

// Starts are paced in batches; connections already running don't count
// toward a batch.
func TestRevivePaced(t *testing.T) {
	h := newHarness(t)
	seedRevive(h)
	u := h.user("more")
	for _, id := range []string{"m1", "m2", "m3", "m4"} {
		testenv_conn(h, id, u, cfg)
	}
	if err := h.run("revive", "--batch", "2", "--pause", "10s"); err != nil {
		t.Fatal(err)
	}
	// m1..m4 and stopped are five starts in batches of two: two pauses
	if len(h.slept) != 2 || h.slept[0] != 10*time.Second || h.slept[1] != 10*time.Second {
		t.Errorf("slept %v, want two 10s pauses", h.slept)
	}
	h.has("pausing 10s\n", "started 5, failed 0\n")
}

// --instance starts only that liercd's connections; main is a NULL or
// empty column.
func TestReviveInstance(t *testing.T) {
	m := newMoveHarness(t)
	u := m.user("alice")
	for _, id := range []string{"a", "b", "n"} {
		m.conn(id, u, cfg)
	}
	m.exec(`UPDATE connection SET liercd = 'next' WHERE id = 'n'`)
	m.exec(`UPDATE connection SET liercd = '' WHERE id = 'b'`)
	if err := m.run("revive", "--instance", "next"); err != nil {
		t.Fatal(err)
	}
	if !m.next.Running("n") || m.main.Running("a") || m.main.Running("b") {
		t.Errorf("next: started %v; main a %v b %v", m.next.Running("n"), m.main.Running("a"), m.main.Running("b"))
	}
	m.has("started n\n", "started 1, failed 0\n")
	if err := m.run("revive", "--instance", "main"); err != nil {
		t.Fatal(err)
	}
	if !m.main.Running("a") || !m.main.Running("b") {
		t.Errorf("main: a %v b %v", m.main.Running("a"), m.main.Running("b"))
	}
	m.has("started a\n", "started b\n", "started 2, failed 0\n")
}

// --wait retries until the instance answers, then revives; if it never
// answers, it fails without starting anything.
func TestReviveWait(t *testing.T) {
	h := newHarness(t)
	seedRevive(h)
	// a liercd still starting: the first probes get no answer
	probes := 0
	starting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/portmap" {
			if probes++; probes <= 2 {
				conn, _, _ := w.(http.Hijacker).Hijack()
				conn.Close()
				return
			}
		}
		h.liercd.ServeHTTP(w, r)
	}))
	t.Cleanup(starting.Close)
	h.env.Liercd = &liercd.Client{BaseURL: starting.URL, HTTP: http.DefaultClient}
	if err := h.run("revive", "--instance", "main", "--wait", "1m"); err != nil {
		t.Fatal(err)
	}
	if probes != 3 || len(h.slept) != 2 {
		t.Errorf("%d probes, slept %v; want 3 and two 2s waits", probes, h.slept)
	}
	h.has("started stopped\n")

	h = newHarness(t)
	seedRevive(h)
	h.env.Liercd = &liercd.Client{BaseURL: "http://127.0.0.1:1", HTTP: http.DefaultClient}
	err := h.run("revive", "--instance", "main", "--wait", "6s")
	if err == nil || !strings.Contains(err.Error(), "didn't answer within 6s") {
		t.Fatalf("err = %v", err)
	}
	if len(h.slept) != 3 {
		t.Errorf("slept %v, want three 2s tries", h.slept)
	}
	if got := h.calls("POST", "/create"); len(got) != 1 {
		t.Errorf("created %v; only seedRevive's", got)
	}
}

func TestReviveUsageErrors(t *testing.T) {
	h := newHarness(t)
	for _, args := range [][]string{
		{"--instance", "other"},
		{"--batch", "0"},
		{"--pause", "-1s"},
		{"--wait", "-1s"},
		{"--wait", "1m"},
	} {
		var ue *UsageError
		if err := h.run("revive", args...); !errors.As(err, &ue) {
			t.Errorf("%v: got %v", args, err)
		}
	}
}
