package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/testenv"
)

const connBody = `{"Host":"irc.example.com","Port":6667,"Nick":"alice"}`

type connEnv struct {
	*authEnv
	fake *testenv.LiercdServer
}

func setupConn(t *testing.T) *connEnv {
	t.Helper()
	d := testenv.Postgres(t)
	id, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", "x")
	if err != nil {
		t.Fatal(err)
	}
	fake := testenv.FakeLiercd(t)
	app := &App{Cfg: config.Config{Secret: authSecret}, DB: d, Liercd: &liercd.Client{BaseURL: fake.URL, HTTP: http.DefaultClient}, AssetDir: "."}
	base := &authEnv{t: t, db: d, id: id}
	base.codec.Secret = authSecret
	base.h = base.codec.Middleware(newHandler(app, routes))
	return &connEnv{authEnv: base, fake: fake}
}

// raw sends a request with a raw body as alice.
func (e *connEnv) raw(method, target, body string, hdr ...string) *httptest.ResponseRecorder {
	e.t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.AddCookie(e.session())
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *connEnv) create(body string) string {
	e.t.Helper()
	rec := e.raw("POST", "/connection", body)
	var res struct {
		ID string `json:"id"`
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &res) != nil || res.ID == "" {
		e.t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return res.ID
}

func expectConnError(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != 400 {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	if want != "" && rec.Body.String() != `{"status":"error","error":"`+want+`"}` {
		t.Fatalf("body = %s", rec.Body)
	}
}

func TestCreate(t *testing.T) {
	e := setupConn(t)
	body := ` { "Host" : "irc.example.com", "Port": 6667, "Nick": "alice" } `
	rec := e.raw("POST", "/connection", body)
	var res map[string]string
	json.Unmarshal(rec.Body.Bytes(), &res)
	id := res["id"]
	if rec.Code != 200 || res["success"] != "ok" || id == "" || len(res) != 2 {
		t.Fatalf("response %d %s", rec.Code, rec.Body)
	}
	calls := e.fake.Calls()
	if len(calls) != 1 || calls[0].Method != "POST" || calls[0].Path != "/"+id+"/create" || calls[0].Body != body {
		t.Fatalf("calls = %+v", calls)
	}
	cfg, ok, err := e.db.LookupConfig(context.Background(), id)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// a json column keeps the text as given
	if string(cfg) != body {
		t.Fatalf("stored %q", cfg)
	}
}

func TestCreateRequired(t *testing.T) {
	e := setupConn(t)
	for _, f := range []string{"Host", "Port", "Nick"} {
		m := map[string]any{"Host": "h", "Port": 1, "Nick": "n"}
		m[f] = "  "
		b, _ := json.Marshal(m)
		expectConnError(t, e.raw("POST", "/connection", string(b)), f+" is required")
		delete(m, f)
		b, _ = json.Marshal(m)
		expectConnError(t, e.raw("POST", "/connection", string(b)), f+" is required")
	}
	if n := len(e.fake.Calls()); n != 0 {
		t.Fatalf("liercd called %d times", n)
	}
}

func TestDeleteLiercdFails(t *testing.T) {
	e := setupConn(t)
	if err := e.db.SaveConnection(context.Background(), "c1", e.id, []byte(connBody)); err != nil {
		t.Fatal(err)
	}
	e.fake.Respond("POST", "/c1/destroy", 500, "boom")
	expectConnError(t, e.raw("DELETE", "/connection/c1", ""), "boom")
}

func TestCreateLiercdFails(t *testing.T) {
	e := setupConn(t)
	// POST /connection has no id capture, so force liercd to fail every create.
	app := &App{DB: e.db, Liercd: &liercd.Client{BaseURL: failServer(t, "boom").URL, HTTP: http.DefaultClient}}
	h := e.codec.Middleware(newHandler(app, routes))
	req := httptest.NewRequest("POST", "/connection", strings.NewReader(connBody))
	req.AddCookie(e.session())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expectConnError(t, rec, "boom")
	if conns, _ := e.db.Connections(context.Background(), e.id); len(conns) != 0 {
		t.Fatal("row saved")
	}
}

func failServer(t *testing.T, body string) *httptest.Server {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		io.WriteString(w, body)
	}))
	t.Cleanup(s.Close)
	return s
}

func TestCreateLiercdDown(t *testing.T) {
	e := setupConn(t)
	app := &App{DB: e.db, Liercd: &liercd.Client{BaseURL: "http://127.0.0.1:1", HTTP: http.DefaultClient}}
	h := e.codec.Middleware(newHandler(app, routes))
	req := httptest.NewRequest("POST", "/connection", strings.NewReader(connBody))
	req.AddCookie(e.session())
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expectConnError(t, rec, "")
}

func TestCreateBadJSON(t *testing.T) {
	e := setupConn(t)
	expectConnError(t, e.raw("POST", "/connection", "{nope"), "")
	expectConnError(t, e.raw("POST", "/connection", "[1]"), "")
}

