package lctl

import (
	"errors"
	"strings"
	"testing"
)

const cfg = `{"Host":"ergo","Port":6667,"Nick":"x"}`

func TestReconnect(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	h.liercd.Respond("POST", "/c1/destroy", 200, "ok")
	if err := h.run("reconnect", "c1"); err != nil {
		t.Fatal(err)
	}
	h.has("reconnected c1\n")
	calls := h.liercd.Calls()
	if len(calls) != 2 || calls[0].Path != "/c1/destroy" || calls[1].Path != "/c1/create" || calls[1].Body != cfg {
		t.Fatalf("calls %+v", calls)
	}
}

func TestReconnectNotRunning(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	if err := h.run("reconnect", "c1"); err != nil { // destroy is 404
		t.Fatal(err)
	}
}

func TestReconnectUnknown(t *testing.T) {
	h := newHarness(t)
	if err := h.run("reconnect", "nope"); err == nil || err.Error() != "unknown connection nope" {
		t.Fatalf("got %v", err)
	}
}

func TestReconnectDestroyFails(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	h.liercd.Respond("POST", "/c1/destroy", 500, "nok")
	if err := h.run("reconnect", "c1"); err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("got %v", err)
	}
	if len(h.calls("POST", "/create")) != 0 {
		t.Error("created after a failed destroy")
	}
}

func TestReconnectCreateFails(t *testing.T) {
	h := newHarness(t)
	testenv_conn(h, "c1", h.user("ann"), cfg)
	h.liercd.Respond("POST", "/c1/create", 400, "nok")
	if err := h.run("reconnect", "c1"); err == nil || !strings.Contains(err.Error(), "400 Bad Request: nok") {
		t.Fatalf("got %v", err)
	}
}

func TestReconnectUsage(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	if err := h.run("reconnect"); !errors.As(err, &ue) {
		t.Fatalf("got %v", err)
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
