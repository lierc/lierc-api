package session

import (
	"crypto/hmac"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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

var fixedNow = time.Unix(1_800_000_000, 0)

func codec() Codec { return Codec{Secret: secret, Now: func() time.Time { return fixedNow }} }

func TestChats2RoundTrip(t *testing.T) {
	c := codec()
	v := c.Encode("abc123")
	if u, ok := c.Decode(v); !ok || u != "abc123" {
		t.Fatalf("got %q %v", u, ok)
	}
	if !strings.Contains(v, ".1800604800.") {
		t.Fatalf("expiry: %s", v)
	}
}

func TestChats2Rejects(t *testing.T) {
	c := codec()
	v := c.Encode("abc")
	parts := strings.Split(v, ".")
	later := Codec{Secret: secret, Now: func() time.Time { return fixedNow.Add(8 * 24 * time.Hour) }}
	other := Codec{Secret: "nope", Now: c.Now}
	for name, tc := range map[string]struct {
		c Codec
		v string
	}{
		"expired":     {later, v},
		"user":        {c, "abd." + parts[1] + "." + parts[2]},
		"expiry":      {c, parts[0] + ".1900000000." + parts[2]},
		"secret":      {other, v},
		"dotuser":     {c, c.Encode("a.b")},
		"garbage":     {c, "garbage"},
		"empty":       {c, ""},
		"emptyuser":   {c, c.Encode("")},
		"signedexp":   {c, parts[0] + ".+" + parts[1] + "." + parts[2]},
		"hugeexpiry":  {c, parts[0] + ".99999999999999999999." + parts[2]},
		"fourparts":   {c, v + ".x"},
		"expiredzero": {c, parts[0] + ".0." + parts[2]},
	} {
		if u, ok := tc.c.Decode(tc.v); ok {
			t.Errorf("%s accepted as %q", name, u)
		}
	}
}

func FuzzCodecDecode(f *testing.F) {
	f.Add(codec().Encode("abc"))
	f.Fuzz(func(t *testing.T, s string) { _, _ = codec().Decode(s); _, _ = ReadPerl(s, secret) })
}

// run serves one request through the middleware and returns the response.
func run(c Codec, cookies []*http.Cookie, h func(s *Session)) *http.Response {
	req := httptest.NewRequest("GET", "/", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(FromContext(r.Context()))
		w.Write([]byte("ok"))
	})).ServeHTTP(rec, req)
	return rec.Result()
}

func find(res *http.Response, name string) *http.Cookie {
	for _, ck := range res.Cookies() {
		if ck.Name == name {
			return ck
		}
	}
	return nil
}

func perlCookie(t testing.TB, name string) *http.Cookie {
	for _, f := range fixtures(t) {
		if f.Name == name {
			return &http.Cookie{Name: "chats", Value: f.Value}
		}
	}
	t.Fatal("no fixture " + name)
	return nil
}

func TestMiddlewarePrefersChats2(t *testing.T) {
	c := codec()
	var got string
	run(c, []*http.Cookie{perlCookie(t, "login_ascii"), {Name: "chats2", Value: c.Encode("userA")}},
		func(s *Session) { got = s.User() })
	if got != "userA" {
		t.Fatalf("got %q", got)
	}
}

func TestMiddlewareMigrates(t *testing.T) {
	for _, secure := range []bool{false, true} {
		c := codec()
		c.Secure = secure
		var got string
		res := run(c, []*http.Cookie{perlCookie(t, "login_ascii")}, func(s *Session) { got = s.User() })
		if got != "lee" {
			t.Fatalf("got %q", got)
		}
		ck := find(res, "chats2")
		if ck == nil || ck.Path != "/" || !ck.HttpOnly || ck.MaxAge != 604800 || ck.Secure != secure {
			t.Fatalf("bad cookie %+v", ck)
		}
		if u, ok := c.Decode(ck.Value); !ok || u != "lee" {
			t.Fatalf("value %q", ck.Value)
		}
		if find(res, "chats") != nil {
			t.Fatal("chats must not be written")
		}
	}
}

func TestMiddlewareBadChats2FallsBack(t *testing.T) {
	c := codec()
	var got string
	run(c, []*http.Cookie{{Name: "chats2", Value: "lee.1.deadbeef"}, perlCookie(t, "login_numeric_id")},
		func(s *Session) { got = s.User() })
	if got != "12345" {
		t.Fatalf("got %q", got)
	}
}

