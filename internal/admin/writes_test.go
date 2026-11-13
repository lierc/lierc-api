package admin_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/stream"
	"github.com/lierc/lierc-api/internal/testenv"
)

// recorder collects announcements.
type recorder struct {
	mu  sync.Mutex
	got []db.ConnectionChange
}

func (r *recorder) announce(_ context.Context, c db.ConnectionChange) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.got = append(r.got, c)
}

func (r *recorder) all() []db.ConnectionChange {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]db.ConnectionChange(nil), r.got...)
}

func newWriteEnv(t *testing.T) (*env, *recorder) {
	d := testenv.Postgres(t)
	r := &recorder{}
	return &env{t, d, admin.New(d, r.announce)}, r
}

func (e *env) send(method, path string) (int, string) {
	e.t.Helper()
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, httptest.NewRequest(method, path, nil))
	return w.Code, strings.TrimSpace(w.Body.String())
}

func (e *env) count(sql string) int {
	e.t.Helper()
	var n int
	if err := e.d.Pool.QueryRow(context.Background(), sql).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

// seedUser makes a user with connections c1 and c2 and a row in every table.
func (e *env) seedUser(name string, conns ...string) string {
	e.t.Helper()
	u := testenv.AddUser(e.t, e.d, name)
	for _, c := range conns {
		testenv.AddConnection(e.t, e.d, c, u, `{}`)
		e.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ('` + u + `', '` + c + `', '#a', 1)`)
		e.exec(`INSERT INTO private (connection, nick, time) VALUES ('` + c + `', 'n', now())`)
		e.exec(`INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
			VALUES ('` + c + `', '` + u + `', '#a', 1, 2, now(), now(), 2, '[]')`)
	}
	e.exec(`INSERT INTO pref ("user", name, value) VALUES ('` + u + `', 'p', 'v')`)
	e.exec(`INSERT INTO web_push ("user", endpoint, key, auth) VALUES ('` + u + `', 'https://e/` + name + `', 'k', 'a')`)
	return u
}

func TestDeleteUser(t *testing.T) {
	e, rec := newWriteEnv(t)
	a := e.seedUser("alice", "c1", "c2")
	b := e.seedUser("bob", "b1")
	testenv.AddLog(t, e.d, 1, "c1", a)
	testenv.AddLog(t, e.d, 2, "gone", a) // a connection deleted before the user
	// the earlier-deleted connection's archive and seen rows carry alice's id
	e.exec(`INSERT INTO log_archive (connection, "user", channel, first_id, last_id, min_time, max_time, count, messages)
		VALUES ('gone', '` + a + `', '#a', 3, 4, now(), now(), 2, '[]')`)
	e.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ('` + a + `', 'gone', '#a', 1)`)
	testenv.AddLog(t, e.d, 3, "b1", b)
	testenv.Login(t, e.d, a)
	testenv.Login(t, e.d, b)
	// rows that arrived after log_after, for the user's connections
	testenv.AddLog(t, e.d, 50, "c2", a)
	testenv.AddLog(t, e.d, 51, "c1", "")

	code, body := e.send("DELETE", "/admin/users/"+a+"?log_after=10")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var res struct {
		Status string
		Counts map[string]int64
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil || res.Status != "ok" {
		t.Fatalf("body = %s", body)
	}
	if res.Counts["connection"] != 2 || res.Counts["log_archive"] != 3 || res.Counts["seen"] != 3 || res.Counts["private"] != 2 ||
		res.Counts["pref"] != 1 || res.Counts["web_push"] != 1 || res.Counts["login"] != 1 || res.Counts["log"] < 2 {
		t.Errorf("counts = %v", res.Counts)
	}
	for _, q := range []string{
		`SELECT count(*) FROM "user" WHERE id = '` + a + `'`,
		`SELECT count(*) FROM connection WHERE "user" = '` + a + `'`,
		`SELECT count(*) FROM log WHERE "user" = '` + a + `' OR connection IN ('c1', 'c2')`,
		`SELECT count(*) FROM log_archive WHERE connection IN ('c1', 'c2')`,
		`SELECT count(*) FROM seen WHERE connection IN ('c1', 'c2')`,
		`SELECT count(*) FROM private WHERE connection IN ('c1', 'c2')`,
		`SELECT count(*) FROM pref WHERE "user" = '` + a + `'`,
		`SELECT count(*) FROM web_push WHERE "user" = '` + a + `'`,
		`SELECT count(*) FROM login WHERE "user" = '` + a + `'`,
		`SELECT count(*) FROM log_archive WHERE connection = 'gone'`,
		`SELECT count(*) FROM seen WHERE connection = 'gone'`,
	} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d, want 0", q, n)
		}
	}
	// bob untouched
	for q, want := range map[string]int{
		`SELECT count(*) FROM "user" WHERE id = '` + b + `'`:       1,
		`SELECT count(*) FROM connection WHERE id = 'b1'`:          1,
		`SELECT count(*) FROM log WHERE connection = 'b1'`:         1,
		`SELECT count(*) FROM log_archive WHERE connection = 'b1'`: 1,
		`SELECT count(*) FROM seen WHERE connection = 'b1'`:        1,
		`SELECT count(*) FROM private WHERE connection = 'b1'`:     1,
		`SELECT count(*) FROM pref WHERE "user" = '` + b + `'`:     1,
		`SELECT count(*) FROM web_push WHERE "user" = '` + b + `'`: 1,
		`SELECT count(*) FROM login WHERE "user" = '` + b + `'`:    1,
	} {
		if n := e.count(q); n != want {
			t.Errorf("%s = %d, want %d", q, n, want)
		}
	}
	got := rec.all()
	if len(got) != 2 {
		t.Fatalf("announced %v", got)
	}
	seen := map[string]bool{}
	for _, c := range got {
		if c.User != a || c.Action != "delete" {
			t.Errorf("announced %+v", c)
		}
		seen[c.Connection] = true
	}
	if !seen["c1"] || !seen["c2"] {
		t.Errorf("announced %v", got)
	}
}

