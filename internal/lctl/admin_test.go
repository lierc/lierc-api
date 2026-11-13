package lctl

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestAdminClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/users/gone":
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"no such user"}`))
		case "/admin/users/bad":
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"that is wrong"}`))
		case "/admin/users/boom":
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":"internal error"}`))
		default:
			w.WriteHeader(http.StatusBadGateway)
			w.Write([]byte(`<html>`))
		}
	}))
	defer srv.Close()
	a := &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
	ctx := context.Background()

	if _, err := a.User(ctx, "gone"); !errors.Is(err, ErrNotFound) {
		t.Errorf("404: %v, want ErrNotFound", err)
	}
	if _, err := a.User(ctx, "bad"); err == nil || err.Error() != "that is wrong" || errors.Is(err, ErrNotFound) {
		t.Errorf("400: %v", err)
	}
	if _, err := a.User(ctx, "boom"); err == nil || err.Error() != "internal error" {
		t.Errorf("500: %v", err)
	}
	if _, err := a.User(ctx, "other"); err == nil || !strings.Contains(err.Error(), "502") {
		t.Errorf("non-JSON body: %v", err)
	}

	srv.Close()
	_, err := a.User(ctx, "x")
	want := "the API's admin server isn't reachable ("
	if !errors.Is(err, ErrUnreachable) || err == nil || !strings.HasPrefix(err.Error(), want) || !strings.HasSuffix(err.Error(), "); nothing was changed") {
		t.Errorf("closed: %v", err)
	}
}

func TestReadCommandsAdminDown(t *testing.T) {
	h := newHarness(t)
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	h.env.Admin = &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
	h.env.StatsURL = "http://127.0.0.1:1/stats" // never reached for the lookups below
	for _, c := range [][]string{
		{"user-info", "ann"},
		{"inactive-users", "--since", "1d"},
		{"connection-list"},
		{"connection-info", "c1"},
		{"logins", "ann"},
		{"users", "ann"},
	} {
		err := h.run(c[0], c[1:]...)
		if !errors.Is(err, ErrUnreachable) || !strings.HasPrefix(err.Error(), "the API's admin server isn't reachable (") ||
			!strings.HasSuffix(err.Error(), "); nothing was changed") {
			t.Errorf("%s: %v", c[0], err)
		}
	}
}

func TestConnectionInfoNoPassword(t *testing.T) {
	h := newHarness(t)
	u := h.user("Ann")
	h.conn("c1", u, `{"Host":"irc.example.com","Port":6667,"Nick":"ann","Pass":"hunter2"}`)
	for _, c := range [][]string{{"connection-info", "c1"}, {"user-info", "ann"}, {"connection-list"}} {
		if err := h.run(c[0], c[1:]...); err != nil {
			t.Fatalf("%s: %v", c[0], err)
		}
		h.lacks("hunter2")
		switch c[0] {
		case "user-info":
			h.has("server password set")
		case "connection-info":
			h.has("password:    set, sent with PASS")
		}
	}
}

// Deletes of big users can outlast any fixed bound, so they run without a
// client timeout; every other request keeps one.
func TestAdminTimeoutSkipsDeletes(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		w.Write([]byte(`{"id":1,"counts":{}}`))
	}))
	defer srv.Close()
	a := NewAdmin(srv.URL)
	a.Timeout = 50 * time.Millisecond
	ctx := context.Background()

	if _, err := a.DeleteUser(ctx, "u", 0); err != nil {
		t.Errorf("DeleteUser: %v", err)
	}
	if _, err := a.DeleteConnection(ctx, "c", 0); err != nil {
		t.Errorf("DeleteConnection: %v", err)
	}
	// a read that runs out of time is the server not answering: nothing
	// was changed
	_, err := a.LastLogID(ctx)
	if !errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("LastLogID: %v, want the unreachable error", err)
	}
	// a write that runs out of time may have been applied
	_, err = a.PruneLogins(ctx)
	if err == nil || errors.Is(err, ErrUnreachable) || !strings.Contains(err.Error(), "may have been made") {
		t.Errorf("PruneLogins: %v, want a may-have-been-made error", err)
	}
}

// instanceOf routes liercd calls by the admin API's view of a connection:
// next, main ("" too) and gone are answers; any other failure is an error,
// and a routed liercd call then fails without reaching liercd.
func TestInstanceOf(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/admin/connections/n":
			w.Write([]byte(`{"id":"n","instance":"next"}`))
		case "/admin/connections/m":
			w.Write([]byte(`{"id":"m","instance":"main"}`))
		case "/admin/connections/gone":
			w.WriteHeader(404)
			w.Write([]byte(`{"error":"no such connection"}`))
		default:
			w.WriteHeader(500)
			w.Write([]byte(`{"error":"internal error"}`))
		}
	}))
	defer srv.Close()
	a := &Admin{BaseURL: srv.URL, HTTP: http.DefaultClient}
	ctx := context.Background()
	for id, want := range map[string]string{"n": "next", "m": "", "gone": ""} {
		if got, err := a.instanceOf(ctx, id); got != want || err != nil {
			t.Errorf("instanceOf(%s) = %q, %v, want %q", id, got, err, want)
		}
	}
	if got, err := a.instanceOf(ctx, "broken"); err == nil || got != "" {
		t.Errorf("instanceOf(broken) = %q, %v, want an error", got, err)
	}

	fake := testenv.FakeLiercd(t)
	c := liercd.NewRoutedForTest(fake.URL, fake.URL, a.instanceOf)
	if _, err := c.Do(ctx, http.MethodGet, "broken/status", nil); err == nil || !strings.Contains(err.Error(), "internal error") {
		t.Errorf("routed call on a failed lookup: %v, want the lookup's error", err)
	}
	if calls := fake.Calls(); len(calls) != 0 {
		t.Errorf("liercd called %v after a failed lookup", calls)
	}
}
