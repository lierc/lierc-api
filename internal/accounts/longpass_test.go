package accounts

import (
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lierc/lierc-api/internal/passwords"
)

func TestRegisterLongPassword(t *testing.T) {
	h := setup(t)
	wantError(t, nil2(h.s.Register(ctx, "long", "long@example.com", strings.Repeat("a", 73))), 400, msgPassLong)
	var n int
	h.d.Pool.QueryRow(ctx, `SELECT count(*) FROM users WHERE username='long'`).Scan(&n)
	if n != 0 {
		t.Errorf("refused registration left %d rows", n)
	}
	if _, err := h.s.Register(ctx, "edge", "edge@example.com", strings.Repeat("a", 72)); err != nil {
		t.Errorf("72-byte password: %v", err)
	}
}

func TestResetConfirmLongPassword(t *testing.T) {
	h := setup(t)
	id := h.addUser("forgot", "forgot@example.com", "old pw", ptr(true))
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash)
	token := MakeToken(secret, "reset", id, hash, time.Now().Unix()+ResetLifetime)

	wantError(t, nil2(h.s.ResetConfirm(ctx, token, strings.Repeat("é", 37))), 400, msgPassLong)
	var after string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&after)
	if after != hash {
		t.Error("refused reset changed the password")
	}
}

// Passwords set before the limit, longer than 72 bytes, still log in
// (bcrypt reads only the first 72 bytes).
func TestLoginLongPasswordStillWorks(t *testing.T) {
	h := setup(t)
	long := strings.Repeat("p", 80)
	h.addUser("old", "old@example.com", long, ptr(true))
	if _, err := h.s.Login(ctx, "old", long); err != nil {
		t.Errorf("80-byte password: %v", err)
	}
}

// An old hash of a password over 72 bytes, at another cost, logs in and is
// rehashed at passwords.Cost(), and the password still works after. bcrypt read only
// the first 72 bytes when the old hash was made; Go's bcrypt refuses longer
// passwords, so the rehash has to cut it the same way.
func TestLoginRehashLongPassword(t *testing.T) {
	h := setup(t)
	long := strings.Repeat("p", 72) + "-tail"
	id := h.addUserHash("oldlong", "oldlong@example.com", otherCostHash(t, long[:72]), ptr(true))

	if u, err := h.s.Login(ctx, "oldlong", long); err != nil || u.ID != id {
		t.Fatalf("Login = %+v, %v", u, err)
	}
	var hash string
	h.d.Pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id=$1`, id).Scan(&hash)
	if c, err := bcrypt.Cost([]byte(hash)); err != nil || c != passwords.Cost() {
		t.Errorf("after login: cost %d, %v", c, err)
	}
	if u, err := h.s.Login(ctx, "oldlong", long); err != nil || u.ID != id {
		t.Errorf("Login after the rehash = %+v, %v", u, err)
	}
}