func TestDeleteLogAfterParam(t *testing.T) {
	e, _ := newWriteEnv(t)
	a := e.seedUser("alice", "c1")
	// log_after is optional; a malformed one is refused
	if code, _ := e.send("DELETE", "/admin/users/"+a+"?log_after=x"); code != 400 {
		t.Errorf("bad log_after: %d", code)
	}
	if code, _ := e.send("DELETE", "/admin/connections/c1?log_after=-1"); code != 400 {
		t.Errorf("negative log_after: %d", code)
	}
	if code, body := e.send("DELETE", "/admin/users/"+a); code != 200 {
		t.Errorf("no log_after: %d %s", code, body)
	}
}

func TestDeleteUserNoConnections(t *testing.T) {
	e, rec := newWriteEnv(t)
	a := e.seedUser("alice")
	if code, body := e.send("DELETE", "/admin/users/"+a); code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	if n := e.count(`SELECT count(*) FROM "user" WHERE id = '` + a + `'`); n != 0 {
		t.Errorf("user remains")
	}
	if n := e.count(`SELECT count(*) FROM pref`); n != 0 {
		t.Errorf("pref remains")
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("announced %v", got)
	}
}

func TestDeleteConnection(t *testing.T) {
	e, rec := newWriteEnv(t)
	a := e.seedUser("alice", "c1", "c2")
	testenv.AddLog(t, e.d, 1, "c1", a)
	testenv.AddLog(t, e.d, 2, "c2", a)
	testenv.AddLog(t, e.d, 60, "c1", a) // late
	code, body := e.send("DELETE", "/admin/connections/c1?log_after=10")
	if code != 200 {
		t.Fatalf("%d %s", code, body)
	}
	var res struct {
		Status string
		Counts map[string]int64
	}
	if err := json.Unmarshal([]byte(body), &res); err != nil {
		t.Fatal(err)
	}
	if res.Status != "ok" || res.Counts["seen"] != 1 || res.Counts["private"] != 1 || res.Counts["log_archive"] != 1 || res.Counts["connection"] != 1 || res.Counts["log"] < 1 {
		t.Errorf("body = %s", body)
	}
	for _, q := range []string{
		`SELECT count(*) FROM connection WHERE id = 'c1'`,
		`SELECT count(*) FROM log WHERE connection = 'c1'`,
		`SELECT count(*) FROM log_archive WHERE connection = 'c1'`,
		`SELECT count(*) FROM seen WHERE connection = 'c1'`,
		`SELECT count(*) FROM private WHERE connection = 'c1'`,
	} {
		if n := e.count(q); n != 0 {
			t.Errorf("%s = %d", q, n)
		}
	}
	for _, q := range []string{
		`SELECT count(*) FROM "user" WHERE id = '` + a + `'`,
		`SELECT count(*) FROM connection WHERE id = 'c2'`,
		`SELECT count(*) FROM log WHERE connection = 'c2'`,
		`SELECT count(*) FROM seen WHERE connection = 'c2'`,
	} {
		if n := e.count(q); n != 1 {
			t.Errorf("%s = %d, want 1", q, n)
		}
	}
	if got := rec.all(); len(got) != 1 || got[0] != (db.ConnectionChange{User: a, Connection: "c1", Action: "delete"}) {
		t.Errorf("announced %v", got)
	}
}

