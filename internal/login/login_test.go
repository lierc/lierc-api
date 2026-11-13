package login_test

import (
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

var ctx = context.Background()

func exec(t *testing.T, d *db.DB, sql string, args ...any) {
	t.Helper()
	if _, err := d.Pool.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, d *db.DB, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := d.Pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTokenShape(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	tok, err := login.Create(ctx, d, u, "ua", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(tok) != 43 || !login.IsToken(tok) {
		t.Errorf("token %q: len %d, IsToken %v", tok, len(tok), login.IsToken(tok))
	}
	a := strings.Repeat("a", 43)
	for _, bad := range []string{"", a[:42], a + "a", a[:42] + "+", a[:42] + "/", a[:42] + "=", "1792769372.78598%3ABQsDAAAAAQoBdQAAAAR1c2Vy%3A0123456789abcdef0123456789abcdef01234567" /* a Perl-format cookie */} {
		if login.IsToken(bad) {
			t.Errorf("IsToken(%q) = true", bad)
		}
	}
}

func TestCreateLookup(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	tok, err := login.Create(ctx, d, u, "ua", "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	l, ok, err := login.Lookup(ctx, d, tok)
	if err != nil || !ok {
		t.Fatalf("Lookup: %v %v", ok, err)
	}
	if l.User != u || l.Unverified || l.ID <= 0 {
		t.Errorf("login = %+v", l)
	}
	sum := sha256.Sum256([]byte(tok))
	if n := count(t, d, `SELECT count(*) FROM login WHERE token_hash = $1`, sum[:]); n != 1 {
		t.Errorf("token_hash rows = %d", n)
	}
	if n := count(t, d, `SELECT count(*) FROM login WHERE login::text LIKE '%' || $1 || '%'`, tok); n != 0 {
		t.Error("the token is stored in a column")
	}
	if _, ok, _ := login.Lookup(ctx, d, strings.Repeat("a", 43)); ok {
		t.Error("unknown token found")
	}
	exec(t, d, `UPDATE "user" SET verified = false WHERE id = $1`, u)
	if l, _, _ := login.Lookup(ctx, d, tok); !l.Unverified {
		t.Error("unverified user not reported")
	}
	exec(t, d, `UPDATE "user" SET verified = NULL WHERE id = $1`, u)
	if l, _, _ := login.Lookup(ctx, d, tok); l.Unverified {
		t.Error("NULL verified (legacy user) reported as unverified")
	}
}

func TestLookupExpired(t *testing.T) {
	d := testenv.Postgres(t)
	tok := testenv.Login(t, d, testenv.AddUser(t, d, "alice"))
	l, ok, err := login.Lookup(ctx, d, tok)
	if err != nil || !ok || l.ID <= 0 {
		t.Fatalf("fresh Lookup: %+v %v %v", l, ok, err)
	}
	exec(t, d, `UPDATE login SET last_used = now() - interval '7 days 1 second'`)
	if _, ok, err := login.Lookup(ctx, d, tok); err != nil || ok {
		t.Fatalf("expired Lookup: %v %v", ok, err)
	}
	if alive, err := login.Alive(ctx, d, l.ID); err != nil || alive {
		t.Errorf("expired Alive = %v %v", alive, err)
	}
	exec(t, d, `UPDATE login SET last_used = now() - interval '6 days 23 hours'`)
	if _, ok, err = login.Lookup(ctx, d, tok); err != nil || !ok {
		t.Fatalf("live Lookup: %v %v", ok, err)
	}
	if alive, err := login.Alive(ctx, d, l.ID); err != nil || !alive {
		t.Errorf("live Alive = %v %v", alive, err)
	}
	if alive, _ := login.Alive(ctx, d, l.ID+1000); alive {
		t.Error("missing login alive")
	}
}

func TestTouch(t *testing.T) {
	d := testenv.Postgres(t)
	tok := testenv.Login(t, d, testenv.AddUser(t, d, "alice"))
	exec(t, d, `UPDATE login SET last_used = now() - interval '2 hours'`)
	l, _, _ := login.Lookup(ctx, d, tok)
	if err := login.Touch(ctx, d, l.ID); err != nil {
		t.Fatal(err)
	}
	if n := count(t, d, `SELECT count(*) FROM login WHERE last_used > now() - interval '1 minute'`); n != 1 {
		t.Error("Touch did not refresh last_used")
	}
}

func TestDelete(t *testing.T) {
	d := testenv.Postgres(t)
	a, b := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	ta, tb := testenv.Login(t, d, a), testenv.Login(t, d, b)
	la, _, _ := login.Lookup(ctx, d, ta)
	lb, _, _ := login.Lookup(ctx, d, tb)
	if ok, err := login.Delete(ctx, d, a, lb.ID); err != nil || ok {
		t.Errorf("deleting another user's login = %v %v", ok, err)
	}
	if _, ok, _ := login.Lookup(ctx, d, tb); !ok {
		t.Error("another user's login was deleted")
	}
	if ok, err := login.Delete(ctx, d, a, la.ID); err != nil || !ok {
		t.Errorf("deleting own login = %v %v", ok, err)
	}
	if _, ok, _ := login.Lookup(ctx, d, ta); ok {
		t.Error("login still found")
	}
}

func TestDeleteToken(t *testing.T) {
	d := testenv.Postgres(t)
	tok := testenv.Login(t, d, testenv.AddUser(t, d, "alice"))
	if err := login.DeleteToken(ctx, d, tok); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := login.Lookup(ctx, d, tok); ok {
		t.Error("login still found")
	}
	if err := login.DeleteToken(ctx, d, tok); err != nil {
		t.Errorf("deleting an absent token: %v", err)
	}
}

func TestDeleteAll(t *testing.T) {
	d := testenv.Postgres(t)
	u1, u2 := testenv.AddUser(t, d, "alice"), testenv.AddUser(t, d, "bob")
	testenv.Login(t, d, u1)
	testenv.Login(t, d, u1)
	t2a, t2b := testenv.Login(t, d, u2), testenv.Login(t, d, u2)
	n, err := login.DeleteAll(ctx, d, u1)
	if err != nil || n != 2 {
		t.Fatalf("DeleteAll = %d %v", n, err)
	}
	for _, tok := range []string{t2a, t2b} {
		if _, ok, _ := login.Lookup(ctx, d, tok); !ok {
			t.Error("other user's login deleted")
		}
	}
}

func TestUserAgentTruncated(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	long := strings.Repeat("x", 300)
	if _, err := login.Create(ctx, d, u, long, ""); err != nil {
		t.Fatal(err)
	}
	// a multi-byte rune straddling byte 256 is dropped whole
	multi := strings.Repeat("x", 255) + "é" + "tail"
	if _, err := login.Create(ctx, d, u, multi, "192.0.2.7"); err != nil {
		t.Fatal(err)
	}
	var got string
	var nullIP bool
	if err := d.Pool.QueryRow(ctx, `SELECT user_agent, ip IS NULL FROM login ORDER BY id LIMIT 1`).Scan(&got, &nullIP); err != nil {
		t.Fatal(err)
	}
	if got != long[:256] || !nullIP {
		t.Errorf("user_agent len %d, ip null %v", len(got), nullIP)
	}
	infos, err := login.List(ctx, d, u)
	if err != nil || len(infos) != 2 {
		t.Fatalf("List = %v %v", infos, err)
	}
	for _, i := range infos {
		switch len(i.UserAgent) {
		case 256:
			if i.IP != "" {
				t.Errorf("IP = %q, want empty", i.IP)
			}
		case 255:
			if i.UserAgent != multi[:255] || i.IP != "192.0.2.7" {
				t.Errorf("rune not dropped whole: %q ip %q", i.UserAgent, i.IP)
			}
		default:
			t.Errorf("user agent length %d", len(i.UserAgent))
		}
	}
}

func TestUserDeleteCascades(t *testing.T) {
	d := testenv.Postgres(t)
	testenv.Login(t, d, testenv.AddUser(t, d, "alice"))
	exec(t, d, `DELETE FROM "user"`)
	if n := count(t, d, `SELECT count(*) FROM login`); n != 0 {
		t.Errorf("%d logins left", n)
	}
}

func TestPrune(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	old, live := testenv.Login(t, d, u), testenv.Login(t, d, u)
	exec(t, d, `UPDATE login SET last_used = now() - interval '8 days'
		WHERE token_hash = $1`, hash(old))
	n, err := login.Prune(ctx, d)
	if err != nil || n != 1 {
		t.Fatalf("Prune = %d %v", n, err)
	}
	if _, ok, _ := login.Lookup(ctx, d, live); !ok {
		t.Error("live login pruned")
	}
	if c := count(t, d, `SELECT count(*) FROM login`); c != 1 {
		t.Errorf("%d rows left", c)
	}
}

func hash(tok string) []byte { s := sha256.Sum256([]byte(tok)); return s[:] }

func TestListOrder(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	var toks []string
	for i := 0; i < 3; i++ {
		toks = append(toks, testenv.Login(t, d, u))
	}
	// toks[1] newest, then toks[2], then toks[0]; toks[0] kept live
	for i, ago := range map[int]string{0: "3 days", 1: "1 hour", 2: "1 day"} {
		exec(t, d, `UPDATE login SET last_used = now() - $2::interval WHERE token_hash = $1`, hash(toks[i]), ago)
	}
	infos, err := login.List(ctx, d, u)
	if err != nil || len(infos) != 3 {
		t.Fatalf("List = %v %v", infos, err)
	}
	var want []int64
	for _, i := range []int{1, 2, 0} {
		l, _, _ := login.Lookup(ctx, d, toks[i])
		want = append(want, l.ID)
	}
	for i, info := range infos {
		if info.ID != want[i] {
			t.Errorf("position %d: id %d, want %d", i, info.ID, want[i])
		}
	}
	exec(t, d, `UPDATE login SET last_used = now() - interval '8 days' WHERE token_hash = $1`, hash(toks[0]))
	if infos, _ := login.List(ctx, d, u); len(infos) != 2 {
		t.Errorf("List includes an expired login: %d", len(infos))
	}
}

func TestSetCookie(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	tok := strings.Repeat("a", 43)
	exp := now.Add(login.Lifetime).UTC().Format(http.TimeFormat)
	for _, secure := range []bool{false, true} {
		w := httptest.NewRecorder()
		login.SetCookie(w, tok, secure, now)
		want := "chats=" + tok + "; Path=/; Expires=" + exp + "; HttpOnly; SameSite=Strict"
		if secure {
			want += "; Secure"
		}
		if got := w.Header().Values("Set-Cookie"); len(got) != 1 || got[0] != want {
			t.Errorf("secure=%v: %q, want %q", secure, got, want)
		}
	}
}

func TestExpireCookie(t *testing.T) {
	w := httptest.NewRecorder()
	login.ExpireCookie(w, true)
	want := "chats=; Path=/; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Max-Age=0; HttpOnly; SameSite=Strict; Secure"
	if got := w.Header().Get("Set-Cookie"); got != want {
		t.Errorf("%q, want %q", got, want)
	}
}
