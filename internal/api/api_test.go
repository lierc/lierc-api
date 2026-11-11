package api

import (
	"bufio"
	"context"
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
	t    *testing.T
	db   *db.DB
	srv  *httptest.Server
	fake *testenv.LiercdServer
	s    *stream.Server
}

func setup(t *testing.T) *env { return setupCfg(t, config.Config{Secret: testSecret}) }

func setupCfg(t *testing.T, cfg config.Config) *env {
	t.Helper()
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	lc := &liercd.Client{BaseURL: fake.URL, HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	s := stream.New(cfg, d, lc)
	old := checkHost
	checkHost = func(context.Context, string, int) error { return nil }
	t.Cleanup(func() { checkHost = old })
	srv := httptest.NewServer(New(cfg, d, s, lc, fakePerl()))
	t.Cleanup(srv.Close)
	return &env{t: t, db: d, srv: srv, fake: fake, s: s}
}

// do sends method path, logged in as user unless user is "".
func (e *env) do(method, path, user string) *http.Response {
	e.t.Helper()
	return e.doReq(method, path, user, "")
}

// doReq sends method path with body and header name/value pairs, logged
// in as user unless user is "".
func (e *env) doReq(method, path, user, body string, hdr ...string) *http.Response {
	e.t.Helper()
	var rb io.Reader
	if body != "" {
		rb = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, e.srv.URL+path, rb)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
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
		{"POST", "/auth"}, {"GET", "/eventsx"}, {"GET", "/auth"}, {"GET", "/stats"},
	} {
		checkPerl(t, e, rq[0], rq[1], "")
	}
}

func TestCrossSiteToPerlRefused(t *testing.T) {
	e := setupCfg(t, config.Config{Secret: testSecret, Origins: []string{"https://ok"}})
	res := e.doReq("POST", "/auth", "", "", "Sec-Fetch-Site", "cross-site")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || res.Header.Get("X-Perl") != "" || string(body) != `{"status":"error","error":"Cross-site request refused"}` {
		t.Errorf("cross-site POST /auth: %d %q %s", res.StatusCode, res.Header.Get("X-Perl"), body)
	}
	res = e.doReq("POST", "/auth", "", "", "Sec-Fetch-Site", "same-origin")
	res.Body.Close()
	if res.StatusCode != 299 {
		t.Errorf("same-origin POST /auth: %d, want Perl", res.StatusCode)
	}
}

func TestCrossSitePingRefused(t *testing.T) {
	e := setupCfg(t, config.Config{Secret: testSecret, Origins: []string{"https://ok"}})
	u := testenv.AddUser(t, e.db, "alice")
	res, id := e.openStream("/events", u)
	defer res.Body.Close()
	ping := e.doReq("POST", "/events/"+id+"/ping", u, "", "Sec-Fetch-Site", "cross-site")
	ping.Body.Close()
	if ping.StatusCode != 403 {
		t.Errorf("cross-site ping: %d, want 403", ping.StatusCode)
	}
	ping = e.doReq("POST", "/events/"+id+"/ping", u, "", "Sec-Fetch-Site", "same-origin")
	ping.Body.Close()
	if ping.StatusCode != 204 {
		t.Errorf("same-origin ping: %d, want 204", ping.StatusCode)
	}
}

func TestStreamSetsCookie(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res, _ := e.openStream("/events", u)
	defer res.Body.Close()
	if cs := res.Cookies(); len(cs) != 1 || cs[0].Name != "chats" {
		t.Errorf("stream Set-Cookie = %v", res.Header["Set-Cookie"])
	}
}

func TestPassThroughSetsNoCookie(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res := e.do("POST", "/auth", u)
	res.Body.Close()
	if res.StatusCode != 299 || res.Header.Get("Set-Cookie") != "" {
		t.Errorf("pass-through: %d %v", res.StatusCode, res.Header["Set-Cookie"])
	}
}
