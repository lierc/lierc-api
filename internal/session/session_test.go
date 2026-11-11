package session

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

const secret = "test-secret"

type fixture struct {
	Name      string            `json:"name"`
	Session   map[string]string `json:"session"`
	SetCookie string            `json:"set_cookie_header"`
	Value     string            `json:"cookie_value"`
}

func fixtures(t testing.TB) []fixture {
	b, err := os.ReadFile("../../testdata/fixtures/cookies.json")
	if err != nil {
		t.Fatal(err)
	}
	var f []fixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestReadPerlCookies(t *testing.T) {
	for _, f := range fixtures(t) {
		u, ok := ReadPerl(f.Value, secret)
		want := f.Session["user"]
		if ok != (want != "") || u != want {
			t.Errorf("%s: got %q,%v want %q", f.Name, u, ok, want)
		}
	}
}

func TestReadPerlRejects(t *testing.T) {
	good := fixtures(t)[0].Value
	parts := strings.Split(good, "%3A")
	for name, v := range map[string]string{
		"badsig":    strings.TrimSuffix(good, good[len(good)-1:]) + "0",
		"truncated": parts[0] + "%3ABQsDAAAAAQoDbGVlAAAABHVz%3A" + parts[len(parts)-1],
		"empty":     "",
		"nosep":     "abcdef",
		"badescape": "%zz:a:b",
	} {
		if u, ok := ReadPerl(v, secret); ok {
			t.Errorf("%s accepted as %q", name, u)
		}
	}
	// valid signature, truncated base64 / non-hash storable
	for name, b64 := range map[string]string{
		"truncb64": "BQsDAAAAAQoDbGVlAAAABHVz=",
		"array":    "BQsCAAAAAA==", // SX_ARRAY, empty
		"junk":     "AAAA",
	} {
		v := "1:" + b64 + ":" + hmacSHA1Hex(b64)
		if u, ok := ReadPerl(v, secret); ok {
			t.Errorf("%s accepted as %q", name, u)
		}
	}
}

func hmacSHA1Hex(s string) string {
	// reuse ReadPerl's primitive via a throwaway check: compute independently
	return sha1hex(s, secret)
}

func sha1hex(s, key string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func TestUser(t *testing.T) {
	good := fixtures(t)[0]
	cases := []struct {
		name    string
		cookies []*http.Cookie
		want    string
	}{
		{"chats", []*http.Cookie{{Name: "chats", Value: good.Value}}, good.Session["user"]},
		{"no cookie", nil, ""},
		{"other cookie", []*http.Cookie{{Name: "chats2", Value: good.Value}}, ""},
		{"bad signature", []*http.Cookie{{Name: "chats", Value: good.Value[:len(good.Value)-1] + "x"}}, ""},
		{"first wins", []*http.Cookie{{Name: "chats", Value: "junk"}, {Name: "chats", Value: good.Value}}, ""},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/", nil)
		for _, ck := range c.cookies {
			r.AddCookie(ck)
		}
		if got := User(r, secret); got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}

func TestWritePerlMatchesFixtures(t *testing.T) {
	b, err := os.ReadFile("../../testdata/fixtures/cookies.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name    string            `json:"name"`
		Value   string            `json:"cookie_value"`
		Session map[string]string `json:"session"`
	}
	if err := json.Unmarshal(b, &fixtures); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, f := range fixtures {
		switch f.Name {
		case "login_ascii", "login_numeric_id", "login_email_like":
		default:
			continue
		}
		raw, _ := url.QueryUnescape(f.Value)
		id := strings.SplitN(raw, ":", 2)[0]
		got, err := WritePerl(f.Session["user"], "test-secret", id)
		if err != nil || got != f.Value {
			t.Errorf("%s: %q, %v\nwant %q", f.Name, got, err, f.Value)
		}
		n++
	}
	if n != 3 {
		t.Fatalf("%d fixtures checked", n)
	}
}

func TestWritePerlRoundTrip(t *testing.T) {
	for _, u := range []string{"abc", strings.Repeat("u", 300)} {
		v, err := WritePerl(u, "s3cret", PerlID(time.Now()))
		if err != nil {
			t.Fatal(err)
		}
		if got, ok := ReadPerl(v, "s3cret"); !ok || got != u {
			t.Errorf("%d bytes: %q %v", len(u), got, ok)
		}
	}
	if _, err := WritePerl("pässwörd", "s", "1.0"); err == nil {
		t.Error("non-ASCII user written")
	}
}

func TestPerlID(t *testing.T) {
	if got := PerlID(time.Unix(1792769372, 785980000)); got != "1792769372.78598" {
		t.Errorf("PerlID = %q", got)
	}
}

func TestSetPerl(t *testing.T) {
	for _, secure := range []bool{false, true} {
		w := httptest.NewRecorder()
		now := time.Now()
		if err := SetPerl(w, "abc", "s3cret", secure, now); err != nil {
			t.Fatal(err)
		}
		cs := (&http.Response{Header: w.Header()}).Cookies()
		if len(cs) != 1 {
			t.Fatalf("cookies = %v", w.Header()["Set-Cookie"])
		}
		c := cs[0]
		h := w.Header().Get("Set-Cookie")
		if c.Name != "chats" || c.Path != "/" || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Secure != secure {
			t.Errorf("secure %v: %s", secure, h)
		}
		if d := c.Expires.Sub(now.Add(7 * 24 * time.Hour)); d > time.Minute || d < -time.Minute {
			t.Errorf("expires %v", c.Expires)
		}
		if u, ok := ReadPerl(c.Value, "s3cret"); !ok || u != "abc" {
			t.Errorf("value reads %q %v", u, ok)
		}
	}
}
