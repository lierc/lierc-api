package lctl

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/lierc/lierc-api/internal/testenv"
)

func (h *harness) hash(user string) (hash, legacy *string) {
	h.t.Helper()
	if err := h.db.Pool.QueryRow(h.t.Context(), `SELECT password_hash, password FROM "user" WHERE id = $1`, user).Scan(&hash, &legacy); err != nil {
		h.t.Fatal(err)
	}
	return
}

func TestResetFromStdin(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("s3cret pw\r\nignored\n")
	if err := h.run("reset", "ANN@example.com"); err != nil {
		t.Fatal(err)
	}
	h.has("password set for ANN@example.com\n")
	hash, legacy := h.hash(u)
	if hash == nil || !regexp.MustCompile(`^\$2[ab]\$12\$`).MatchString(*hash) {
		t.Fatalf("hash %v isn't a cost-12 bcrypt the API accepts", hash)
	}
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte("s3cret pw")) != nil {
		t.Error("hash doesn't match the password")
	}
	if legacy != nil {
		t.Error("legacy password column not cleared")
	}
}

func TestResetPrompt(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.env.Stdin = strings.NewReader("from stdin\n")
	h.env.ReadPassword = func() (string, error) { return "typed", nil }
	if err := h.run("reset", "ann@example.com"); err != nil {
		t.Fatal(err)
	}
	hash, _ := h.hash(u)
	if bcrypt.CompareHashAndPassword([]byte(*hash), []byte("typed")) != nil {
		t.Error("didn't use the typed password")
	}
}

func TestResetUnknownEmail(t *testing.T) {
	h := newHarness(t)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("pw\n")
	if err := h.run("reset", "who@example.com"); err == nil || err.Error() != "no user with email who@example.com" {
		t.Fatalf("got %v", err)
	}
}

func TestResetEmptyPassword(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("\n")
	if err := h.run("reset", "ann@example.com"); err == nil || err.Error() != "password is empty" {
		t.Fatalf("got %v", err)
	}
	if hash, _ := h.hash(u); hash != nil {
		t.Error("password changed")
	}
}

func TestResetUsage(t *testing.T) {
	h := newHarness(t)
	var ue *UsageError
	for _, args := range [][]string{{}, {"a@b", "pw"}} {
		if err := h.run("reset", args...); !errors.As(err, &ue) {
			t.Errorf("%v: got %v", args, err)
		}
	}
}

func TestResetEndsLogins(t *testing.T) {
	h := newHarness(t)
	u := h.user("ann")
	other := h.user("bob")
	testenv.Login(t, h.db, u)
	testenv.Login(t, h.db, u)
	testenv.Login(t, h.db, other)
	h.env.StdinIsTerminal = false
	h.env.Stdin = strings.NewReader("s3cret pw\n")
	if err := h.run("reset", "ann@example.com"); err != nil {
		t.Fatal(err)
	}
	h.has("password set for ann@example.com\n", "ended 2 logins\n")
	if n := h.count(`SELECT count(*) FROM login WHERE "user" = $1`, u); n != 0 {
		t.Errorf("%d logins left", n)
	}
	if n := h.count(`SELECT count(*) FROM login WHERE "user" = $1`, other); n != 1 {
		t.Errorf("other user has %d logins, want 1", n)
	}
}
