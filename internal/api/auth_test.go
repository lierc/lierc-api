package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/quotedprintable"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/mail"
	"github.com/lierc/lierc-api/internal/password"
	"github.com/lierc/lierc-api/internal/session"
	"github.com/lierc/lierc-api/internal/testenv"
)

const authSecret = "testsecret"

var (
	hashOnce sync.Once
	hashPW   string
)

// aliceHash is the (slow, cost 15) hash of "hunter2", computed once.
func aliceHash(t *testing.T) string {
	t.Helper()
	hashOnce.Do(func() {
		var err error
		if hashPW, err = password.Hash("hunter2", authSecret); err != nil {
			t.Fatal(err)
		}
	})
	return hashPW
}

type authEnv struct {
	t     *testing.T
	h     http.Handler
	db    *db.DB
	codec session.Codec
	id    string
	msgs  func() []mail.Received
}

// setupAuth serves the real route table with a user alice / hunter2 and a
// fake SMTP server.
func setupAuth(t *testing.T, noSMTP bool) *authEnv {
	t.Helper()
	d := testenv.Postgres(t)
	id, _, err := d.AddUser(context.Background(), "alice", "alice@example.com", aliceHash(t))
	if err != nil {
		t.Fatal(err)
	}
	smtp, msgs := testenv.FakeSMTP(t)
	cfg := config.Config{Secret: authSecret, FromAddress: "noreply@example.org", NoSMTP: noSMTP}
	app := &App{Cfg: cfg, DB: d, Mail: mail.New(smtp, cfg.FromAddress, false), AssetDir: "."}
	codec := session.Codec{Secret: authSecret}
	return &authEnv{t: t, h: codec.Middleware(newHandler(app, routes)), db: d, codec: codec, id: id, msgs: msgs}
}

func (e *authEnv) do(method, target string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	e.t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *authEnv) session() *http.Cookie {
	return &http.Cookie{Name: "chats2", Value: e.codec.Encode(e.id)}
}

func setCookie(rec *httptest.ResponseRecorder, name string) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func expectJSON(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	if rec.Code != 200 {
		t.Fatalf("status = %d (body %s)", rec.Code, rec.Body)
	}
	var a, b any
	if err := json.Unmarshal(rec.Body.Bytes(), &a); err != nil {
		t.Fatal(err)
	}
	json.Unmarshal([]byte(want), &b)
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Fatalf("body = %s, want %s", rec.Body, want)
	}
}

func TestHealth(t *testing.T) {
	e := setupAuth(t, true)
	expectJSON(t, e.do("GET", "/health", nil, e.session()), `{"status":"ok"}`)
}

func TestShow(t *testing.T) {
	e := setupAuth(t, true)
	expectJSON(t, e.do("GET", "/auth", nil, e.session()),
		`{"email":"alice@example.com","user":"alice","id":"`+e.id+`"}`)
}

func TestLoginByEmailAndUsername(t *testing.T) {
	e := setupAuth(t, true)
	for _, who := range []string{"alice@example.com", "alice"} {
		rec := e.do("POST", "/auth", url.Values{"email": {who}, "pass": {"hunter2"}})
		expectJSON(t, rec, `{"email":"alice@example.com","user":"alice","id":"`+e.id+`"}`)
	}
}

func TestLoginBadPassword(t *testing.T) {
	e := setupAuth(t, true)
	expectError(t, e.do("POST", "/auth", url.Values{"email": {"alice"}, "pass": {"nope"}}),
		401, "Invalid email or password")
	expectError(t, e.do("POST", "/auth", url.Values{}), 401, "Invalid email or password")
}

func TestLoginSetsChats2(t *testing.T) {
	e := setupAuth(t, true)
	rec := e.do("POST", "/auth", url.Values{"email": {"alice"}, "pass": {"hunter2"}})
	c := setCookie(rec, "chats2")
	if c == nil {
		t.Fatal("no chats2 cookie")
	}
	if u, ok := e.codec.Decode(c.Value); !ok || u != e.id {
		t.Fatalf("Decode = %q, %v", u, ok)
	}
	if setCookie(rec, "chats") != nil {
		t.Fatal("chats must not be set")
	}
	// The cookie works.
	expectJSON(t, e.do("GET", "/health", nil, c), `{"status":"ok"}`)
}