func TestMiddlewareSlidingExpiry(t *testing.T) {
	now := fixedNow
	c := Codec{Secret: secret, Now: func() time.Time { return now }}
	old := c.Encode("u1")
	now = now.Add(3 * 24 * time.Hour)
	res := run(c, []*http.Cookie{{Name: "chats2", Value: old}}, func(*Session) {})
	ck := find(res, "chats2")
	if ck == nil || ck.Value == old || !strings.Contains(ck.Value, "."+"1800864000"+".") {
		t.Fatalf("not refreshed: %+v", ck)
	}
}

func TestMiddlewareSetUser(t *testing.T) {
	res := run(codec(), nil, func(s *Session) { s.SetUser("new") })
	if ck := find(res, "chats2"); ck == nil {
		t.Fatal("no cookie after SetUser")
	}
}

func TestMiddlewareClear(t *testing.T) {
	c := codec()
	res := run(c, []*http.Cookie{{Name: "chats2", Value: c.Encode("u")}, perlCookie(t, "login_ascii")},
		func(s *Session) { s.Clear() })
	// Path/Domain of the Perl cookie come from the fixture's Set-Cookie header.
	var hdr string
	for _, f := range fixtures(t) {
		if f.Name == "login_ascii" {
			hdr = f.SetCookie
		}
	}
	if !strings.Contains(hdr, "path=/") || strings.Contains(strings.ToLower(hdr), "domain=") {
		t.Fatalf("fixture header: %s", hdr)
	}
	for _, n := range []string{"chats2", "chats"} {
		ck := find(res, n)
		if ck == nil || ck.MaxAge != -1 || ck.Value != "" || ck.Path != "/" || ck.Domain != "" || ck.Secure {
			t.Fatalf("%s: %+v", n, ck)
		}
	}
	raw := strings.Join(res.Header.Values("Set-Cookie"), "\n")
	if strings.Count(raw, "Max-Age=0") != 2 {
		t.Fatalf("raw: %s", raw)
	}
}

func TestMiddlewareNoSession(t *testing.T) {
	res := run(codec(), nil, func(*Session) {})
	if len(res.Header.Values("Set-Cookie")) != 0 {
		t.Fatal("cookies set")
	}
	res = run(codec(), []*http.Cookie{{Name: "chats", Value: "junk"}}, func(*Session) {})
	if len(res.Header.Values("Set-Cookie")) != 0 {
		t.Fatal("cookies set for bad cookie")
	}
}

func TestMiddlewareStreaming(t *testing.T) {
	c := codec()
	srv := httptest.NewServer(c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		FromContext(r.Context()).SetUser("sse")
		w.Header().Set("Content-Type", "text/event-stream")
		rc := http.NewResponseController(w)
		if err := rc.Flush(); err != nil {
			t.Errorf("flush: %v", err)
		}
		<-r.Context().Done()
	})))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if find(res, "chats2") == nil {
		t.Fatal("cookie not sent with flushed headers")
	}
}

func sha1hex(s, key string) string {
	m := hmac.New(sha1.New, []byte(key))
	m.Write([]byte(s))
	return hex.EncodeToString(m.Sum(nil))
}

func TestMiddlewareClearSecure(t *testing.T) {
	c := codec()
	c.Secure = true
	res := run(c, nil, func(s *Session) { s.Clear() })
	for _, n := range []string{"chats2", "chats"} {
		if ck := find(res, n); ck == nil || !ck.Secure || ck.MaxAge != -1 {
			t.Fatalf("%s: %+v", n, ck)
		}
	}
}

func serveEmpty(c Codec, cookies []*http.Cookie, h func(s *Session)) *http.Response {
	req := httptest.NewRequest("GET", "/", nil)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h(FromContext(r.Context()))
	})).ServeHTTP(rec, req)
	return rec.Result()
}

func TestMiddlewareEmptyHandlerSetUser(t *testing.T) {
	res := serveEmpty(codec(), nil, func(s *Session) { s.SetUser("u9") })
	if ck := find(res, "chats2"); ck == nil {
		t.Fatal("cookie lost when handler writes nothing")
	}
}

func TestMiddlewareEmptyHandlerClear(t *testing.T) {
	res := serveEmpty(codec(), nil, func(s *Session) { s.Clear() })
	if find(res, "chats2") == nil || find(res, "chats") == nil {
		t.Fatal("expiry cookies lost when handler writes nothing")
	}
}

type failFlusher struct{ *httptest.ResponseRecorder }

var errFlush = errors.New("flush failed")

func (failFlusher) FlushError() error { return errFlush }

func TestFlushErrorPropagates(t *testing.T) {
	var got error
	h := Codec{Secret: secret}.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = http.NewResponseController(w).Flush()
	}))
	h.ServeHTTP(failFlusher{httptest.NewRecorder()}, httptest.NewRequest("GET", "/", nil))
	if !errors.Is(got, errFlush) {
		t.Fatalf("Flush() = %v", got)
	}
}
