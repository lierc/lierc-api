package accounts

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/connections"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/passwords"
	"github.com/lierc/lierc-api/internal/testenv"
	"golang.org/x/crypto/bcrypt"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const (
	secret  = "0123456789abcdef"
	siteURL = "https://site.example"
)

type mail struct{ To, Subject, Body string }

type recorder struct {
	mu   sync.Mutex
	sent []mail
}

func (r *recorder) Send(to, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, mail{to, subject, body})
	return nil
}

func (r *recorder) all() []mail {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mail(nil), r.sent...)
}

type harness struct {
	t    *testing.T
	d    *db.DB
	fake *testenv.LiercdServer
	mail *recorder
	s    *Service
}

func setup(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, d: testenv.Postgres(t), fake: testenv.FakeLiercd(t), mail: &recorder{}}
	conns := &connections.Service{
		DB:               h.d,
		Liercd:           &liercd.Client{BaseURL: h.fake.URL, HTTP: &http.Client{}},
		Announce:         func(context.Context, db.ConnectionChange) { t.Error("CreateDefault announced") },
		NotifyHighlights: connections.NotifyHighlights(h.d),
		CheckHost:        func(context.Context, string, int) error { return nil },
		Limit:            5,
	}
	h.s = &Service{
		DB: h.d, Conns: conns, Mail: h.mail,
		Secret: secret, SiteURL: siteURL,
		DefaultNetwork: &config.DefaultNetwork{Host: "irc.example", Port: 6697, Ssl: true},
		Now:            time.Now,
		After:          func(f func()) { f() },
	}
	return h
}

var ctx = context.Background()

// addUser inserts a user with a password_hash for pass.
func (h *harness) addUser(username, email, pass string, verified *bool) string {
	h.t.Helper()
	hash, err := passwords.Hash(pass)
	if err != nil {
		h.t.Fatal(err)
	}
	return h.addUserHash(username, email, hash, verified)
}

func (h *harness) addUserHash(username, email, hash string, verified *bool) string {
	h.t.Helper()
	var id string
	err := h.d.Pool.QueryRow(ctx,
		`INSERT INTO "user" (id, username, email, password_hash, verified) VALUES (substr(md5(random()::text), 1, 22), $1, $2, $3, $4) RETURNING id`,
		username, email, hash, verified).Scan(&id)
	if err != nil {
		h.t.Fatal(err)
	}
	return id
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if _, err := h.d.Pool.Exec(ctx, sql, args...); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) connections(user string) []map[string]any {
	h.t.Helper()
	conns, err := h.d.Connections(ctx, user)
	if err != nil {
		h.t.Fatal(err)
	}
	var out []map[string]any
	for _, c := range conns {
		var m map[string]any
		json.Unmarshal(c.Config, &m)
		out = append(out, m)
	}
	return out
}

func (h *harness) verified(id string) *bool {
	h.t.Helper()
	var v *bool
	if err := h.d.Pool.QueryRow(ctx, `SELECT verified FROM "user" WHERE id=$1`, id).Scan(&v); err != nil {
		h.t.Fatal(err)
	}
	return v
}

func ptr(b bool) *bool { return &b }

func wantError(t *testing.T, err error, code int, msg string) {
	t.Helper()
	e, ok := IsError(err)
	if !ok || e.Code != code || e.Msg != msg {
		t.Errorf("error = %v, want %d %q", err, code, msg)
	}
}

// countChecks wraps checkPassword, counting calls and the hashes checked.
func countChecks(t *testing.T) func() []string {
	var mu sync.Mutex
	var hashes []string
	old := checkPassword
	checkPassword = func(pw, hash string) bool {
		mu.Lock()
		hashes = append(hashes, hash)
		mu.Unlock()
		return old(pw, hash)
	}
	t.Cleanup(func() { checkPassword = old })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hashes...)
	}
}