func TestRegisterRequiredFields(t *testing.T) {
	e := setupAuth(t, true)
	full := url.Values{"username": {"bob"}, "email": {"bob@example.com"}, "pass": {"pw"}}
	for _, f := range []string{"username", "email", "pass"} {
		for _, bad := range []*string{nil, ptr(""), ptr(" \t\n"), ptr("\v")} {
			form := url.Values{}
			for k, v := range full {
				form[k] = v
			}
			delete(form, f)
			if bad != nil {
				form.Set(f, *bad)
			}
			expectError(t, e.do("POST", "/register", form), 400, f+" is required")
		}
	}
	// Order: username is reported before email before pass.
	expectError(t, e.do("POST", "/register", url.Values{}), 400, "username is required")
	expectError(t, e.do("POST", "/register", url.Values{"username": {"b"}}), 400, "email is required")
	expectError(t, e.do("POST", "/register", url.Values{"username": {"b"}, "email": {"b@example.com"}}), 400, "pass is required")
}

func ptr(s string) *string { return &s }

func TestRegisterBadEmail(t *testing.T) {
	e := setupAuth(t, true)
	expectError(t, e.do("POST", "/register", url.Values{"username": {"bob"}, "email": {"nope"}, "pass": {"pw"}}),
		400, "Invalid email address")
}

