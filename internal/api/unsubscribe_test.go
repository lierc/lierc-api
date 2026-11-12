package api

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/accounts"
	"github.com/lierc/lierc-api/internal/testenv"
)

const badUnsubscribe = `{"status":"error","error":"This unsubscribe link isn't valid."}`

func (e *env) unsubscribeToken(user string) string {
	return accounts.UnsubscribeToken(e.cfg.UnsubscribeSecret, user)
}

func (e *env) emailPref(user string) string {
	e.t.Helper()
	return e.row(`SELECT coalesce((SELECT value FROM pref WHERE "user"=$1 AND name='email'), 'none')`, user)
}

func TestUnsubscribeRoute(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	res, body := e.form("POST", "/unsubscribe", "", url.Values{"token": {e.unsubscribeToken(a)}})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Fatalf("unsubscribe: %d %s", res.StatusCode, body)
	}
	if v := e.emailPref(a); v != "false" {
		t.Errorf("email = %s, want false", v)
	}
}

func TestUnsubscribeTampered(t *testing.T) {
	e := setup(t)
	a, b := testenv.AddUser(t, e.db, "alice"), testenv.AddUser(t, e.db, "bob")
	raw, _ := base64.RawURLEncoding.DecodeString(e.unsubscribeToken(a))
	mac := string(raw)[strings.LastIndex(string(raw), ":")+1:]
	tok := base64.RawURLEncoding.EncodeToString([]byte(b + ":" + mac))
	res, body := e.form("POST", "/unsubscribe", "", url.Values{"token": {tok}})
	if res.StatusCode != 400 || strings.TrimSpace(body) != badUnsubscribe {
		t.Errorf("tampered: %d %s", res.StatusCode, body)
	}
	if v := e.emailPref(b); v != "none" {
		t.Errorf("bob's email pref = %s, want untouched", v)
	}
}

func TestUnsubscribeBad(t *testing.T) {
	e := setup(t)
	for name, v := range map[string]url.Values{"missing": {}, "garbage": {"token": {"junk"}}} {
		res, body := e.form("POST", "/unsubscribe", "", v)
		if res.StatusCode != 400 || strings.TrimSpace(body) != badUnsubscribe {
			t.Errorf("%s: %d %s", name, res.StatusCode, body)
		}
	}
}

func TestUnsubscribeOneClick(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	res := e.doReq("POST", "/unsubscribe?token="+url.QueryEscape(e.unsubscribeToken(a)), "", "List-Unsubscribe=One-Click", "Content-Type", formType)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("one-click: %d", res.StatusCode)
	}
	if v := e.emailPref(a); v != "false" {
		t.Errorf("email = %s, want false", v)
	}
}

func TestUnsubscribeGoneUser(t *testing.T) {
	e := setup(t)
	res, body := e.form("POST", "/unsubscribe", "", url.Values{"token": {e.unsubscribeToken("nosuchuser00000000000")}})
	if res.StatusCode != 200 || strings.TrimSpace(body) != `{"status":"ok"}` {
		t.Errorf("gone user: %d %s", res.StatusCode, body)
	}
}

func TestUnsubscribeGetGone(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	res := e.do("GET", "/unsubscribe", a)
	res.Body.Close()
	if res.StatusCode != 404 {
		t.Errorf("GET /unsubscribe: %d, want 404", res.StatusCode)
	}
}
