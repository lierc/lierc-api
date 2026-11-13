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

type env struct {
	t    *testing.T
	db   *db.DB
	srv  *httptest.Server
	fake *testenv.LiercdServer
	s    *stream.Server
	cfg  config.Config
}

func setup(t *testing.T) *env { return setupCfg(t, config.Config{Secret: testSecret}) }

func setupCfg(t *testing.T, cfg config.Config) *env {
	t.Helper()
	if cfg.UnsubscribeSecret == "" {
		cfg.UnsubscribeSecret = "test-unsubscribe-secret"
	}
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	lc := &liercd.Client{BaseURL: fake.URL, HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	s := stream.New(cfg, d, lc)
	old := checkHost
	checkHost = func(context.Context, string, int) error { return nil }
	t.Cleanup(func() { checkHost = old })
	srv := httptest.NewServer(New(cfg, d, s, lc))
	t.Cleanup(srv.Close)
	return &env{t: t, db: d, srv: srv, fake: fake, s: s, cfg: cfg}
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
	if res.StatusCode != 200 || res.Header.Get("Content-Type") != "text/event-stream;charset=utf-8" {
		res.Body.Close()
		e.t.Fatalf("GET %s: %d %q, want a stream", path, res.StatusCode, res.Header.Get("Content-Type"))
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

const notFoundBody = `{"status":"error","error":"not found"}`

// checkNotFound expects Go's 404 JSON: every route Perl served is now
// Go's, and anything else is unknown.
func checkNotFound(t *testing.T, e *env, method, path, user string) {
	t.Helper()
	res := e.do(method, path, user)
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 || string(body) != notFoundBody || res.Header.Get("Content-Type") != "application/javascript;charset=utf-8" {
		t.Errorf("%s %s: %d %q %s, want the 404 JSON", method, path, res.StatusCode, res.Header.Get("Content-Type"), body)
	}
}

// The paths the stream served before nginx sent /events are unknown now.
func TestLegacyStreamPathsNotFound(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res, id := e.openStream("/events", u)
	defer res.Body.Close()
	checkNotFound(t, e, "GET", "/", u)
	checkNotFound(t, e, "POST", "/"+id+"/ping", u)
}

func TestUnknownPathNotFound(t *testing.T) {
	e := setup(t)
	for _, rq := range [][2]string{
		{"POST", "/nope"}, {"GET", "/eventsx"}, {"GET", "/nope"}, {"GET", "/stats"},
		{"PUT", "/connection/c1/nick/bob"}, {"GET", "/"},
	} {
		checkNotFound(t, e, rq[0], rq[1], "")
	}
}

func TestCrossSiteRefused(t *testing.T) {
	e := setupCfg(t, config.Config{Secret: testSecret, Origins: []string{"https://ok"}})
	res := e.doReq("POST", "/nope", "", "", "Sec-Fetch-Site", "cross-site")
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 403 || string(body) != `{"status":"error","error":"Cross-site request refused"}` {
		t.Errorf("cross-site POST /nope: %d %s", res.StatusCode, body)
	}
	res = e.doReq("POST", "/nope", "", "", "Sec-Fetch-Site", "same-origin")
	body, _ = io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 404 || string(body) != notFoundBody {
		t.Errorf("same-origin POST /nope: %d %s, want the 404", res.StatusCode, body)
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

func TestNotFoundSetsNoCookie(t *testing.T) {
	e := setup(t)
	u := testenv.AddUser(t, e.db, "alice")
	res := e.do("POST", "/nope", u)
	res.Body.Close()
	if res.StatusCode != 404 || res.Header.Get("Set-Cookie") != "" {
		t.Errorf("unknown path: %d %v", res.StatusCode, res.Header["Set-Cookie"])
	}
}

// fakeStore implements only Prefs; calling anything else panics.
type fakeStore struct {
	db.Store
	prefs []db.Pref
}

func (f fakeStore) Prefs(context.Context, string) ([]db.Pref, error) { return f.prefs, nil }

// The API's queries go through db.Store: with a fake in place of the
// database, GET /preference answers from it. The rest of the store is the
// real database, which the login check needs.
func TestAPIRunsOnFakeStore(t *testing.T) {
	cfg := config.Config{Secret: testSecret}
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	lc := &liercd.Client{HTTP: &http.Client{}}
	s := stream.New(cfg, d, lc)
	fake := fakeStore{Store: d, prefs: []db.Pref{{Name: "theme", Value: "dark"}}}
	srv := httptest.NewServer(New(cfg, fake, s, lc))
	t.Cleanup(srv.Close)

	req, _ := http.NewRequest("GET", srv.URL+"/preference", nil)
	req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.Login(t, d, u)})
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(body), "dark") {
		t.Errorf("GET /preference = %d %s", res.StatusCode, body)
	}
}