func TestRegisterSendsMail(t *testing.T) {
	e := setupAuth(t, false)
	rec := e.do("POST", "/register", url.Values{"username": {"bob <&>"}, "email": {"bob@example.com"}, "pass": {"pw"}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var who struct{ Email, User, ID string }
	json.Unmarshal(rec.Body.Bytes(), &who)
	if who.Email != "bob@example.com" || who.User != "bob <&>" || who.ID == "" {
		t.Fatalf("body = %s", rec.Body)
	}
	if c := setCookie(rec, "chats2"); c == nil {
		t.Fatal("register did not log in")
	}

	var token string
	if err := e.db.Pool.QueryRow(context.Background(), `SELECT verify_token FROM "user" WHERE id=$1`, who.ID).Scan(&token); err != nil {
		t.Fatal(err)
	}

	msgs := e.msgs()
	if len(msgs) != 1 {
		t.Fatalf("%d messages", len(msgs))
	}
	m := msgs[0]
	if m.From != "noreply@example.org" || len(m.To) != 1 || m.To[0] != "bob@example.com" {
		t.Errorf("envelope %+v", m)
	}
	head, enc, _ := strings.Cut(m.Data, "\r\n\r\n")
	for _, want := range []string{
		"From: noreply@example.org", "To: bob@example.com",
		"Subject: Please verify your relaychat.party account",
		"Content-Transfer-Encoding: quoted-printable",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("header missing %q:\n%s", want, head)
		}
	}
	dec, _ := io.ReadAll(quotedprintable.NewReader(strings.NewReader(enc)))
	got := strings.ReplaceAll(strings.TrimSuffix(string(dec), "\r\n"), "\r\n", "\n")
	// Text::Xslate HTML-escapes <: $name :>.
	want := "Hello bob &lt;&amp;&gt;,\n\n" +
		"Thank you for signing up with relaychat.party!\n\n" +
		"Please click the following link to validate your email address:\n" +
		"https://relaychat.party/api/verify?" + token + "\n\n" +
		"You must visit this link in the next day, or your account will\n" +
		"be deleted from the system.\n\n" +
		"Happy chatting!"
	if got != want {
		t.Errorf("body:\n%q\nwant:\n%q", got, want)
	}
}

func TestRegisterNoSMTP(t *testing.T) {
	e := setupAuth(t, true)
	rec := e.do("POST", "/register", url.Values{"username": {"bob"}, "email": {"bob@example.com"}, "pass": {"pw"}})
	if rec.Code != 200 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if n := len(e.msgs()); n != 0 {
		t.Fatalf("%d messages sent with NoSMTP", n)
	}
}

func TestRegisterDuplicateEmail(t *testing.T) {
	e := setupAuth(t, true)
	rec := e.do("POST", "/register", url.Values{"username": {"other"}, "email": {"alice@example.com"}, "pass": {"pw"}})
	if rec.Code != 400 {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if setCookie(rec, "chats2") != nil {
		t.Fatal("failed register must not log in")
	}
}

func TestRegisterMailFailureStillLogsIn(t *testing.T) {
	e := setupAuth(t, false)
	app := &App{Cfg: config.Config{Secret: authSecret}, DB: e.db,
		Mail: mail.New(config.SMTPOpts{Host: "127.0.0.1", Port: 1}, "a@b.c", false), AssetDir: "."}
	h := e.codec.Middleware(newHandler(app, routes))
	req := httptest.NewRequest("POST", "/register", strings.NewReader(url.Values{
		"username": {"bob"}, "email": {"bob@example.com"}, "pass": {"pw"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 || setCookie(rec, "chats2") == nil {
		t.Fatalf("status %d cookie %v body %s", rec.Code, setCookie(rec, "chats2"), rec.Body)
	}
}

func TestLogoutClearsUser(t *testing.T) {
	e := setupAuth(t, true)
	for _, m := range []string{"GET", "POST"} {
		rec := e.do(m, "/logout", nil, e.session())
		expectJSON(t, rec, `{"status":"ok"}`)
		for _, n := range []string{"chats2", "chats"} {
			if c := setCookie(rec, n); c == nil || c.MaxAge >= 0 || c.Value != "" {
				t.Errorf("%s not expired: %v", n, c)
			}
		}
	}
}

func TestLogoutWithoutSession(t *testing.T) {
	e := setupAuth(t, true)
	expectJSON(t, e.do("GET", "/logout", nil), `{"status":"ok"}`)
}

func TestLogoutAfterMigration(t *testing.T) {
	// A user who only has Perl's "chats" cookie logs out; the cookies the
	// browser holds afterwards (the response expires both) no longer log in.
	e := setupAuth(t, true)
	e.codec = session.Codec{Secret: "test-secret"}
	app := &App{Cfg: config.Config{Secret: authSecret}, DB: e.db, AssetDir: "."}
	e.h = e.codec.Middleware(newHandler(app, routes))

	fx, err := os.ReadFile("../../testdata/fixtures/cookies.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name  string `json:"name"`
		Value string `json:"cookie_value"`
	}
	if err := json.Unmarshal(fx, &fixtures); err != nil {
		t.Fatal(err)
	}
	var chats *http.Cookie
	for _, f := range fixtures {
		if f.Name == "login_numeric_id" {
			chats = &http.Cookie{Name: "chats", Value: f.Value}
		}
	}
	if chats == nil {
		t.Fatal("no login_numeric_id fixture")
	}
	if _, err := e.db.Pool.Exec(context.Background(),
		`INSERT INTO "user" (id, username, email, password, verify_token) VALUES ('12345','perl','perl@example.com','x','t')`); err != nil {
		t.Fatal(err)
	}
	expectJSON(t, e.do("GET", "/health", nil, chats), `{"status":"ok"}`)

	rec := e.do("GET", "/logout", nil, chats)
	expectJSON(t, rec, `{"status":"ok"}`)

	// Apply the response to a cookie jar holding the old cookie.
	jar := map[string]*http.Cookie{"chats": chats}
	for _, c := range rec.Result().Cookies() {
		if c.MaxAge < 0 {
			delete(jar, c.Name)
		} else {
			jar[c.Name] = c
		}
	}
	if len(jar) != 0 {
		t.Fatalf("cookies left after logout: %v", jar)
	}
	expectError(t, e.do("GET", "/health", nil), 401, "unauthorized")
}

func TestTokenShape(t *testing.T) {
	e := setupAuth(t, true)
	rec := e.do("GET", "/token", nil, e.session())
	var b struct {
		Token string
		Extra []string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil || rec.Code != 200 {
		t.Fatalf("%d %s %v", rec.Code, rec.Body, err)
	}
	if len(b.Extra) != 6 {
		t.Fatalf("extra = %v", b.Extra)
	}
	seen := map[string]bool{}
	for _, tok := range append([]string{b.Token}, b.Extra...) {
		if seen[tok] {
			t.Fatalf("duplicate token %q", tok)
		}
		seen[tok] = true
		ok, err := e.db.CheckToken(context.Background(), e.id, tok)
		if err != nil || !ok {
			t.Fatalf("CheckToken(%q) = %v, %v", tok, ok, err)
		}
	}
}

func TestVerify(t *testing.T) {
	e := setupAuth(t, true)
	var token string
	e.db.Pool.QueryRow(context.Background(), `SELECT verify_token FROM "user" WHERE id=$1`, e.id).Scan(&token)
	verified := func() bool {
		var v *bool
		e.db.Pool.QueryRow(context.Background(), `SELECT verified FROM "user" WHERE id=$1`, e.id).Scan(&v)
		return v != nil && *v
	}
	if verified() {
		t.Fatal("verified before verify")
	}
	// Unknown token: ok, nothing updated (empty arrayref is truthy in Auth.pm).
	expectJSON(t, e.do("GET", "/verify?nope", nil, e.session()), `{"status":"ok"}`)
	if verified() {
		t.Fatal("unknown token verified a user")
	}
	expectJSON(t, e.do("GET", "/verify?"+token, nil, e.session()), `{"status":"ok"}`)
	if !verified() {
		t.Fatal("token did not verify the user")
	}
	// Requires a login (route is not public).
	expectError(t, e.do("GET", "/verify?"+token, nil), 401, "unauthorized")
}

func TestIsEmail(t *testing.T) {
	b, err := os.ReadFile("../../testdata/fixtures/emails.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Email string `json:"email"`
		Valid bool   `json:"valid"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases) < 40 {
		t.Fatalf("only %d fixture cases", len(cases))
	}
	for _, c := range cases {
		if got := isEmail(c.Email); got != c.Valid {
			t.Errorf("isEmail(%q) = %v, Perl says %v", c.Email, got, c.Valid)
		}
	}
}

func TestRenderAuthTemplatePercent(t *testing.T) {
	got := renderAuthTemplate("a%sb", "https://x/y?%s")
	if !strings.HasPrefix(got, "Hello a%sb,\n") || !strings.Contains(got, "address:\nhttps://x/y?%s\n") {
		t.Errorf("rendered:\n%s", got)
	}
}

func TestLoginLongSecret(t *testing.T) {
	e := setupAuth(t, true)
	app := &App{Cfg: config.Config{Secret: "a-secret-longer-than-sixteen"}, DB: e.db, AssetDir: "."}
	h := e.codec.Middleware(newHandler(app, routes))
	req := httptest.NewRequest("POST", "/auth", strings.NewReader(url.Values{"email": {"alice"}, "pass": {"x"}}.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	expectError(t, rec, 400, "Salt must be exactly 16 octets long")
}

func TestHashPasswordBounded(t *testing.T) {
	const limit = 3
	oldSlots, oldFunc := hashSlots, hashFunc
	t.Cleanup(func() { hashSlots, hashFunc = oldSlots, oldFunc })
	hashSlots = make(chan struct{}, limit)
	var cur, peak atomic.Int32
	release := make(chan struct{})
	hashFunc = func(pass, secret string) (string, error) {
		n := cur.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		<-release
		cur.Add(-1)
		return "h", nil
	}

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := hashPassword(context.Background(), "p", "s"); err != nil {
				t.Error(err)
			}
		}()
	}
	// Wait until the slots are full, then check nobody else got in.
	for cur.Load() < limit {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(20 * time.Millisecond)
	if n := cur.Load(); n != limit {
		t.Fatalf("%d concurrent hashes, limit %d", n, limit)
	}

	// A waiter whose context ends gives up without hashing.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := hashPassword(ctx, "p", "s"); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled waiter: %v", err)
	}

	close(release)
	wg.Wait()
	if p := peak.Load(); p != limit {
		t.Fatalf("peak %d, want %d", p, limit)
	}
}