func TestLogin(t *testing.T) {
	h := setup(t)
	id := h.addUser("Alice", "alice@example.com", "secret pw", ptr(true))

	for _, login := range []string{"alice@example.com", "ALICE@Example.COM", "alice", "aLiCe"} {
		u, err := h.s.Login(ctx, login, "secret pw")
		if err != nil || u.ID != id || u.Username != "Alice" || u.Email != "alice@example.com" || u.Verified == nil || !*u.Verified {
			t.Errorf("Login(%q) = %+v, %v", login, u, err)
		}
	}

	_, err := h.s.Login(ctx, "alice", "wrong")
	wantError(t, err, 401, "Invalid email or password")

	checks := countChecks(t)
	_, err = h.s.Login(ctx, "nobody@example.com", "secret pw")
	wantError(t, err, 401, "Invalid email or password")
	if c := checks(); len(c) != 1 || c[0] != passwords.Dummy {
		t.Errorf("checks with no user = %v, want the dummy hash once", c)
	}

	// a legacy-only row (password_hash NULL) never matches
	testenv.AddUser(t, h.d, "legacy")
	_, err = h.s.Login(ctx, "legacy", "x")
	wantError(t, err, 401, "Invalid email or password")
}

func TestShow(t *testing.T) {
	h := setup(t)
	id := h.addUser("bob", "bob@example.com", "pw", nil)
	u, err := h.s.Show(ctx, id)
	if err != nil || u.ID != id || u.Username != "bob" || u.Email != "bob@example.com" || u.Verified != nil {
		t.Errorf("Show = %+v, %v", u, err)
	}
	_, err = h.s.Show(ctx, "missing")
	wantError(t, err, 401, "unauthorized")
}

