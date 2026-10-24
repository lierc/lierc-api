package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/session"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) {
	log.SetOutput(io.Discard) // handler error logging
	testenv.Main(m)
}

type testEnv struct {
	t     *testing.T
	h     http.Handler
	db    *db.DB
	codec session.Codec
	user  string
}

// setup serves a copy of the route table where every route has a stub
// handler echoing its name and captures, overridden by handlers.
func setup(t *testing.T, handlers map[string]Handler) *testEnv {
	t.Helper()
	d := testenv.Postgres(t)
	user, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	table := make([]route, len(routes))
	copy(table, routes)
	for i := range table {
		name := table[i].Name
		table[i].H = func(a *App, r *Request) (Response, error) {
			return JSON(map[string]any{"route": name, "params": r.Params}), nil
		}
		if h, ok := handlers[name]; ok {
			table[i].H = h
		}
	}
	codec := session.Codec{Secret: "testsecret"}
	app := &App{Cfg: config.Config{Secret: "testsecret"}, DB: d, AssetDir: "."}
	return &testEnv{t: t, h: codec.Middleware(newHandler(app, table)), db: d, codec: codec, user: user}
}

func (e *testEnv) do(method, target string, body io.Reader, loggedIn bool, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, body)
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	if loggedIn {
		req.AddCookie(&http.Cookie{Name: "chats2", Value: e.codec.Encode(e.user)})
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

type errBody struct {
	Status string  `json:"status"`
	Error  *string `json:"error"`
}

func expectError(t *testing.T, rec *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	if rec.Code != code {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, code, rec.Body)
	}
	var b errBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	if b.Status != "error" || b.Error == nil || *b.Error != msg {
		t.Fatalf("body = %s, want error %q", rec.Body, msg)
	}
}

type echo struct {
	Route  string            `json:"route"`
	Params map[string]string `json:"params"`
}

func expectRoute(t *testing.T, rec *httptest.ResponseRecorder, name string) echo {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body)
	}
	var e echo
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("body %q: %v", rec.Body, err)
	}
	if e.Route != name {
		t.Fatalf("route = %q, want %q", e.Route, name)
	}
	return e
}

func TestUnauthenticated(t *testing.T) {
	e := setup(t, nil)
	for _, p := range []string{"/auth", "/health", "/nope"} {
		expectError(t, e.do("GET", p, nil, false), 401, "unauthorized")
	}
}

func TestPublicRoutesSkipAuth(t *testing.T) {
	e := setup(t, nil)
	expectRoute(t, e.do("POST", "/auth", nil, false), "auth.login")
	expectRoute(t, e.do("POST", "/register", nil, false), "auth.register")
	expectRoute(t, e.do("POST", "/v1/pushPackages/web.x", nil, false), "apn.package")
	expectRoute(t, e.do("POST", "/v1/log", nil, false), "apn.log")
	expectRoute(t, e.do("POST", "/v1/devices/d/registrations/p", nil, false), "apn.register")
	expectRoute(t, e.do("DELETE", "/v1/devices/d/registrations/p", nil, false), "apn.unregister")
}

func TestUnknownPathLoggedIn(t *testing.T) {
	e := setup(t, nil)
	expectError(t, e.do("GET", "/nope", nil, true), 404, "not found")
}

func TestMethodMismatch(t *testing.T) {
	e := setup(t, nil)
	expectError(t, e.do("PATCH", "/auth", nil, true), 404, "not found")
}

func TestLogoutAnyMethod(t *testing.T) {
	e := setup(t, nil)
	for _, m := range []string{"GET", "POST", "DELETE"} {
		expectRoute(t, e.do(m, "/logout", nil, false), "auth.logout")
	}
}

