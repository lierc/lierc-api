package auth

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/testenv"
)

func TestMain(m *testing.M) { testenv.Main(m) }

const secret = "testsecret"

// serve runs Require around a handler that writes the user, with a
// "chats" cookie for user made with cookieSecret ("" user: no cookie).
func serve(t *testing.T, d *db.DB, user, cookieSecret string) (int, string) {
	t.Helper()
	h := Users{Secret: secret, DB: d}.Require(func(w http.ResponseWriter, r *http.Request, user string) {
		io.WriteString(w, "user="+user)
	})
	req := httptest.NewRequest("GET", "/", nil)
	if user != "" {
		req.AddCookie(&http.Cookie{Name: "chats", Value: testenv.PerlCookie(user, cookieSecret)})
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 && w.Header().Get("Content-Type") != "application/javascript;charset=utf-8" {
		t.Errorf("error content type %q", w.Header().Get("Content-Type"))
	}
	return w.Code, w.Body.String()
}

func TestRequireUnauthorized(t *testing.T) {
	d := testenv.Postgres(t)
	for _, user := range []string{"", "ghost"} {
		code, body := serve(t, d, user, secret)
		if code != 401 || body != `{"status":"error","error":"unauthorized"}` {
			t.Errorf("user %q: %d %s", user, code, body)
		}
	}
}

func TestRequireBadCookie(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "alice")
	if code, body := serve(t, d, u, "othersecret"); code != 401 {
		t.Errorf("other secret: %d %s", code, body)
	}
}

func TestRequireUnverified(t *testing.T) {
	d := testenv.Postgres(t)
	u := testenv.AddUser(t, d, "newbie")
	set := func(v any) {
		if _, err := d.Pool.Exec(context.Background(), `UPDATE "user" SET verified=$2 WHERE id=$1`, u, v); err != nil {
			t.Fatal(err)
		}
	}
	set(false)
	if code, body := serve(t, d, u, secret); code != 403 || body != `{"status":"error","error":"Confirm your email address first","unverified":true}` {
		t.Errorf("unverified: %d %s", code, body)
	}
	for _, v := range []any{true, nil} {
		set(v)
		if code, body := serve(t, d, u, secret); code != 200 || body != "user="+u {
			t.Errorf("verified %v: %d %s", v, code, body)
		}
	}
}