func TestLoginRehash(t *testing.T) {
	h := setup(t)
	// testdata/fixtures/legacy_hashes.json's first entry
	conv, err := passwords.FromLegacy("0123456789abcdef", "25748e912f5c4c18b6aec0b348aa867778c7d71f976a91")
	if err != nil {
		t.Fatal(err)
	}
	id := h.addUserHash("conv", "conv@example.com", conv, ptr(true))
	h.exec(`UPDATE "user" SET password='oldlegacy' WHERE id=$1`, id)

	u, err := h.s.Login(ctx, "conv", "correct horse")
	if err != nil || u.ID != id {
		t.Fatalf("Login with the converted hash = %+v, %v", u, err)
	}
	var hash string
	var legacy *string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash, password FROM "user" WHERE id=$1`, id).Scan(&hash, &legacy)
	if c, err := bcrypt.Cost([]byte(hash)); err != nil || c != 12 || legacy != nil {
		t.Errorf("after login: hash %q (cost %d, %v), password %v", hash, c, err, legacy)
	}

	checks := countChecks(t)
	if u, err := h.s.Login(ctx, "conv", "correct horse"); err != nil || u.ID != id {
		t.Errorf("second Login = %+v, %v", u, err)
	}
	var again string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&again)
	if c := checks(); again != hash || len(c) != 1 || c[0] != hash {
		t.Errorf("second login rehashed (%q -> %q) or checked %v", hash, again, c)
	}
}

func TestLoginRehashLostRace(t *testing.T) {
	h := setup(t)
	conv, _ := passwords.FromLegacy("0123456789abcdef", "25748e912f5c4c18b6aec0b348aa867778c7d71f976a91")
	id := h.addUserHash("conv", "conv@example.com", conv, ptr(true))
	// someone else changes the hash between the check and the save
	old := checkPassword
	checkPassword = func(pw, hash string) bool {
		ok := old(pw, hash)
		h.exec(`UPDATE "user" SET password_hash='$2b$12$other' WHERE id=$1`, id)
		return ok
	}
	t.Cleanup(func() { checkPassword = old })
	if u, err := h.s.Login(ctx, "conv", "correct horse"); err != nil || u.ID != id {
		t.Errorf("Login = %+v, %v", u, err)
	}
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&hash)
	if hash != "$2b$12$other" {
		t.Errorf("hash = %q, overwrote the other change", hash)
	}
}

func TestLoginSeveralRows(t *testing.T) {
	h := setup(t)
	h.exec(`ALTER TABLE "user" DROP CONSTRAINT user_username_valid`)
	a := h.addUser("usera", "x@example.com", "pass a", ptr(true))
	b := h.addUser("x@example.com", "b@example.com", "pass b", ptr(true))

	if u, err := h.s.Login(ctx, "X@example.com", "pass b"); err != nil || u.ID != b {
		t.Errorf("B's password = %+v, %v; want %s", u, err, b)
	}
	if u, err := h.s.Login(ctx, "x@example.com", "pass a"); err != nil || u.ID != a {
		t.Errorf("A's password = %+v, %v; want %s", u, err, a)
	}
	checks := countChecks(t)
	_, err := h.s.Login(ctx, "x@example.com", "neither")
	wantError(t, err, 401, "Invalid email or password")
	if n := len(checks()); n != 2 {
		t.Errorf("checked %d hashes, want both rows'", n)
	}
}

func TestRegister(t *testing.T) {
	h := setup(t)
	now := time.Unix(1800000000, 0)
	h.s.Now = func() time.Time { return now }
	u, err := h.s.Register(ctx, "NewUser", "New@Example.COM", "a password")
	if err != nil {
		t.Fatal(err)
	}
	if u.Username != "NewUser" || u.Email != "new@example.com" || u.Verified == nil || *u.Verified || u.ID == "" {
		t.Errorf("Register = %+v", u)
	}
	var email, hash string
	var sent, registered bool
	h.d.Pool.QueryRow(ctx, `SELECT email, password_hash, verify_sent IS NOT NULL, registered IS NOT NULL FROM "user" WHERE id=$1`, u.ID).
		Scan(&email, &hash, &sent, &registered)
	if c, _ := bcrypt.Cost([]byte(hash)); email != "new@example.com" || c != 12 || !sent || !registered || !passwords.Check("a password", hash) {
		t.Errorf("row: email %q hash %q sent %v registered %v", email, hash, sent, registered)
	}
	if n := len(h.connections(u.ID)); n != 0 {
		t.Errorf("register added %d connections", n)
	}

	m := h.mail.all()
	if len(m) != 1 || m[0].To != "new@example.com" || m[0].Subject != "Confirm your relaychat.party email address" {
		t.Fatalf("mail = %+v", m)
	}
	token := MakeToken(secret, "verify", u.ID, "new@example.com", now.Unix()+VerifyLifetime)
	want := "Hello NewUser,\n\nThanks for signing up with relaychat.party! Please confirm your email\naddress by opening this link:\n" +
		siteURL + "/verify/#" + token + "\n\nHappy chatting!"
	if m[0].Body != want {
		t.Errorf("body = %q\nwant %q", m[0].Body, want)
	}

	for _, c := range []struct{ user, email, pass, msg string }{
		{"", "a@example.com", "p", "username is required"},
		{" \t", "a@example.com", "p", "username is required"},
		{"a", "", "p", "email is required"},
		{"a", "a@example.com", "", "pass is required"},
		{"a", "a@example.com", "\n\v", "pass is required"},
		{"a b", "a@example.com", "p", "Usernames can only use letters, digits, _ and ., up to 32 characters"},
		{strings.Repeat("a", 33), "a@example.com", "p", "Usernames can only use letters, digits, _ and ., up to 32 characters"},
		{"a@b", "a@example.com", "p", "Usernames can only use letters, digits, _ and ., up to 32 characters"},
		{"a", "not an email", "p", "Invalid email address"},
		{"a", "a@x.inc", "p", "Invalid email address"},
		// Perl's lc is ASCII-only: the Kelvin sign doesn't become "k"
		{"a", "K@example.com", "p", "Invalid email address"},
	} {
		_, err := h.s.Register(ctx, c.user, c.email, c.pass)
		wantError(t, err, 400, c.msg)
	}

	for _, c := range [][2]string{{"newuser", "other@example.com"}, {"other", "NEW@example.com"}} {
		_, err := h.s.Register(ctx, c[0], c[1], "p")
		e, ok := IsError(err)
		if !ok || e.Code != 400 || !strings.Contains(e.Msg, "already exists") {
			t.Errorf("Register(%q, %q) = %v", c[0], c[1], err)
		}
	}
	if n := len(h.mail.all()); n != 1 {
		t.Errorf("%d emails after failed registers", n)
	}
}

func TestRegisterNoSMTP(t *testing.T) {
	h := setup(t)
	h.s.NoSMTP = true
	if _, err := h.s.Register(ctx, "quiet", "quiet@example.com", "p"); err != nil {
		t.Fatal(err)
	}
	if n := len(h.mail.all()); n != 0 {
		t.Errorf("NoSMTP sent %d emails", n)
	}
}

func TestVerify(t *testing.T) {
	h := setup(t)
	id := h.addUser("New.User1", "new@example.com", "p", ptr(false))
	token := MakeToken(secret, "verify", id, "new@example.com", time.Now().Unix()+VerifyLifetime)

	if err := h.s.Verify(ctx, token); err != nil {
		t.Fatal(err)
	}
	if v := h.verified(id); v == nil || !*v {
		t.Errorf("verified = %v", v)
	}
	conns := h.connections(id)
	if len(conns) != 1 {
		t.Fatalf("connections = %v", conns)
	}
	want := `{"Channels":[],"Highlight":["New_User1"],"Host":"irc.example","Nick":"New_User1","Port":6697,"Ssl":true,"User":"New_User1"}`
	if got, _ := json.Marshal(conns[0]); string(got) != want {
		t.Errorf("config = %s\nwant %s", got, want)
	}
	if c := h.fake.Calls(); len(c) != 1 || !strings.HasSuffix(c[0].Path, "/create") {
		t.Errorf("liercd calls = %v", c)
	}

	// the same token again: ok, no second network
	if err := h.s.Verify(ctx, token); err != nil {
		t.Error(err)
	}
	if n := len(h.connections(id)); n != 1 {
		t.Errorf("re-verify: %d connections", n)
	}

	// a NULL verified becomes true, but isn't a first verification
	old := h.addUser("old", "old@example.com", "p", nil)
	if err := h.s.Verify(ctx, MakeToken(secret, "verify", old, "old@example.com", time.Now().Unix()+60)); err != nil {
		t.Error(err)
	}
	if v := h.verified(old); v == nil || !*v || len(h.connections(old)) != 0 {
		t.Errorf("NULL user: verified %v, connections %v", v, h.connections(old))
	}

	// a user with a connection already gets none
	has := h.addUser("has", "has@example.com", "p", ptr(false))
	testenv.AddConnection(t, h.d, "existing", has, `{"Host":"other.example"}`)
	if err := h.s.Verify(ctx, MakeToken(secret, "verify", has, "has@example.com", time.Now().Unix()+60)); err != nil {
		t.Error(err)
	}
	if n := len(h.connections(has)); n != 1 || !*h.verified(has) {
		t.Errorf("user with a connection: %d connections", n)
	}

	// liercd failing: still ok, and the row stays
	h.fake.Respond("POST", "/*/create", 500, "boom")
	fail := h.addUser("fail", "fail@example.com", "p", ptr(false))
	if err := h.s.Verify(ctx, MakeToken(secret, "verify", fail, "fail@example.com", time.Now().Unix()+60)); err != nil {
		t.Errorf("Verify with liercd failing = %v", err)
	}
	if n := len(h.connections(fail)); n != 1 || !*h.verified(fail) {
		t.Errorf("liercd failing: %d connections", n)
	}

	// no default network configured: nothing added
	h.s.DefaultNetwork = nil
	none := h.addUser("none", "none@example.com", "p", ptr(false))
	if err := h.s.Verify(ctx, MakeToken(secret, "verify", none, "none@example.com", time.Now().Unix()+60)); err != nil {
		t.Error(err)
	}
	if n := len(h.connections(none)); n != 0 || !*h.verified(none) {
		t.Errorf("no default network: %d connections", n)
	}

	// bad tokens
	bad := h.addUser("bad", "bad@example.com", "p", ptr(false))
	exp := time.Now().Unix() + 60
	changed := MakeToken(secret, "verify", bad, "bad@example.com", exp)
	h.exec(`UPDATE "user" SET email='changed@example.com' WHERE id=$1`, bad)
	expired := MakeToken(secret, "verify", bad, "changed@example.com", exp)
	for name, tok := range map[string]string{
		"garbage":       "not a token",
		"empty":         "",
		"no such user":  MakeToken(secret, "verify", "missing", "x@example.com", exp),
		"wrong purpose": MakeToken(secret, "reset", bad, "changed@example.com", exp),
		"wrong secret":  MakeToken("other", "verify", bad, "changed@example.com", exp),
		"email changed": changed,
	} {
		t.Run(name, func(t *testing.T) {
			wantError(t, h.s.Verify(ctx, tok), 400, "This confirmation link has expired or isn't valid.")
		})
	}
	h.s.Now = func() time.Time { return time.Unix(exp+1, 0) }
	wantError(t, h.s.Verify(ctx, expired), 400, "This confirmation link has expired or isn't valid.")
	h.s.Now = func() time.Time { return time.Unix(exp, 0) }
	if err := h.s.Verify(ctx, expired); err != nil {
		t.Errorf("at the expiry second: %v", err)
	}
}

func TestResend(t *testing.T) {
	h := setup(t)
	id := h.addUser("pending", "pending@example.com", "p", ptr(false))

	if err := h.s.Resend(ctx, id); err != nil {
		t.Fatal(err)
	}
	m := h.mail.all()
	if len(m) != 1 || m[0].To != "pending@example.com" || m[0].Subject != "Confirm your relaychat.party email address" ||
		!strings.Contains(m[0].Body, "Hello pending,") || !strings.Contains(m[0].Body, siteURL+"/verify/#") {
		t.Fatalf("mail = %+v", m)
	}
	if err := h.s.Resend(ctx, id); err != nil || len(h.mail.all()) != 1 {
		t.Errorf("second resend within 60s: %v, %d emails", err, len(h.mail.all()))
	}
	h.exec(`UPDATE "user" SET verify_sent = now() - interval '1 hour' WHERE id=$1`, id)
	if err := h.s.Resend(ctx, id); err != nil || len(h.mail.all()) != 2 {
		t.Errorf("after the window: %v, %d emails", err, len(h.mail.all()))
	}

	done := h.addUser("done", "done@example.com", "p", ptr(true))
	if err := h.s.Resend(ctx, done); err != nil || len(h.mail.all()) != 2 {
		t.Errorf("verified user: %v, %d emails", err, len(h.mail.all()))
	}
	if err := h.s.Resend(ctx, "missing"); err != nil || len(h.mail.all()) != 2 {
		t.Errorf("missing user: %v, %d emails", err, len(h.mail.all()))
	}
}

func TestReset(t *testing.T) {
	h := setup(t)
	now := time.Unix(1800000000, 0)
	h.s.Now = func() time.Time { return now }
	id := h.addUser("forgot", "forgot@example.com", "p", ptr(true))

	for _, e := range []string{"", "nope", "a@x.inc"} {
		wantError(t, h.s.Reset(ctx, e), 400, "Invalid email address")
	}
	if err := h.s.Reset(ctx, "unknown@example.com"); err != nil || len(h.mail.all()) != 0 {
		t.Errorf("unknown address: %v, %d emails", err, len(h.mail.all()))
	}
	if err := h.s.Reset(ctx, "Forgot@EXAMPLE.com"); err != nil {
		t.Fatal(err)
	}
	m := h.mail.all()
	if len(m) != 1 || m[0].To != "forgot@example.com" || m[0].Subject != "Reset your relaychat.party password" {
		t.Fatalf("mail = %+v", m)
	}
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&hash)
	token := MakeToken(secret, "reset", id, hash, now.Unix()+ResetLifetime)
	want := "Hello forgot,\n\nSomeone asked to reset the password for your relaychat.party account.\n" +
		"To choose a new password, open this link within the next hour:\n" + siteURL + "/reset/#" + token +
		"\n\nIf you didn't ask for this, you can ignore this email; your password\nhasn't changed."
	if m[0].Body != want {
		t.Errorf("body = %q\nwant %q", m[0].Body, want)
	}

	// a legacy-only user's fingerprint is its password column
	leg := testenv.AddUser(t, h.d, "legacy")
	if err := h.s.Reset(ctx, "legacy@example.com"); err != nil {
		t.Fatal(err)
	}
	tok := MakeToken(secret, "reset", leg, "x", now.Unix()+ResetLifetime)
	if m := h.mail.all(); len(m) != 2 || !strings.Contains(m[1].Body, "/reset/#"+tok+"\n") {
		t.Errorf("legacy reset mail = %+v", m)
	}
}

func TestEscapedTemplate(t *testing.T) {
	// Text::Xslate's default html escaping, as Perl rendered it
	got := render(verifyTemplate, `a&<>"'b`, "http://x/?a=1&b=2#tok")
	want := "Hello a&amp;&lt;&gt;&quot;&#39;b,\n\nThanks for signing up with relaychat.party! Please confirm your email\naddress by opening this link:\nhttp://x/?a=1&amp;b=2#tok\n\nHappy chatting!"
	if got != want {
		t.Errorf("render = %q", got)
	}
}