// failing is a Store whose deletes fail.
type failing struct{ db.Store }

func (failing) DeleteUserData(context.Context, string) ([]db.ConnectionChange, map[string]int64, error) {
	return nil, nil, errors.New("boom secret detail")
}
func (failing) DeleteConnectionData(context.Context, string) ([]db.ConnectionChange, map[string]int64, error) {
	return nil, nil, errors.New("boom secret detail")
}

func TestDeleteNoAnnounceOnFailure(t *testing.T) {
	rec := &recorder{}
	h := admin.New(failing{}, rec.announce)
	for _, path := range []string{"/admin/users/u1", "/admin/connections/c1"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", path, nil))
		if w.Code != 500 || strings.TrimSpace(w.Body.String()) != `{"error":"internal error"}` {
			t.Errorf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("announced %v", got)
	}
}

func TestDeleteNotFound(t *testing.T) {
	e, rec := newWriteEnv(t)
	code, body := e.send("DELETE", "/admin/users/nosuch")
	if code != 404 || body != `{"error":"no such user"}` {
		t.Errorf("user: %d %s", code, body)
	}
	code, body = e.send("DELETE", "/admin/connections/nosuch")
	if code != 404 || body != `{"error":"no such connection"}` {
		t.Errorf("connection: %d %s", code, body)
	}
	if got := rec.all(); len(got) != 0 {
		t.Errorf("announced %v", got)
	}
}

// cancelling is a Store that cancels the request as the delete starts.
type cancelling struct {
	db.Store
	cancel context.CancelFunc
}

func (c cancelling) DeleteConnectionData(ctx context.Context, id string) ([]db.ConnectionChange, map[string]int64, error) {
	c.cancel()
	return c.Store.DeleteConnectionData(ctx, id)
}

func (c cancelling) DeleteUserData(ctx context.Context, id string) ([]db.ConnectionChange, map[string]int64, error) {
	c.cancel()
	return c.Store.DeleteUserData(ctx, id)
}

func TestDeleteSurvivesCancel(t *testing.T) {
	d := testenv.Postgres(t)
	e := &env{t, d, nil}
	a := e.seedUser("alice", "c1")
	rec := &recorder{}
	for _, path := range []string{"/admin/connections/c1", "/admin/users/" + a} {
		ctx, cancel := context.WithCancel(context.Background())
		h := admin.New(cancelling{d, cancel}, rec.announce)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", path, nil).WithContext(ctx))
		cancel()
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if n := e.count(`SELECT count(*) FROM "user" WHERE id = '` + a + `'`); n != 0 {
		t.Errorf("user remains")
	}
	if got := rec.all(); len(got) != 1 {
		t.Errorf("announced %v", got)
	}
}

func TestEndLogin(t *testing.T) {
	e, _ := newWriteEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.Login(t, e.d, a)
	testenv.Login(t, e.d, a)
	infos, _ := login.List(context.Background(), e.d, a)
	if len(infos) != 2 {
		t.Fatal(infos)
	}
	id := infos[0].ID
	code, body := e.send("DELETE", "/admin/users/"+a+"/logins/"+itoa(id))
	if code != 200 || body != `{"status":"ok","ended":1}` {
		t.Errorf("%d %s", code, body)
	}
	if n := e.count(`SELECT count(*) FROM login`); n != 1 {
		t.Errorf("logins = %d", n)
	}
	for _, p := range []string{"/admin/users/" + a + "/logins/" + itoa(id), "/admin/users/other/logins/" + itoa(infos[1].ID), "/admin/users/" + a + "/logins/zz"} {
		code, body := e.send("DELETE", p)
		if code != 404 || body != `{"error":"no such login"}` {
			t.Errorf("%s: %d %s", p, code, body)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestEndAllLogins(t *testing.T) {
	e, _ := newWriteEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	b := testenv.AddUser(t, e.d, "bob")
	testenv.Login(t, e.d, a)
	testenv.Login(t, e.d, a)
	testenv.Login(t, e.d, b)
	code, body := e.send("DELETE", "/admin/users/"+a+"/logins")
	if code != 200 || body != `{"status":"ok","ended":2}` {
		t.Errorf("%d %s", code, body)
	}
	if n := e.count(`SELECT count(*) FROM login`); n != 1 {
		t.Errorf("logins = %d", n)
	}
	code, body = e.send("DELETE", "/admin/users/"+a+"/logins")
	if code != 200 || body != `{"status":"ok","ended":0}` {
		t.Errorf("again: %d %s", code, body)
	}
}

func TestPruneLogins(t *testing.T) {
	e, _ := newWriteEnv(t)
	a := testenv.AddUser(t, e.d, "alice")
	testenv.Login(t, e.d, a)
	testenv.Login(t, e.d, a)
	e.exec(`UPDATE login SET last_used = now() - interval '8 days' WHERE id = (SELECT min(id) FROM login)`)
	code, body := e.send("POST", "/admin/logins/prune")
	if code != 200 || body != `{"status":"ok","pruned":1}` {
		t.Errorf("%d %s", code, body)
	}
	if n := e.count(`SELECT count(*) FROM login`); n != 1 {
		t.Errorf("logins = %d", n)
	}
}

func TestPruneSeen(t *testing.T) {
	e, _ := newWriteEnv(t)
	u := testenv.AddUser(t, e.d, "u")
	testenv.AddConnection(t, e.d, "c", u, `{}`)
	e.exec(`INSERT INTO seen ("user", connection, channel, message_id) VALUES ('` + u + `', 'c', '#fresh', 1), ('` + u + `', 'gone', '#orphan', 1)`)
	e.exec(`INSERT INTO seen ("user", connection, channel, message_id, updated) VALUES ('` + u + `', 'c', '#old', 1, now() - interval '2 months')`)
	code, body := e.send("POST", "/admin/seen/prune")
	if code != 200 || body != `{"status":"ok","pruned":2}` {
		t.Errorf("%d %s", code, body)
	}
	if n := e.count(`SELECT count(*) FROM seen`); n != 1 {
		t.Errorf("seen = %d", n)
	}
}

func TestStreamGetsDelete(t *testing.T) {
	d := testenv.Postgres(t)
	fake := testenv.FakeLiercd(t)
	lc := &liercd.Client{BaseURL: fake.URL, HTTP: &http.Client{Transport: &http.Transport{DisableKeepAlives: true}}}
	s := stream.New(config.Config{Secret: "secret", UnsubscribeSecret: "unsub"}, d, lc)
	mux := http.NewServeMux()
	s.Routes(mux, auth.Users{Secret: "secret", DB: d})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	u := testenv.AddUser(t, d, "alice")
	testenv.AddConnection(t, d, "c1", u, `{}`)
	req, _ := http.NewRequest("GET", srv.URL+"/events", nil)
	req.AddCookie(&http.Cookie{Name: login.CookieName, Value: testenv.Login(t, d, u)})
	res, err := (&http.Client{Transport: &http.Transport{DisableKeepAlives: true}}).Do(req)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("stream: %v %v", err, res)
	}
	t.Cleanup(func() { res.Body.Close() })
	events := make(chan [2]string, 100)
	go func() {
		defer close(events)
		br := bufio.NewReader(res.Body)
		typ := ""
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				return
			}
			l = strings.TrimSuffix(l, "\n")
			if strings.HasPrefix(l, "event: ") {
				typ = l[7:]
			} else if strings.HasPrefix(l, "data: ") {
				events <- [2]string{typ, l[6:]}
			}
		}
	}()
	// the stream is registered once its opening event has arrived
	select {
	case <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("no stream event")
	}

	h := admin.New(d, s.ConnectionChanged)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/admin/connections/c1", nil))
	if w.Code != 200 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("stream ended")
			}
			if ev[0] == "connection:delete" {
				if ev[1] != `{"Id":"c1"}` {
					t.Errorf("data = %s", ev[1])
				}
				return
			}
		case <-deadline:
			t.Fatal("no connection:delete")
		}
	}
}

