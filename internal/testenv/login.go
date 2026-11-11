package testenv

import (
	"context"
	"testing"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
)

// Login creates a login for user and returns its cookie token.
func Login(t testing.TB, d *db.DB, user string) string {
	t.Helper()
	token, err := login.Create(context.Background(), d, user, "testenv", "")
	if err != nil {
		t.Fatal(err)
	}
	return token
}