func TestResetConfirm(t *testing.T) {
	h := setup(t)
	id := h.addUser("forgot", "forgot@example.com", "old pw", ptr(false))
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&hash)
	token := MakeToken(secret, "reset", id, hash, time.Now().Unix()+ResetLifetime)

	wantError(t, nil2(h.s.ResetConfirm(ctx, token, " \t")), 400, "A new password is required")
	wantError(t, nil2(h.s.ResetConfirm(ctx, "garbage", "new pw")), 400, "This reset link has expired or was already used. Request a new one.")
	wantError(t, nil2(h.s.ResetConfirm(ctx, MakeToken(secret, "verify", id, hash, time.Now().Unix()+60), "new pw")), 400,
		"This reset link has expired or was already used. Request a new one.")

	u, err := h.s.ResetConfirm(ctx, token, "new pw")
	if err != nil || u.ID != id || u.Username != "forgot" || u.Verified == nil || !*u.Verified {
		t.Fatalf("ResetConfirm = %+v, %v", u, err)
	}
	if _, err := h.s.Login(ctx, "forgot", "new pw"); err != nil {
		t.Errorf("new password: %v", err)
	}
	if _, err := h.s.Login(ctx, "forgot", "old pw"); err == nil {
		t.Error("old password still works")
	}
	if n := len(h.connections(id)); n != 1 {
		t.Errorf("first verification by reset: %d connections", n)
	}
	wantError(t, nil2(h.s.ResetConfirm(ctx, token, "again")), 400, "This reset link has expired or was already used. Request a new one.")

	// a legacy-only user resets through the password column
	leg := testenv.AddUser(t, h.d, "legacy")
	if _, err := h.s.ResetConfirm(ctx, MakeToken(secret, "reset", leg, "x", time.Now().Unix()+60), "fresh"); err != nil {
		t.Errorf("legacy reset: %v", err)
	}
	var legacy *string
	h.d.Pool.QueryRow(ctx, `SELECT password FROM "user" WHERE id=$1`, leg).Scan(&legacy)
	if _, err := h.s.Login(ctx, "legacy", "fresh"); err != nil || legacy != nil {
		t.Errorf("legacy after reset: %v, password %v", err, legacy)
	}
	if n := len(h.connections(leg)); n != 0 {
		t.Errorf("already-verified legacy user got %d connections", n)
	}
}

