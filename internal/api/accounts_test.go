package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/accounts"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/passwords"
	"github.com/lierc/lierc-api/internal/testenv"
)

type sentMail struct{ to, subject, body string }

type recorder struct {
	mu   sync.Mutex
	mail []sentMail
}

func (r *recorder) Send(to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mail = append(r.mail, sentMail{to, subject, body})
	return nil
}

// wait is the n-th email, once it has been sent (mail goes out after the
// response).
func (r *recorder) wait(t *testing.T, n int) sentMail {
	t.Helper()
	for i := 0; i < 500; i++ {
		r.mu.Lock()
		if len(r.mail) >= n {
			m := r.mail[n-1]
			r.mu.Unlock()
			return m
		}
		r.mu.Unlock()
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("email %d never sent", n)
	return sentMail{}
}

// mailEnv is setup with a recording mailer.
func mailEnv(t *testing.T) (*env, *recorder) {
	t.Helper()
	rec := &recorder{}
	old := newMailer
	newMailer = func(config.Config) accounts.Mailer { return rec }
	t.Cleanup(func() { newMailer = old })
	return setupCfg(t, config.Config{Secret: testSecret, SiteURL: "https://site.example"}), rec
}

const formType = "application/x-www-form-urlencoded"

func (e *env) form(method, path, user string, v url.Values, hdr ...string) (*http.Response, string) {
	e.t.Helper()
	res := e.doReq(method, path, user, v.Encode(), append([]string{"Content-Type", formType}, hdr...)...)
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	return res, string(b)
}

func (e *env) setPassword(id, pass string) {
	e.t.Helper()
	h, err := passwords.Hash(pass)
	if err != nil {
		e.t.Fatal(err)
	}
	if _, err := e.db.Pool.Exec(context.Background(), `UPDATE "user" SET password_hash=$2 WHERE id=$1`, id, h); err != nil {
		e.t.Fatal(err)
	}
}

func chatsCookie(res *http.Response) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == "chats" {
			return c
		}
	}
	return nil
}

func TestShowRoute(t *testing.T) {
	e := setup(t)
	if res := e.do("GET", "/auth", ""); res.StatusCode != 401 {
		t.Errorf("logged out: %d, want 401", res.StatusCode)
	}
	id := testenv.AddUser(t, e.db, "alice")
	get := func() (int, string) {
		res := e.do("GET", "/auth", id)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		return res.StatusCode, string(b)
	}
	c, b := get()
	want := `{"email":"alice@example.com","user":"alice","id":"` + id + `","verified":true}`
	if c != 200 || strings.TrimSpace(b) != want {
		t.Errorf("verified: %d %s, want %s", c, b, want)
	}
	e.db.Pool.Exec(context.Background(), `UPDATE "user" SET verified=false WHERE id=$1`, id)
	if c, b := get(); c != 200 || !strings.Contains(b, `"verified":false`) {
		t.Errorf("unverified: %d %s", c, b)
	}
	e.db.Pool.Exec(context.Background(), `UPDATE "user" SET verified=NULL WHERE id=$1`, id)
	if c, b := get(); c != 200 || !strings.Contains(b, `"verified":null`) {
		t.Errorf("null verified: %d %s", c, b)
	}
}

func TestLoginRoute(t *testing.T) {
	e := setup(t)
	id := testenv.AddUser(t, e.db, "alice")
	e.setPassword(id, "hunter2")
	res, body := e.form("POST", "/auth", "", url.Values{"email": {"Alice@example.com"}, "pass": {"hunter2"}})
	c := chatsCookie(res)
	if res.StatusCode != 200 || c == nil || !strings.Contains(body, `"user":"alice"`) || !strings.Contains(body, `"id":"`+id+`"`) {
		t.Fatalf("login: %d %v %s", res.StatusCode, res.Header["Set-Cookie"], body)
	}
	pref := e.doReq("GET", "/preference", "", "", "Cookie", "chats="+c.Value)
	pref.Body.Close()
	if pref.StatusCode != 200 {
		t.Errorf("GET /preference with the new cookie: %d", pref.StatusCode)
	}
	// the query is the fallback for a field the body lacks
	res, _ = e.form("POST", "/auth?email=alice&pass=hunter2", "", url.Values{})
	if res.StatusCode != 200 {
		t.Errorf("query login: %d", res.StatusCode)
	}

	res, body = e.form("POST", "/auth", "", url.Values{"email": {"alice"}, "pass": {"wrong"}})
	if res.StatusCode != 401 || chatsCookie(res) != nil || strings.TrimSpace(body) != `{"status":"error","error":"Invalid email or password"}` {
		t.Errorf("wrong password: %d %v %s", res.StatusCode, res.Header["Set-Cookie"], body)
	}
}

func TestLoginCrossSiteRefused(t *testing.T) {
	e := setupCfg(t, config.Config{Secret: testSecret, Origins: []string{"https://ok"}})
	res, _ := e.form("POST", "/auth", "", url.Values{"email": {"a"}, "pass": {"b"}}, "Sec-Fetch-Site", "cross-site")
	if res.StatusCode != 403 {
		t.Errorf("cross-site login: %d, want 403", res.StatusCode)
	}
}