func TestRoutesMatch(t *testing.T) {
	e := setup(t, nil)
	if err := e.db.SaveConnection(context.Background(), "c1", e.user, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		method, path, name string
		params             map[string]string
	}{
		{"GET", "/highlight", "message.highlight", map[string]string{}},
		{"GET", "/highlight/9", "message.highlight", map[string]string{"event": "9"}},
		{"POST", "/connection/c1", "connection.send", map[string]string{"id": "c1"}},
		{"DELETE", "/connection/c1/nick/bob", "private.delete", map[string]string{"id": "c1", "nick": "bob"}},
		{"GET", "/connection/c1/channel/%23chan/date/2020-01-01/2020-01-02", "channel.date",
			map[string]string{"id": "c1", "channel": "#chan", "from": "2020-01-01", "to": "2020-01-02"}},
		{"GET", "/connection/c1/channel/%23chan/events/5", "channel.logs_id",
			map[string]string{"id": "c1", "channel": "#chan", "event": "5"}},
		{"GET", "/notification/web_push_keys", "webpush.keys", map[string]string{}},
	}
	for _, c := range cases {
		got := expectRoute(t, e.do(c.method, c.path, nil, true), c.name)
		if len(got.Params) != len(c.params) {
			t.Errorf("%s %s params = %v, want %v", c.method, c.path, got.Params, c.params)
		}
		for k, v := range c.params {
			if got.Params[k] != v {
				t.Errorf("%s %s param %s = %q, want %q", c.method, c.path, k, got.Params[k], v)
			}
		}
	}
}