func nil2(_ User, err error) error { return err }

func TestResetConfirmConcurrent(t *testing.T) {
	h := setup(t)
	id := h.addUser("race", "race@example.com", "old", ptr(false))
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&hash)
	token := MakeToken(secret, "reset", id, hash, time.Now().Unix()+ResetLifetime)

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = h.s.ResetConfirm(ctx, token, fmt.Sprint("pw", i))
		}()
	}
	wg.Wait()
	ok, expired := 0, 0
	for _, err := range errs {
		if err == nil {
			ok++
		} else if e, is := IsError(err); is && e.Code == 400 && e.Msg == "This reset link has expired or was already used. Request a new one." {
			expired++
		} else {
			t.Errorf("unexpected error %v", err)
		}
	}
	if ok != 1 || expired != 1 {
		t.Errorf("%d ok, %d expired: %v", ok, expired, errs)
	}
	if n := len(h.connections(id)); n != 1 {
		t.Errorf("%d connections", n)
	}
}

type blocking struct {
	release chan struct{}
	started atomic.Int32
	done    atomic.Int32
}

func (b *blocking) Send(to, subject, body string) error {
	b.started.Add(1)
	defer b.done.Add(1)
	<-b.release
	return fmt.Errorf("refused")
}

func TestMailDoesNotBlock(t *testing.T) {
	h := setup(t)
	b := &blocking{release: make(chan struct{})}
	h.s.Mail = b
	h.s.After = nil // the default: go f()
	// let any blocked send finish (briefly) before the database goes
	t.Cleanup(func() {
		close(b.release)
		for end := time.Now().Add(5 * time.Second); b.done.Load() != b.started.Load() && time.Now().Before(end); {
			time.Sleep(10 * time.Millisecond)
		}
	})

	check := func(name string, f func() error) {
		t.Helper()
		res := make(chan error, 1)
		go func() { res <- f() }()
		select {
		case err := <-res:
			if err != nil {
				t.Errorf("%s: %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s waited for the mail server", name)
		}
	}
	var id string
	check("Register", func() error {
		u, err := h.s.Register(ctx, "slow", "slow@example.com", "p")
		id = u.ID
		return err
	})
	h.exec(`UPDATE "user" SET verify_sent = NULL WHERE id=$1`, id)
	check("Resend", func() error { return h.s.Resend(ctx, id) })
	check("Reset", func() error { return h.s.Reset(ctx, "slow@example.com") })

	deadline := time.Now().Add(5 * time.Second)
	for b.started.Load() != 3 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := b.started.Load(); n != 3 {
		t.Errorf("%d sends started, want 3", n)
	}
}

// cancellingLiercd answers liercd's create after cancelling the caller's
// ctx, as a browser leaving mid-request does.
func cancellingLiercd(t *testing.T, cancel func()) (*liercd.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/create") {
			cancel()
			time.Sleep(100 * time.Millisecond)
		}
		io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)
	return &liercd.Client{BaseURL: srv.URL, HTTP: &http.Client{}}, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), calls...)
	}
}