// sweepFails is a Store whose late-log sweep fails.
type sweepFails struct{ db.Store }

func (sweepFails) SweepLateLogs(context.Context, string, []string, int64) (int64, int64, error) {
	return 0, 0, errors.New("boom")
}

func TestSweepFailureStillAnnounces(t *testing.T) {
	d := testenv.Postgres(t)
	e := &env{t, d, nil}
	a := e.seedUser("alice", "c1")
	rec := &recorder{}
	h := admin.New(sweepFails{d}, rec.announce)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("DELETE", "/admin/connections/c1?log_after=3", nil))
	var res struct {
		Status     string
		Counts     map[string]int64
		Swept      int64
		SweepError string `json:"sweep_error"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.Status != "ok" ||
		res.SweepError != "internal error" || res.Counts["connection"] != 1 {
		t.Errorf("%d %s", w.Code, w.Body)
	}
	if got := rec.all(); len(got) != 1 || got[0].User != a || got[0].Connection != "c1" {
		t.Errorf("announced %v", got)
	}
	if n := e.count(`SELECT count(*) FROM connection WHERE id = 'c1'`); n != 0 {
		t.Errorf("connection remains")
	}
}

// sweeps records the sweeps asked for.
type sweeps struct {
	db.Store
	calls []int64
}

func (s *sweeps) SweepLateLogs(ctx context.Context, user string, conns []string, after int64) (int64, int64, error) {
	s.calls = append(s.calls, after)
	return s.Store.SweepLateLogs(ctx, user, conns, after)
}

// log_after=0 is given (an empty log has last id 0); only its absence skips the sweep.
func TestLogAfterZeroSweeps(t *testing.T) {
	d := testenv.Postgres(t)
	e := &env{t, d, nil}
	e.seedUser("alice", "c1", "c2")
	s := &sweeps{Store: d}
	h := admin.New(s, nil)
	for _, path := range []string{"/admin/connections/c1", "/admin/connections/c2?log_after=0"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", path, nil))
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	if len(s.calls) != 1 || s.calls[0] != 0 {
		t.Errorf("sweeps = %v, want one with after 0", s.calls)
	}
}

// sweepsTwo is a Store whose late-log sweep removes 2 rows.
type sweepsTwo struct{ db.Store }

func (sweepsTwo) SweepLateLogs(context.Context, string, []string, int64) (int64, int64, error) {
	return 2, 1, nil
}

// swept reports the sweep's rows apart from the delete's own counts; it is 0
// without log_after.
func TestDeleteReportsSwept(t *testing.T) {
	d := testenv.Postgres(t)
	e := &env{t, d, nil}
	e.seedUser("alice", "c1", "c2")
	testenv.AddLog(t, d, 10, "c1", "x")
	h := admin.New(sweepsTwo{d}, nil)
	for path, want := range map[string]int64{"/admin/connections/c1?log_after=30": 2, "/admin/connections/c2": 0} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("DELETE", path, nil))
		var res struct {
			Counts       map[string]int64
			Swept        *int64
			SweptArchive *int64 `json:"swept_archive"`
		}
		wantBlocks := want / 2 // sweepsTwo sweeps 2 rows and 1 block; no sweep, none
		if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil || w.Code != 200 || res.Swept == nil || *res.Swept != want ||
			res.SweptArchive == nil || *res.SweptArchive != wantBlocks {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		if strings.Contains(path, "log_after") && res.Counts["log"] != 1 {
			t.Errorf("counts = %v, want the transaction's own log count", res.Counts)
		}
	}
}

func (failing) ListUsers(context.Context, db.UserFilter) ([]db.UserRow, error) {
	return nil, errors.New("boom secret detail")
}

// A store failure answers 500 {"error":"internal error"} and logs the cause,
// which never reaches the response.
func TestInternalErrorLogsCause(t *testing.T) {
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	h := admin.New(failing{}, nil)
	for _, rq := range [][2]string{{"GET", "/admin/users"}, {"DELETE", "/admin/users/u1"}} {
		logged.Reset()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(rq[0], rq[1], nil))
		if w.Code != 500 || strings.TrimSpace(w.Body.String()) != `{"error":"internal error"}` {
			t.Errorf("%s %s: %d %s", rq[0], rq[1], w.Code, w.Body)
		}
		if !strings.Contains(logged.String(), "boom secret detail") {
			t.Errorf("%s %s: cause not logged: %q", rq[0], rq[1], logged.String())
		}
	}
}