func TestOwnerCheck(t *testing.T) {
	e := setup(t, nil)
	ctx := context.Background()
	other, _, err := e.db.AddUser(ctx, "bob", "bob@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveConnection(ctx, "theirs", other, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.db.SaveConnection(ctx, "mine", e.user, []byte(`{}`)); err != nil {
		t.Fatal(err)
	}
	rec := e.do("GET", "/connection/theirs", nil, true)
	if rec.Code != 401 {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var b errBody
	json.Unmarshal(rec.Body.Bytes(), &b)
	// Perl's message interpolates the request object (Dispatch.pm).
	perlMsg := regexp.MustCompile(`^Invalid connection id 'API::Request=HASH\(0x[0-9a-f]+\)->captures->\{id\}'$`)
	if b.Error == nil || !strings.HasPrefix(*b.Error, "Invalid connection id '") || !perlMsg.MatchString(*b.Error) {
		t.Fatalf("body = %s", rec.Body)
	}
	got := expectRoute(t, e.do("GET", "/connection/mine", nil, true), "connection.show")
	if got.Params["id"] != "mine" {
		t.Fatalf("params = %v", got.Params)
	}
}

func TestRestCaptures(t *testing.T) {
	e := setup(t, nil)
	got := expectRoute(t, e.do("DELETE", "/notification/web_push/https://x/y?z", nil, true), "webpush.delete")
	if got.Params["endpoint"] != "https://x/y" {
		t.Fatalf("endpoint = %q", got.Params["endpoint"])
	}
	got = expectRoute(t, e.do("DELETE", "/image/", nil, true), "image.delete")
	if v, ok := got.Params["url"]; !ok || v != "" {
		t.Fatalf("params = %v", got.Params)
	}
	// {endpoint:.+} needs at least one character.
	expectError(t, e.do("DELETE", "/notification/web_push/", nil, true), 404, "not found")
}

func TestHandlerError(t *testing.T) {
	e := setup(t, map[string]Handler{
		"auth.show": func(*App, *Request) (Response, error) { return nil, errors.New("boom") },
		"auth.token": func(*App, *Request) (Response, error) {
			return nil, &StatusError{Code: 403, Msg: "nope"}
		},
		"auth.health": func(*App, *Request) (Response, error) { panic("kaboom") },
		"pref.list": func(*App, *Request) (Response, error) {
			return nil, &pgconn.PgError{Message: "dup", Detail: "Key (id)=(1) already exists."}
		},
		"ignore.list": func(*App, *Request) (Response, error) {
			return nil, &pgconn.PgError{Message: "dup"}
		},
	})
	expectError(t, e.do("GET", "/auth", nil, true), 400, "boom")
	expectError(t, e.do("GET", "/token", nil, true), 403, "nope")
	expectError(t, e.do("GET", "/health", nil, true), 400, "kaboom")
	expectError(t, e.do("GET", "/preference", nil, true), 400, "Key (id)=(1) already exists.")

	rec := e.do("GET", "/ignore", nil, true)
	if rec.Code != 400 || strings.TrimSpace(rec.Body.String()) != `{"status":"error","error":null}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestContentType(t *testing.T) {
	e := setup(t, nil)
	for _, rec := range []*httptest.ResponseRecorder{
		e.do("GET", "/auth", nil, true),
		e.do("GET", "/auth", nil, false),
		e.do("GET", "/nope", nil, true),
	} {
		if ct := rec.Header().Get("Content-Type"); ct != "application/javascript;charset=utf-8" {
			t.Fatalf("Content-Type = %q", ct)
		}
		if cl := rec.Header().Get("Content-Length"); cl != itoa(rec.Body.Len()) {
			t.Fatalf("Content-Length = %q, body %d bytes", cl, rec.Body.Len())
		}
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestNoContentBody(t *testing.T) {
	e := setup(t, map[string]Handler{
		"auth.show": func(*App, *Request) (Response, error) { return NoContent(), nil },
	})
	rec := e.do("GET", "/auth", nil, true)
	if rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("got %d %q", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "" {
		t.Fatalf("Content-Type = %q", ct)
	}
}

func TestDeletedUserSession(t *testing.T) {
	e := setup(t, nil)
	e.user = "ghost"
	expectError(t, e.do("GET", "/auth", nil, true), 401, "unauthorized")
}

func TestSessionCookieReissued(t *testing.T) {
	e := setup(t, nil)
	rec := e.do("GET", "/auth", nil, true)
	if !strings.Contains(rec.Header().Get("Set-Cookie"), "chats2="+e.user+".") {
		t.Fatalf("Set-Cookie = %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestParamAndBody(t *testing.T) {
	var gotA, gotB, gotC string
	var okD bool
	var body []byte
	e := setup(t, map[string]Handler{
		"connection.create": func(a *App, r *Request) (Response, error) {
			gotA, _ = r.Param("a")
			gotB, _ = r.Param("b")
			gotC, _ = r.Param("c")
			_, okD = r.Param("d")
			body = r.Body
			return OK("x", 1), nil
		},
	})
	rec := e.do("POST", "/connection?a=1&b=q;c=x+y", strings.NewReader("a=2&c=%41%zz"), true,
		"Content-Type", "application/x-www-form-urlencoded; charset=utf-8")
	if rec.Code != 200 || strings.TrimSpace(rec.Body.String()) != `{"status":"ok","x":1}` {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if gotA != "2" || gotB != "q" || gotC != "A%zz" || okD {
		t.Fatalf("a=%q b=%q c=%q d=%v", gotA, gotB, gotC, okD)
	}
	if string(body) != "a=2&c=%41%zz" {
		t.Fatalf("body = %q", body)
	}

	// A JSON body is not parsed as parameters.
	e.do("POST", "/connection?c=q", strings.NewReader(`{"a":"3"}`), true, "Content-Type", "application/json")
	if gotA != "" || gotC != "q" || string(body) != `{"a":"3"}` {
		t.Fatalf("a=%q c=%q body=%q", gotA, gotC, body)
	}
}

func TestResponses(t *testing.T) {
	rec := httptest.NewRecorder()
	Passthrough(201, http.Header{"X-Up": {"1"}, "Content-Length": {"99"}}, []byte("hi")).Write(rec)
	if rec.Code != 201 || rec.Body.String() != "hi" || rec.Header().Get("X-Up") != "1" ||
		rec.Header().Get("Content-Length") != "2" {
		t.Fatalf("passthrough: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}

	rec = httptest.NewRecorder()
	Stream(func(w io.Writer) bool { io.WriteString(w, "event: log\n\n"); return true }).Write(rec)
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "text/event-stream;charset=utf-8" ||
		rec.Body.String() != "event: log\n\n" || !rec.Flushed {
		t.Fatalf("stream: %d %v %q", rec.Code, rec.Header(), rec.Body)
	}

	rec = httptest.NewRecorder()
	JSONStatus(map[string]string{"a": "<&>"}, 202).Write(rec)
	if rec.Code != 202 || rec.Body.String() != `{"a":"<&>"}` {
		t.Fatalf("json: %d %q", rec.Code, rec.Body)
	}

	rec = httptest.NewRecorder()
	Unauthorized("").Write(rec)
	expectError(t, rec, 401, "unauthorized")

	rec = httptest.NewRecorder()
	Error("bad\n\n").Write(rec)
	expectError(t, rec, 400, "bad")

	rec = httptest.NewRecorder()
	ErrorFromDB(errors.New("plain")).Write(rec)
	expectError(t, rec, 400, "plain")
}

func TestNilHandlerNotFound(t *testing.T) {
	d := testenv.Postgres(t)
	user, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	codec := session.Codec{Secret: "s"}
	table := []route{{Method: "GET", Pattern: "/auth", Name: "auth.show"}}
	h := codec.Middleware(newHandler(&App{Cfg: config.Config{Secret: "s"}, DB: d}, table))
	req := httptest.NewRequest("GET", "/auth", nil)
	req.AddCookie(&http.Cookie{Name: "chats2", Value: codec.Encode(user)})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expectError(t, rec, 404, "not found")
}

func TestRouteTable(t *testing.T) {
	if len(routes) != 47 {
		t.Fatalf("len(routes) = %d", len(routes))
	}
	public := 0
	for _, r := range routes {
		if r.Public {
			public++
		}
	}
	if public != 7 {
		t.Fatalf("public routes = %d, want 7", public)
	}
}

func TestBodyCap(t *testing.T) {
	h := newHandler(&App{Cfg: config.Config{MaxBodyBytes: 16}}, routes)
	var logged bytes.Buffer
	old := apnLogOutput
	apnLogOutput = &logged
	t.Cleanup(func() { apnLogOutput = old })
	post := func(h http.Handler, target, body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("POST", target, strings.NewReader(body)))
		return rec
	}
	for _, target := range []string{"/v1/log", "/auth", "/register"} {
		rec := post(h, target, strings.Repeat("x", 17))
		if rec.Code != 413 || rec.Body.String() != `{"status":"error","error":"Request entity too large"}` ||
			rec.Header().Get("Content-Type") != "application/javascript;charset=utf-8" {
			t.Errorf("%s: %d %s", target, rec.Code, rec.Body)
		}
	}
	if rec := post(h, "/v1/log", strings.Repeat("x", 16)); rec.Code != 204 {
		t.Errorf("at cap: %d %s", rec.Code, rec.Body)
	}
	// An unset cap is the 32 MiB default.
	dh := newHandler(&App{}, routes)
	if rec := post(dh, "/v1/log", strings.Repeat("y", 1<<20)); rec.Code != 204 {
		t.Errorf("1 MiB with default cap: %d %s", rec.Code, rec.Body)
	}
	if rec := post(dh, "/v1/log", strings.Repeat("z", config.DefaultMaxBodyBytes+1)); rec.Code != 413 {
		t.Errorf("over default cap: %d", rec.Code)
	}
}

func TestAPNLogBounded(t *testing.T) {
	var logged bytes.Buffer
	old := apnLogOutput
	apnLogOutput = &logged
	t.Cleanup(func() { apnLogOutput = old })
	h := newHandler(&App{}, routes)
	body := "a\nb\r\n" + strings.Repeat("x", 100<<10)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/v1/log", strings.NewReader(body)))
	want := `a\nb\r\n` + strings.Repeat("x", 64<<10-5) + "\n"
	if logged.String() != want {
		t.Fatalf("logged %d bytes, %d lines; want %d bytes", logged.Len(), strings.Count(logged.String(), "\n"), len(want))
	}
}

// Database errors never show their text: a connect error names the host,
// user and database.
func TestDBErrorsAreNull(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	d, err := db.Open(context.Background(), config.Config{DBHost: addr, DBUser: "secretuser", DBName: "secretdb"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	table := []route{
		{Method: "GET", Pattern: "/exec", Name: "t.exec", Public: true, H: func(a *App, r *Request) (Response, error) {
			_, err := a.DB.Pool.Exec(r.Context(), `SELECT 1`)
			return nil, err
		}},
		{Method: "GET", Pattern: "/plain", Name: "t.plain", Public: true, H: func(a *App, r *Request) (Response, error) {
			return nil, errors.New("Invalid token")
		}},
	}
	h := newHandler(&App{DB: d}, table)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/exec", nil))
	expectNullError(t, rec)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/plain", nil))
	expectError(t, rec, 400, "Invalid token")

	for _, err := range []error{
		&db.Error{Err: context.DeadlineExceeded},
		&db.Error{Err: pgx.ErrNoRows},
		fmt.Errorf("wrapped: %w", &db.Error{Err: errors.New("failed to connect to `user=u database=d`")}),
	} {
		rec := httptest.NewRecorder()
		ErrorFromDB(err).Write(rec)
		expectNullError(t, rec)
	}
	rec = httptest.NewRecorder()
	ErrorFromDB(&db.Error{Err: &pgconn.PgError{Detail: "Key (x)=(1) already exists."}}).Write(rec)
	expectError(t, rec, 400, "Key (x)=(1) already exists.")
}

func expectNullError(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != 400 || rec.Body.String() != `{"status":"error","error":null}` {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
}
