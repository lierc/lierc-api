package api

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/testenv"
)

type loginEntry struct {
	ID        *int64  `json:"id"`
	Created   *string `json:"created"`
	LastUsed  *string `json:"last_used"`
	UserAgent *string `json:"user_agent"`
	IP        *string `json:"ip"`
	Current   *bool   `json:"current"`
}

// tokenReq sends method path with the login token as its chats cookie and
// returns the status and body.
func (e *env) tokenReq(method, path, token string, hdr ...string) (int, string) {
	e.t.Helper()
	res := e.doReq(method, path, "", "", append([]string{"Cookie", "chats=" + token}, hdr...)...)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res.StatusCode, strings.TrimSpace(string(b))
}

func (e *env) listLogins(token string) []loginEntry {
	e.t.Helper()
	code, body := e.tokenReq("GET", "/logins", token)
	if code != 200 {
		e.t.Fatalf("GET /logins: %d %s", code, body)
	}
	var list []loginEntry
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		e.t.Fatalf("GET /logins: %v: %s", err, body)
	}
	return list
}

func (e *env) loginID(token string) int64 {
	e.t.Helper()
	l, ok, err := login.Lookup(context.Background(), e.db, token)
	if err != nil || !ok {
		e.t.Fatalf("lookup: %v %v", ok, err)
	}
	return l.ID
}

const noSuchLogin = `{"status":"error","error":"no such login"}`

