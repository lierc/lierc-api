package api

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"

	"github.com/lierc/lierc-api/internal/testenv"
)

var (
	goodKey  = base64.RawURLEncoding.EncodeToString(append([]byte{4}, []byte(strings.Repeat("k", 64))...))
	goodAuth = base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 16)))
	b64      = base64.RawURLEncoding.EncodeToString
)

// The cases from t/web_push.t.
func TestCheckSubscription(t *testing.T) {
	for _, ok := range []string{
		"https://fcm.googleapis.com/fcm/send/abc:APA91b-x_y",
		"https://updates.push.services.mozilla.com/wpush/v2/gAAAAA",
		"https://wns2-par02p.notify.windows.com/w/?token=BQYAAA%3d",
		"https://web.push.apple.com/QGuQyavXutnMH",
		"https://FCM.googleapis.com:443/fcm/send/abc",
	} {
		if msg := checkSubscription(ok, goodKey, goodAuth); msg != "" {
			t.Errorf("%s: %q, want accepted", ok, msg)
		}
	}
	for _, c := range [][2]string{
		{"http://fcm.googleapis.com/fcm/send/abc", "endpoint must be an https URL"},
		{"https://127.0.0.1:5005/abc/destroy", "endpoint must be an https URL"},
		{"https://127.0.0.1/abc/destroy", "endpoint is not a known push service"},
		{"https://localhost/push", "endpoint is not a known push service"},
		{"https://fcm.googleapis.com.evil.example/x", "endpoint is not a known push service"},
		{"https://evil.example/fcm.googleapis.com", "endpoint is not a known push service"},
		{"https://notify.windows.com/x", "endpoint is not a known push service"},
		{"https://fcm.googleapis.com:4151/pub", "endpoint must be an https URL"},
		{"https://user@fcm.googleapis.com/x", "endpoint must be an https URL"},
		{"https://fcm.googleapis.com/a b", "endpoint must be an https URL"},
		{"https://fcm.googleapis.com/" + strings.Repeat("x", 250), "endpoint is too long"},
		{"", "endpoint is required"},
	} {
		if msg := checkSubscription(c[0], goodKey, goodAuth); msg != c[1] {
			t.Errorf("%s: %q, want %q", c[0], msg, c[1])
		}
	}
	fcm := "https://fcm.googleapis.com/fcm/send/abc"
	for _, c := range []struct{ key, auth, want string }{
		{"", goodAuth, "key is required"},
		{"not base64!", goodAuth, "key is not base64url"},
		{b64(append([]byte{4}, []byte(strings.Repeat("k", 10))...)), goodAuth, "key has the wrong length"},
		{b64(append([]byte{2}, []byte(strings.Repeat("k", 64))...)), goodAuth, "key is not an uncompressed P-256 key"},
		{goodKey, b64([]byte(strings.Repeat("a", 8))), "auth has the wrong length"},
		{goodKey, "", "auth is required"},
	} {
		if msg := checkSubscription(fcm, c.key, c.auth); msg != c.want {
			t.Errorf("key %q auth %q: %q, want %q", c.key, c.auth, msg, c.want)
		}
	}
	// padded base64url is accepted
	if msg := checkSubscription(fcm, base64.URLEncoding.EncodeToString(append([]byte{4}, []byte(strings.Repeat("k", 64))...)), goodAuth); msg != "" {
		t.Errorf("padded key: %q", msg)
	}
}

func TestWebPushRoutes(t *testing.T) {
	e := setup(t)
	a := testenv.AddUser(t, e.db, "alice")
	const ep = "https://fcm.googleapis.com/fcm/send/abc"
	form := "application/x-www-form-urlencoded; charset=UTF-8"
	post := func(query, body string) (int, string) {
		return e.send("POST", "/notification/web_push"+query, a, body, "Content-Type", form)
	}
	body := url.Values{"endpoint": {ep}, "key": {goodKey}, "auth": {goodAuth}}.Encode()
	if c, b := post("", body); c != 204 {
		t.Fatalf("subscribe: %d %s", c, b)
	}
	if v := e.row(`SELECT endpoint || ' ' || key || ' ' || auth FROM web_push WHERE "user"=$1`, a); v != ep+" "+goodKey+" "+goodAuth {
		t.Errorf("row = %s", v)
	}
	if c, b := post("", url.Values{"endpoint": {"http://fcm.googleapis.com/x"}, "key": {goodKey}, "auth": {goodAuth}}.Encode()); c != 400 || b != `{"status":"error","error":"endpoint must be an https URL"}` {
		t.Errorf("http endpoint: %d %s", c, b)
	}
	ep2 := ep + "2"
	if c, b := post("?key="+url.QueryEscape(goodKey), url.Values{"endpoint": {ep2}, "auth": {goodAuth}}.Encode()); c != 204 {
		t.Errorf("key in query: %d %s", c, b)
	}
	if c, b := post("?key="+url.QueryEscape(goodKey), url.Values{"endpoint": {ep2}, "key": {"not base64!"}, "auth": {goodAuth}}.Encode()); c != 400 || b != `{"status":"error","error":"key is not base64url"}` {
		t.Errorf("body key wins: %d %s", c, b)
	}
	e.sendWant("DELETE", "/notification/web_push?endpoint="+url.QueryEscape(ep), a, "", 204, "")
	if v := e.row(`SELECT count(*)::text FROM web_push WHERE endpoint=$1`, ep); v != "0" {
		t.Errorf("%s rows left", v)
	}
	e.sendWant("DELETE", "/notification/web_push", a, "", 400, `{"status":"error","error":"endpoint is required"}`)
	e.sendWant("POST", "/notification/web_push", "", body, 401, `{"status":"error","error":"unauthorized"}`)
	e.sendWant("DELETE", "/notification/web_push?endpoint=x", "", "", 401, `{"status":"error","error":"unauthorized"}`)
}