func TestLogoutRoute(t *testing.T) {
	e := setup(t)
	id := testenv.AddUser(t, e.db, "alice")
	for _, m := range []string{"POST", "GET"} {
		res := e.do(m, "/logout", id)
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		c := chatsCookie(res)
		if res.StatusCode != 200 || strings.TrimSpace(string(b)) != `{"status":"ok"}` {
			t.Errorf("%s /logout: %d %s", m, res.StatusCode, b)
		}
		if c == nil || c.MaxAge != -1 || c.Value != "" || !c.Expires.Before(time.Now()) {
			t.Errorf("%s /logout cookie: %v", m, res.Header["Set-Cookie"])
		}
		if sc := res.Header.Get("Set-Cookie"); !strings.Contains(sc, "Max-Age=0") || !strings.Contains(sc, "HttpOnly") || !strings.Contains(sc, "SameSite=Strict") {
			t.Errorf("%s /logout Set-Cookie: %s", m, sc)
		}
	}
	if res := e.do("GET", "/auth", ""); res.StatusCode != 401 {
		t.Errorf("GET /auth after logout: %d", res.StatusCode)
	}
}

func TestRegisterRoute(t *testing.T) {
	e, rec := mailEnv(t)
	res, body := e.form("POST", "/register", "", url.Values{"username": {"newbie"}, "email": {"new@example.com"}, "pass": {"s3cret"}})
	if res.StatusCode != 200 || chatsCookie(res) == nil || !strings.Contains(body, `"verified":false`) || !strings.Contains(body, `"user":"newbie"`) {
		t.Fatalf("register: %d %v %s", res.StatusCode, res.Header["Set-Cookie"], body)
	}
	m := rec.wait(t, 1)
	if m.to != "new@example.com" || !strings.Contains(m.body, "https://site.example/verify/#") {
		t.Errorf("verify email: %+v", m)
	}
	res, body = e.form("POST", "/register", "", url.Values{"username": {"bad name"}, "email": {"x@example.com"}, "pass": {"p"}})
	if res.StatusCode != 400 || !strings.Contains(body, "Usernames can only use") {
		t.Errorf("bad username: %d %s", res.StatusCode, body)
	}
}

var tokenRE = regexp.MustCompile(`/#([A-Za-z0-9_-]+)`)

func TestVerifyResendResetRoutes(t *testing.T) {
	e, rec := mailEnv(t)
	res, body := e.form("POST", "/register", "", url.Values{"username": {"newbie"}, "email": {"new@example.com"}, "pass": {"s3cret"}})
	if res.StatusCode != 200 {
		t.Fatalf("register: %d %s", res.StatusCode, body)
	}
	var u struct{ ID string }
	json.Unmarshal([]byte(body), &u)
	token := tokenRE.FindStringSubmatch(rec.wait(t, 1).body)[1]

	// resend: logged in, unverified; a minute hasn't passed, so no new
	// email, but it answers ok. Logged out it is a 401.
	if res, _ := e.form("POST", "/verify/resend", "", url.Values{}); res.StatusCode != 401 {
		t.Errorf("logged out resend: %d", res.StatusCode)
	}
	res, body = e.form("POST", "/verify/resend", u.ID, url.Values{})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Errorf("resend: %d %s", res.StatusCode, body)
	}
	e.db.Pool.Exec(context.Background(), `UPDATE "user" SET verify_sent = now() - interval '1 hour' WHERE id=$1`, u.ID)
	if res, _ := e.form("POST", "/verify/resend", u.ID, url.Values{}); res.StatusCode != 200 {
		t.Errorf("resend again: %d", res.StatusCode)
	}
	rec.wait(t, 2)

	res, body = e.form("POST", "/verify", "", url.Values{"token": {"junk"}})
	if res.StatusCode != 400 || !strings.Contains(body, "This confirmation link has expired or isn't valid.") {
		t.Errorf("bad verify: %d %s", res.StatusCode, body)
	}
	res, body = e.form("POST", "/verify", "", url.Values{"token": {token}})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Errorf("verify: %d %s", res.StatusCode, body)
	}

	res, body = e.form("POST", "/reset", "", url.Values{"email": {"nobody@example.com"}})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Errorf("reset unknown: %d %s", res.StatusCode, body)
	}
	res, _ = e.form("POST", "/reset", "", url.Values{"email": {"new@example.com"}})
	if res.StatusCode != 200 {
		t.Errorf("reset: %d", res.StatusCode)
	}
	token = tokenRE.FindStringSubmatch(rec.wait(t, 3).body)[1]
	res, body = e.form("POST", "/reset/confirm", "", url.Values{"token": {token}, "pass": {"brandnew"}})
	if res.StatusCode != 200 || chatsCookie(res) == nil || !strings.Contains(body, `"user":"newbie"`) {
		t.Errorf("reset confirm: %d %v %s", res.StatusCode, res.Header["Set-Cookie"], body)
	}
	res, _ = e.form("POST", "/auth", "", url.Values{"email": {"newbie"}, "pass": {"brandnew"}})
	if res.StatusCode != 200 {
		t.Errorf("login with the new password: %d", res.StatusCode)
	}
}
