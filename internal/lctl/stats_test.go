package lctl

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// statsServer stands in for the stream server's /stats.
func statsServer(h *harness, counts map[string]int, streams map[string][]map[string]any) {
	h.env.Config.Key = "the-key"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Lierc-Key") != "the-key" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"Invalid key"}`))
			return
		}
		if u := r.URL.Query().Get("user"); u != "" {
			s := streams[u]
			if s == nil {
				s = []map[string]any{}
			}
			json.NewEncoder(w).Encode(s)
			return
		}
		json.NewEncoder(w).Encode(counts)
	}))
	h.t.Cleanup(srv.Close)
	h.env.StatsURL = srv.URL + "/stats"
}

func TestStats(t *testing.T) {
	h := newHarness(t)
	a, b, c := h.user("ann"), h.user("bob"), h.user("cy")
	statsServer(h, map[string]int{a: 1, b: 3, c: 1, "ghost": 2}, nil)
	if err := h.run("stats"); err != nil {
		t.Fatal(err)
	}
	want := "3 bob (" + b + ")\n2 ? (ghost)\n1 ann (" + a + ")\n1 cy (" + c + ")\n"
	if h.out() != want {
		t.Fatalf("got\n%s\nwant\n%s", h.out(), want)
	}
}

func TestStatsBadKey(t *testing.T) {
	h := newHarness(t)
	statsServer(h, nil, nil)
	h.env.Config.Key = "wrong"
	if err := h.run("stats"); err == nil || !strings.Contains(err.Error(), "400") {
		t.Fatalf("got %v", err)
	}
}

func TestUsersNamed(t *testing.T) {
	h := newHarness(t)
	a := h.user("Ann")
	now := time.Unix(1_800_000_000, 0)
	h.env.Now = func() time.Time { return now }
	agent := "Firefox"
	statsServer(h, nil, map[string][]map[string]any{a: {
		{"remote": "1.2.3.4", "agent": agent, "created": now.Add(-3*time.Hour - 5*time.Minute).Unix()},
		{"remote": "1.2.3.4", "agent": nil, "created": now.Add(-42 * time.Minute).Unix()},
	}})
	if err := h.run("users", "ann"); err != nil {
		t.Fatal(err)
	}
	want := "Ann - [3 hours] \"Firefox\"\nAnn - [42 minutes] \"-\"\n"
	if h.out() != want {
		t.Fatalf("got\n%s\nwant\n%s", h.out(), want)
	}
}

func TestUsersAll(t *testing.T) {
	h := newHarness(t)
	a, b := h.user("bob"), h.user("ann")
	now := time.Unix(1_800_000_000, 0)
	h.env.Now = func() time.Time { return now }
	s := func() []map[string]any {
		return []map[string]any{{"remote": "x", "agent": "A", "created": now.Add(-time.Minute).Unix()}}
	}
	statsServer(h, map[string]int{a: 1, b: 1}, map[string][]map[string]any{a: s(), b: s()})
	if err := h.run("users"); err != nil {
		t.Fatal(err)
	}
	if h.out() != "ann - [1 minutes] \"A\"\nbob - [1 minutes] \"A\"\n" {
		t.Fatalf("got\n%s", h.out())
	}
}

func TestUsersUnknown(t *testing.T) {
	h := newHarness(t)
	statsServer(h, nil, nil)
	if err := h.run("users", "nobody"); err == nil || err.Error() != "unknown user nobody" {
		t.Fatalf("got %v", err)
	}
}
