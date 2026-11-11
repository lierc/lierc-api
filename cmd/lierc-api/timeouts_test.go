package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/api"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/stream"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestNewServerTimeouts(t *testing.T) {
	s := newServer(http.NotFoundHandler(), serverTimeouts)
	if s.ReadHeaderTimeout != 10*time.Second || s.ReadTimeout != 30*time.Second || s.IdleTimeout != 120*time.Second || s.WriteTimeout != 0 {
		t.Errorf("timeouts = %v %v %v write %v", s.ReadHeaderTimeout, s.ReadTimeout, s.IdleTimeout, s.WriteTimeout)
	}
}

var short = timeouts{ReadHeader: 200 * time.Millisecond, Read: 200 * time.Millisecond, Idle: 200 * time.Millisecond}

// serve runs h behind newServer with short timeouts, returning its URL.
func serve(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(h, short)
	go s.Serve(ln)
	t.Cleanup(func() { s.Close() })
	return "http://" + ln.Addr().String()
}

// A response that keeps writing for far longer than every timeout isn't cut
// off: there is no write timeout, and the read deadline is cleared once the
// handler runs.
func TestStreamOutlivesTimeouts(t *testing.T) {
	url := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for i := 0; i < 15; i++ {
			io.WriteString(w, "tick\n")
			rc.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	n := 0
	for sc.Scan() {
		n++
	}
	if err := sc.Err(); err != nil || n != 15 {
		t.Errorf("%d ticks, %v; want 15", n, err)
	}
}

// The real event stream, wired as main wires it, still delivers a wake
// ping well past the short timeouts.
func TestRealStreamOutlivesTimeouts(t *testing.T) {
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	cfg := config.Config{Secret: "testsecret"}
	lc := &liercd.Client{BaseURL: fake.URL, HTTP: &http.Client{}}
	s := stream.New(cfg, d, lc)
	s.PingInterval = time.Hour
	url := serve(t, api.New(cfg, d, s, lc))
	u := testenv.AddUser(t, d, "alice")
	cookie := &http.Cookie{Name: "chats", Value: testenv.PerlCookie(u, cfg.Secret)}

	req, _ := http.NewRequest("GET", url+"/events", nil)
	req.AddCookie(cookie)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	lines := make(chan string, 100)
	go func() {
		br := bufio.NewReader(res.Body)
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- l
		}
	}()
	var id string
	for id == "" {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("stream ended before its stream event")
			}
			if strings.HasPrefix(l, `data: {"id":"`) {
				id = strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(l), `data: {"id":"`), `"}`)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no stream event")
		}
	}
	time.Sleep(time.Second) // 5x every timeout
	ping, _ := http.NewRequest("POST", fmt.Sprintf("%s/events/%s/ping", url, id), nil)
	ping.AddCookie(cookie)
	pres, err := http.DefaultClient.Do(ping)
	if err != nil || pres.StatusCode != 204 {
		t.Fatalf("ping: %v %v", pres, err)
	}
	pres.Body.Close()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("the stream was cut off")
			}
			if strings.HasPrefix(l, "event: ping") {
				return
			}
		case <-time.After(5 * time.Second):
			t.Fatal("no ping after the timeouts passed")
		}
	}
}