func TestEditKeepsId(t *testing.T) {
	e := setupConn(t)
	id := e.create(connBody)
	e.fake.Respond("POST", "/"+id+"/destroy", 500, "nope")
	rec := e.raw("PUT", "/connection/"+id, connBody)
	expectJSON(t, rec, `{"status":"ok"}`)
	var paths []string
	for _, c := range e.fake.Calls()[1:] {
		paths = append(paths, c.Method+" "+c.Path)
	}
	if strings.Join(paths, ",") != "POST /"+id+"/destroy,POST /"+id+"/create" {
		t.Fatalf("calls = %v", paths)
	}
}

func TestDelete(t *testing.T) {
	e := setupConn(t)
	id := e.create(connBody)
	e.fake.Respond("POST", "/"+id+"/destroy", 200, "")
	expectJSON(t, e.raw("DELETE", "/connection/"+id, ""), `{"status":"ok"}`)
	if _, ok, _ := e.db.LookupConfig(context.Background(), id); ok {
		t.Fatal("row not deleted")
	}
	last := e.fake.Calls()[1]
	if last.Method != "POST" || last.Path != "/"+id+"/destroy" {
		t.Fatalf("call = %+v", last)
	}
}

func TestSendToken(t *testing.T) {
	e := setupConn(t)
	id := e.create(connBody)
	e.fake.Respond("POST", "/"+id+"/raw", 200, "")
	expectConnError(t, e.raw("POST", "/connection/"+id, "PRIVMSG"), "Invalid token")
	expectConnError(t, e.raw("POST", "/connection/"+id, "PRIVMSG", "lierc-token", "bogus"), "Invalid token")

	tok, err := e.db.GetToken(context.Background(), e.id)
	if err != nil {
		t.Fatal(err)
	}
	rec := e.raw("POST", "/connection/"+id, "PRIVMSG #a :hi", "lierc-token", tok)
	var res map[string]string
	json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 200 || res["status"] != "ok" || res["token"] == "" || res["token"] == tok {
		t.Fatalf("response %d %s", rec.Code, rec.Body)
	}
	c := e.fake.Calls()[1]
	if c.Path != "/"+id+"/raw" || c.Body != "PRIVMSG #a :hi" {
		t.Fatalf("call = %+v", c)
	}
	// the used token is spent, the new one works
	expectConnError(t, e.raw("POST", "/connection/"+id, "x", "lierc-token", tok), "Invalid token")
	expectJSON2(t, e.raw("POST", "/connection/"+id, "x", "lierc-token", res["token"]))
}

func expectJSON2(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
}

func TestListAndShow(t *testing.T) {
	e := setupConn(t)
	id := e.create(connBody)
	want := `{"Host":"irc.example.com","Port":6667,"Nick":"alice"}`
	expectJSON(t, e.raw("GET", "/connection", ""), `[{"Id":"`+id+`","Config":`+want+`}]`)
	expectJSON(t, e.raw("GET", "/connection/"+id, ""), `{"Id":"`+id+`","Config":`+want+`}`)
}

func TestPrivates(t *testing.T) {
	e := setupConn(t)
	id := e.create(connBody)
	for _, n := range []string{"bob", "carol"} {
		if rec := e.raw("POST", "/connection/"+id+"/nick/"+n, ""); rec.Code != 204 || rec.Body.Len() != 0 {
			t.Fatalf("create %s: %d %s", n, rec.Code, rec.Body)
		}
	}
	list := func() []map[string]string {
		rec := e.raw("GET", "/privates", "")
		var rows []map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatalf("%v: %s", err, rec.Body)
		}
		return rows
	}
	rows := list()
	if len(rows) != 2 || rows[0]["nick"] != "bob" || rows[1]["nick"] != "carol" || rows[0]["connection"] != id {
		t.Fatalf("rows = %v", rows)
	}
	// DBD::Pg text form of a timestamp: no T, no zone
	if _, err := time.Parse("2006-01-02 15:04:05.999999", rows[0]["time"]); err != nil {
		t.Fatalf("time %q: %v", rows[0]["time"], err)
	}
	e.raw("POST", "/connection/"+id+"/nick/bob", "") // touches bob's time
	rows = list()
	if rows[0]["nick"] != "carol" || rows[1]["nick"] != "bob" {
		t.Fatalf("after touch: %v", rows)
	}
	expectJSON(t, e.raw("DELETE", "/connection/"+id+"/nick/bob", ""), `{"status":"ok"}`)
	if rows = list(); len(rows) != 1 || rows[0]["nick"] != "carol" {
		t.Fatalf("after delete: %v", rows)
	}
}

func TestCreateNotifies(t *testing.T) {
	e := setupConn(t)
	ctx := context.Background()
	c, err := e.db.Pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Release()
	if _, err := c.Exec(ctx, "LISTEN highlights"); err != nil {
		t.Fatal(err)
	}
	e.create(connBody)
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	n, err := c.Conn().WaitForNotification(wctx)
	if err != nil || n.Channel != "highlights" {
		t.Fatalf("notification %v, %v", n, err)
	}
}
