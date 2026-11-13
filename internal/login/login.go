// Package login is the login table: one row per logged-in browser. The
// "chats" cookie holds a random token; only its SHA-256 is stored.
package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"time"
	"unicode/utf8"

	"github.com/lierc/lierc-api/internal/db"
)

const (
	CookieName = "chats"
	// Lifetime is how long a login lasts without use.
	Lifetime = 7 * 24 * time.Hour
	// RenewAfter is how stale last_used gets before a request refreshes it.
	RenewAfter = time.Hour

	userAgentMax = 256
	tokenLen     = 43 // 32 bytes, base64url without padding
)

// IsToken reports whether v is shaped like a token: exactly 43 base64url
// characters.
func IsToken(v string) bool {
	if len(v) != tokenLen {
		return false
	}
	for i := 0; i < len(v); i++ {
		c := v[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

func hash(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// truncate cuts s to at most max bytes without splitting a UTF-8 rune.
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	for max > 0 && !utf8.RuneStart(s[max]) {
		max--
	}
	return s[:max]
}

// Create logs user in and returns the new token. An empty ip is stored as NULL.
func Create(ctx context.Context, s db.Store, user, userAgent, ip string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	if err := s.InsertLogin(ctx, hash(token), user, truncate(userAgent, userAgentMax), ip); err != nil {
		return "", err
	}
	return token, nil
}

// Login is a live login and its user's standing.
type Login = db.Login

// Lookup finds the live login for token.
func Lookup(ctx context.Context, s db.Store, token string) (Login, bool, error) {
	return s.LoginByHash(ctx, hash(token))
}

// Touch marks login id as used now.
func Touch(ctx context.Context, s db.Store, id int64) error {
	return s.TouchLogin(ctx, id)
}

// Delete deletes login id if it belongs to user, and reports whether it did.
func Delete(ctx context.Context, s db.Store, user string, id int64) (bool, error) {
	return s.DeleteLogin(ctx, user, id)
}

// DeleteToken deletes the login for token; it is no error if there is none.
func DeleteToken(ctx context.Context, s db.Store, token string) error {
	return s.DeleteLoginByHash(ctx, hash(token))
}

// DeleteAll deletes every login of user and returns how many there were.
func DeleteAll(ctx context.Context, s db.Store, user string) (int64, error) {
	return s.DeleteLogins(ctx, user)
}

// Alive reports whether login id exists and has not expired.
func Alive(ctx context.Context, s db.Store, id int64) (bool, error) {
	return s.LoginAlive(ctx, id)
}

// Info describes a login for the user's list of sessions.
type Info = db.LoginInfo

// List returns user's live logins, most recently used first.
func List(ctx context.Context, s db.Store, user string) ([]Info, error) {
	return s.Logins(ctx, user)
}

// Prune deletes expired logins and returns how many there were.
func Prune(ctx context.Context, s db.Store) (int64, error) {
	return s.PruneLogins(ctx)
}
