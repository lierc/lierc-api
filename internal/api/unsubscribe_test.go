package api

import (
	"encoding/base64"
	"mime/multipart"
	"net/http"
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

// RFC 8058 allows the one-click body as multipart/form-data. The token is
// in the query either way, so the body only has to be accepted.
func TestUnsubscribeOneClickMultipart(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	var body strings.Builder
	mw := multipart.NewWriter(&body)
	mw.WriteField("List-Unsubscribe", "One-Click")
	mw.Close()
	res := e.doReq("POST", "/unsubscribe?token="+url.QueryEscape(e.unsubscribeToken(a)), "", body.String(), "Content-Type", mw.FormDataContentType())
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("multipart one-click: %d", res.StatusCode)
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

// A mail client that opens the List-Unsubscribe URL instead of posting to
// it lands on the page, with nothing changed.
func TestUnsubscribeGetRedirects(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	tok := e.unsubscribeToken(a)
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, c := range []struct{ query, want string }{
		{"?token=" + url.QueryEscape(tok), "/unsubscribe/#" + tok},
		{"", "/unsubscribe/"},
		{"?token=" + url.QueryEscape("a b<\r\n>"), "/unsubscribe/"},
	} {
		res, err := noFollow.Get(e.srv.URL + "/unsubscribe" + c.query)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != 303 || res.Header.Get("Location") != c.want {
			t.Errorf("GET %q: %d %q, want 303 %q", c.query, res.StatusCode, res.Header.Get("Location"), c.want)
		}
	}
	if v := e.emailPref(a); v != "none" {
		t.Errorf("email pref = %s after GETs, want untouched", v)
	}
}

// A webmail page that sends the one-click POST from the browser makes a
// cross-site request. It's let through: unsubscribe reads no cookie, only
// the token, so there's no session for another site to borrow. A bad token
// is still refused.
func TestUnsubscribeCrossSite(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	for name, hdr := range map[string][]string{
		"Sec-Fetch-Site": {"Sec-Fetch-Site", "cross-site"},
		"Origin":         {"Origin", "https://webmail.example"},
	} {
		res := e.doReq("POST", "/unsubscribe?token="+url.QueryEscape(e.unsubscribeToken(a)), "", "List-Unsubscribe=One-Click",
			append([]string{"Content-Type", formType}, hdr...)...)
		res.Body.Close()
		if res.StatusCode != 200 {
			t.Errorf("%s: %d, want 200", name, res.StatusCode)
		}
		res = e.doReq("POST", "/unsubscribe?token=junk", "", "List-Unsubscribe=One-Click",
			append([]string{"Content-Type", formType}, hdr...)...)
		res.Body.Close()
		if res.StatusCode != 400 {
			t.Errorf("%s, bad token: %d, want 400", name, res.StatusCode)
		}
	}
	if v := e.emailPref(a); v != "false" {
		t.Errorf("email = %s, want false", v)
	}
}