// Once verified=true is committed, a disconnect mustn't cost the user the
// signup network: a re-verify isn't a first verification.
func TestFirstNetworkSurvivesCancel(t *testing.T) {
	h := setup(t)
	id := h.addUser("leaver", "leaver@example.com", "p", ptr(false))
	c, cancel := context.WithCancel(context.Background())
	var calls func() []string
	h.s.Conns.Liercd, calls = cancellingLiercd(t, cancel)
	if err := h.s.Verify(c, MakeToken(secret, "verify", id, "leaver@example.com", time.Now().Unix()+60)); err != nil {
		t.Errorf("Verify = %v", err)
	}
	if n := len(h.connections(id)); n != 1 || len(calls()) != 1 {
		t.Errorf("verify: %d connections, liercd calls %v", n, calls())
	}

	id = h.addUser("leaver2", "leaver2@example.com", "old", ptr(false))
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM "user" WHERE id=$1`, id).Scan(&hash)
	c, cancel = context.WithCancel(context.Background())
	h.s.Conns.Liercd, calls = cancellingLiercd(t, cancel)
	if u, err := h.s.ResetConfirm(c, MakeToken(secret, "reset", id, hash, time.Now().Unix()+60), "new"); err != nil || u.ID != id {
		t.Errorf("ResetConfirm = %+v, %v", u, err)
	}
	if n := len(h.connections(id)); n != 1 || len(calls()) != 1 {
		t.Errorf("reset confirm: %d connections, liercd calls %v", n, calls())
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func captureLog(t *testing.T) *syncBuffer {
	buf := &syncBuffer{}
	log.SetOutput(buf)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	return buf
}

func waitFor(t *testing.T, buf *syncBuffer, subs ...string) {
	t.Helper()
	for end := time.Now().Add(5 * time.Second); time.Now().Before(end); time.Sleep(10 * time.Millisecond) {
		all := true
		for _, s := range subs {
			all = all && strings.Contains(buf.String(), s)
		}
		if all {
			return
		}
	}
	t.Errorf("log = %q, want %q", buf.String(), subs)
}

type panicking struct{}

func (panicking) Send(to, subject, body string) error { panic("mailer blew up") }

// Mail runs in a bare goroutine after the response: a panic there would
// take the whole API down.
func TestMailNeverPanics(t *testing.T) {
	h := setup(t)
	h.s.After = nil // the default: go f()
	buf := captureLog(t)

	h.s.Mail = nil
	if _, err := h.s.Register(ctx, "nomail", "nomail@example.com", "p"); err != nil {
		t.Fatal(err)
	}
	if err := h.s.Reset(ctx, "nomail@example.com"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, buf, "Unable to send verification email for nomail", "Unable to send reset email for nomail")

	h.s.Mail = panicking{}
	if err := h.s.Reset(ctx, "nomail@example.com"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, buf, "mailer blew up")
}