func TestListLogins(t *testing.T) {
	e := setup(t)
	alice, bob := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	ctx := context.Background()
	a, err := login.Create(ctx, e.db, alice, "agent A", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	b, err := login.Create(ctx, e.db, alice, "agent B", "192.0.2.2")
	if err != nil {
		t.Fatal(err)
	}
	testenv.Login(t, e.db, bob)
	e.exec(`UPDATE login SET last_used = now() - interval '30 minutes' WHERE id = $1`, e.loginID(a))
	list := e.listLogins(a)
	if len(list) != 2 {
		t.Fatalf("%d logins, want 2", len(list))
	}
	for _, l := range list {
		if l.ID == nil || l.Created == nil || l.LastUsed == nil || l.UserAgent == nil || l.IP == nil || l.Current == nil {
			t.Fatalf("missing field: %+v", l)
		}
		for _, ts := range []string{*l.Created, *l.LastUsed} {
			if _, err := time.Parse(time.RFC3339, ts); err != nil || !strings.HasSuffix(ts, "Z") {
				t.Errorf("time %q: want RFC 3339 UTC", ts)
			}
		}
	}
	if *list[0].ID != e.loginID(b) || *list[0].UserAgent != "agent B" || *list[0].IP != "192.0.2.2" || *list[0].Current {
		t.Errorf("first: %+v, want B, not current", list[0])
	}
	if *list[1].ID != e.loginID(a) || !*list[1].Current {
		t.Errorf("second: %+v, want A, current", list[1])
	}
}

func TestListLoginsNoIP(t *testing.T) {
	e := setup(t)
	tok := testenv.Login(t, e.db, testenv.AddUser(t, e.db, "alice"))
	if list := e.listLogins(tok); len(list) != 1 || *list[0].IP != "" {
		t.Errorf("logins %+v, want one with ip \"\"", list)
	}
}

func TestDeleteLogin(t *testing.T) {
	e := setup(t)
	alice := testenv.AddUser(t, e.db, "alice")
	a, b := testenv.Login(t, e.db, alice), testenv.Login(t, e.db, alice)
	if code, body := e.tokenReq("DELETE", "/login/"+strconv.FormatInt(e.loginID(b), 10), a); code != 200 || body != `{"status":"ok"}` {
		t.Fatalf("delete: %d %s", code, body)
	}
	if code, _ := e.tokenReq("GET", "/preference", b); code != 401 {
		t.Errorf("ended login: %d, want 401", code)
	}
	if code, _ := e.tokenReq("GET", "/preference", a); code != 200 {
		t.Errorf("own login: %d, want 200", code)
	}
	if list := e.listLogins(a); len(list) != 1 {
		t.Errorf("%d logins after delete, want 1", len(list))
	}
}

func TestDeleteOthersLogin(t *testing.T) {
	e := setup(t)
	a := testenv.Login(t, e.db, testenv.AddUser(t, e.db, "alice"))
	b := testenv.Login(t, e.db, testenv.AddUser(t, e.db, "bob"))
	if code, body := e.tokenReq("DELETE", "/login/"+strconv.FormatInt(e.loginID(b), 10), a); code != 404 || body != noSuchLogin {
		t.Errorf("delete bob's: %d %s", code, body)
	}
	if code, _ := e.tokenReq("GET", "/preference", b); code != 200 {
		t.Errorf("bob's login: %d, want 200", code)
	}
}

func TestDeleteLoginBadID(t *testing.T) {
	e := setup(t)
	alice := testenv.AddUser(t, e.db, "alice")
	a, gone := testenv.Login(t, e.db, alice), testenv.Login(t, e.db, alice)
	goneID := strconv.FormatInt(e.loginID(gone), 10)
	e.exec(`DELETE FROM login WHERE id = $1`, goneID)
	for _, id := range []string{"abc", "999999", goneID, "-1", "1.5"} {
		if code, body := e.tokenReq("DELETE", "/login/"+id, a); code != 404 || body != noSuchLogin {
			t.Errorf("delete %s: %d %s", id, code, body)
		}
	}
}

func TestDeleteCurrentLogin(t *testing.T) {
	e := setup(t)
	a := testenv.Login(t, e.db, testenv.AddUser(t, e.db, "alice"))
	if code, _ := e.tokenReq("DELETE", "/login/"+strconv.FormatInt(e.loginID(a), 10), a); code != 200 {
		t.Fatalf("delete own: %d", code)
	}
	if code, _ := e.tokenReq("GET", "/preference", a); code != 401 {
		t.Errorf("after ending itself: %d, want 401", code)
	}
}

func TestLoginsNeedLogin(t *testing.T) {
	e := setup(t)
	for _, rq := range [][2]string{{"GET", "/logins"}, {"DELETE", "/login/1"}} {
		res := e.do(rq[0], rq[1], "")
		res.Body.Close()
		if res.StatusCode != 401 {
			t.Errorf("%s %s without a login: %d, want 401", rq[0], rq[1], res.StatusCode)
		}
	}
	u := testenv.AddUser(t, e.db, "newbie")
	e.exec(`UPDATE "user" SET verified = false WHERE id = $1`, u)
	tok := testenv.Login(t, e.db, u)
	for _, rq := range [][2]string{{"GET", "/logins"}, {"DELETE", "/login/1"}} {
		if code, _ := e.tokenReq(rq[0], rq[1], tok); code != 403 {
			t.Errorf("%s %s unverified: %d, want 403", rq[0], rq[1], code)
		}
	}
}

func TestLoginsCrossSite(t *testing.T) {
	e := setupCfg(t, config.Config{Secret: testSecret, Origins: []string{"https://ok"}})
	alice := testenv.AddUser(t, e.db, "alice")
	a, b := testenv.Login(t, e.db, alice), testenv.Login(t, e.db, alice)
	code, _ := e.tokenReq("DELETE", "/login/"+strconv.FormatInt(e.loginID(b), 10), a, "Sec-Fetch-Site", "cross-site")
	if code != 403 {
		t.Errorf("cross-site delete: %d, want 403", code)
	}
	if code, _ := e.tokenReq("GET", "/preference", b); code != 200 {
		t.Errorf("login after a refused cross-site delete: %d, want 200", code)
	}
}

// A login with an event stream open is connected; one without isn't.
func TestListLoginsConnected(t *testing.T) {
	e := setup(t)
	alice := testenv.AddUser(t, e.db, "alice")
	a, b := testenv.Login(t, e.db, alice), testenv.Login(t, e.db, alice)
	res := e.doReq("GET", "/events", "", "", "Cookie", "chats="+a)
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("stream: %d", res.StatusCode)
	}
	code, body := e.tokenReq("GET", "/logins", b)
	if code != 200 {
		t.Fatalf("GET /logins: %d %s", code, body)
	}
	var list []struct {
		ID        int64
		Connected *bool
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list) != 2 {
		t.Fatalf("logins %s, %v", body, err)
	}
	for _, l := range list {
		want := l.ID == e.loginID(a)
		if l.Connected == nil || *l.Connected != want {
			t.Errorf("login %d: connected %v, want %v", l.ID, l.Connected, want)
		}
	}
}
