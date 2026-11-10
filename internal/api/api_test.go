package api

import (
	"bufio"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/stream"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard)
	testenv.Main(m)
}

const testSecret = "testsecret"

// fakePerl stands in for the Perl API: it answers 299 with X-Perl
// "<method> <path>".
func fakePerl() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Perl", r.Method+" "+r.URL.Path)
		w.WriteHeader(299)
	})
}

type env struct {
	t   *testing.T
	db  *db.DB
	srv *httptest.Server
}

func setup(t *testing.T) *env { return setupCfg(t, config.Config{Secret: testSecret}) }

func setupCfg(t *testing.T, cfg config.Config) *env {
	t.Helper()
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	lc := &liercd.Client{BaseURL: fake.URL, HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	s := stream.New(cfg, d, lc)
	srv := httptest.NewServer(New(cfg, d, s, fakePerl()))
	t.Cleanup(srv.Close)
	return &env{t: t, db: d, srv: srv}
}

// do sends method path, logged in as user unless user is "".
func (e *env) do(method, path, user string) *http.Response {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.srv.URL+path, nil)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(user, testSecret)})
	}
	c := &http.Client{
		Transport:     &http.Transport{DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	res, err := c.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	return res
}

// openStream opens a stream and returns its id from the first event.
func (e *env) openStream(path, user string) (*http.Response, string) {
	e.t.Helper()
	res := e.do("GET", path, user)
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream;charset=utf-8" || res.Header.Get("X-Perl") != "" {
		res.Body.Close()
		e.t.Fatalf("GET %s: %d %q %q, want a stream", path, res.StatusCode, res.Header.Get("Content-Type"), res.Header.Get("X-Perl"))
	}
	br := bufio.NewReader(res.Body)
	done := make(chan string, 1)
	go func() {
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				done <- ""
				return
			}
			if strings.HasPrefix(line, `data: {"id":"`) {
				done <- strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), `data: {"id":"`), `"}`)
				return
			}
		}
	}()
	select {
	case id := <-done:
		if id == "" {
			e.t.Fatal("stream ended before its stream event")
		}
		return res, id
	case <-time.After(10 * time.Second):
		e.t.Fatal("no stream event")
	}
	return nil, ""
}

func TestEventsIsAStream(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	for _, path := range []string{"/events", "/events/"} {
		res, _ := e.openStream(path, u)
		res.Body.Close()
	}
}

func TestEventsPing(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res, id := e.openStream("/events", u)
	defer res.Body.Close()
	ping := e.do("POST", "/events/"+id+"/ping", u)
	ping.Body.Close()
	if ping.StatusCode != 204 {
		t.Errorf("ping: %d, want 204", ping.StatusCode)
	}
}

func checkPerl(t *testing.T, e *env, method, path, user string) {
	t.Helper()
	res := e.do(method, path, user)
	res.Body.Close()
	want := method + " " + path
	if res.StatusCode != 299 || res.Header.Get("X-Perl") != want {
		t.Errorf("%s: %d %q, want Perl", want, res.StatusCode, res.Header.Get("X-Perl"))
	}
}

func TestLegacyPathsGoToPerl(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res, id := e.openStream("/events", u)
	defer res.Body.Close()
	checkPerl(t, e, "GET", "/", u)
	checkPerl(t, e, "POST", "/"+id+"/ping", u)
}

func TestOtherPathsGoToPerl(t *testing.T) {
	e := setup(t)
	for _, rq := range [][2]string{
		{"POST", "/auth"}, {"GET", "/eventsx"}, {"PUT", "/connection/c1"}, {"GET", "/log/5"}, {"GET", "/stats"},
	} {
		checkPerl(t, e, rq[0], rq[1], "")
	}
}
