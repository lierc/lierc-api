package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestPassThroughRequest(t *testing.T) {
	var seen struct{ method, path, query, body, host, xff string }
	perl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		seen.method, seen.path, seen.query, seen.body = r.Method, r.URL.Path, r.URL.RawQuery, string(b)
		seen.host, seen.xff = r.Host, r.Header.Get("X-Forwarded-For")
		http.SetCookie(w, &http.Cookie{Name: "chats", Value: "abc", Path: "/"})
		w.Header().Set("X-Perl", "yes")
		w.WriteHeader(201)
		io.WriteString(w, "made")
	}))
	defer perl.Close()

	req := httptest.NewRequest("POST", "/connection/c1?x=1", strings.NewReader("hi"))
	req.Host = "relaychat.party"
	req.RemoteAddr = "127.0.0.1:4444"
	req.Header.Set("X-Forwarded-For", "6.6.6.6, 1.2.3.4")
	rec := httptest.NewRecorder()
	PassThrough(mustURL(t, perl.URL)).ServeHTTP(rec, req)

	if seen.method != "POST" || seen.path != "/connection/c1" || seen.query != "x=1" || seen.body != "hi" {
		t.Errorf("Perl got %s %s?%s %q", seen.method, seen.path, seen.query, seen.body)
	}
	if seen.host != "relaychat.party" {
		t.Errorf("Perl got Host %q, want relaychat.party", seen.host)
	}
	if seen.xff != "127.0.0.1" {
		t.Errorf("Perl got X-Forwarded-For %q, want only the client address", seen.xff)
	}
	res := rec.Result()
	if b, _ := io.ReadAll(res.Body); res.StatusCode != 201 || string(b) != "made" ||
		res.Header.Get("X-Perl") != "yes" || !strings.HasPrefix(res.Header.Get("Set-Cookie"), "chats=abc") {
		t.Errorf("client got %d %q %v", res.StatusCode, b, res.Header)
	}
}

func TestPassThroughFlushes(t *testing.T) {
	release := make(chan struct{})
	perl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
		io.WriteString(w, "event: a\n\n")
		w.(http.Flusher).Flush()
		<-release
	}))
	defer perl.Close()
	defer close(release)
	front := httptest.NewServer(PassThrough(mustURL(t, perl.URL)))
	defer front.Close()

	res, err := http.Get(front.URL + "/connection/c1/channel/%23a/date/2026-11-01/2026-11-02")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	line := make(chan string, 1)
	go func() {
		l, _ := bufio.NewReader(res.Body).ReadString('\n')
		line <- l
	}()
	select {
	case l := <-line:
		if l != "event: a\n" {
			t.Errorf("first line %q", l)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing arrived while Perl was still writing")
	}
}

func TestPassThroughPerlDown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	rec := httptest.NewRecorder()
	PassThrough(mustURL(t, "http://"+addr)).ServeHTTP(rec, httptest.NewRequest("GET", "/auth", nil))
	res := rec.Result()
	var body map[string]string
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 502 || res.Header.Get("Content-Type") != "application/javascript;charset=utf-8" ||
		body["status"] != "error" || body["error"] != "API unavailable" || len(body) != 2 {
		t.Errorf("got %d %s %v", res.StatusCode, res.Header.Get("Content-Type"), body)
	}
}
