// Package login is the login table: one row per logged-in browser. The
// "chats" cookie holds a random token; only its SHA-256 is stored.
package login

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
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

// live is the condition for a login that has not expired.
const live = `last_used > now() - interval '7 days'`

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
func Create(ctx context.Context, d *db.DB, user, userAgent, ip string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(b)
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO login (token_hash, "user", user_agent, ip) VALUES ($1, $2, $3, NULLIF($4::text, '')::inet)`,
		hash(token), user, truncate(userAgent, userAgentMax), ip)
	if err != nil {
		return "", err
	}
	return token, nil
}

// Login is a live login and its user's standing.
type Login struct {
	ID         int64
	User       string
	Unverified bool
	LastUsed   time.Time
}

// Lookup finds the live login for token.
func Lookup(ctx context.Context, d *db.DB, token string) (Login, bool, error) {
	var l Login
	err := d.Pool.QueryRow(ctx,
		`SELECT l.id, l."user", u.verified IS NOT DISTINCT FROM false, l.last_used
		 FROM login l JOIN "user" u ON u.id = l."user"
		 WHERE l.token_hash = $1 AND l.`+live, hash(token)).
		Scan(&l.ID, &l.User, &l.Unverified, &l.LastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Login{}, false, nil
	}
	if err != nil {
		return Login{}, false, err
	}
	return l, true, nil
}

// Touch marks login id as used now.
func Touch(ctx context.Context, d *db.DB, id int64) error {
	_, err := d.Pool.Exec(ctx, `UPDATE login SET last_used = now() WHERE id = $1`, id)
	return err
}

// Delete deletes login id if it belongs to user, and reports whether it did.
func Delete(ctx context.Context, q db.Execer, user string, id int64) (bool, error) {
	tag, err := q.Exec(ctx, `DELETE FROM login WHERE id = $1 AND "user" = $2`, id, user)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteToken deletes the login for token; it is no error if there is none.
func DeleteToken(ctx context.Context, d *db.DB, token string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM login WHERE token_hash = $1`, hash(token))
	return err
}

// DeleteAll deletes every login of user and returns how many there were.
func DeleteAll(ctx context.Context, q db.Execer, user string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM login WHERE "user" = $1`, user)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// Alive reports whether login id exists and has not expired.
func Alive(ctx context.Context, d *db.DB, id int64) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login WHERE id = $1 AND `+live+`)`, id).Scan(&ok)
	return ok, err
}

// Info describes a login for the user's list of sessions.
type Info struct {
	ID        int64
	Created   time.Time
	LastUsed  time.Time
	UserAgent string
	IP        string
}

// List returns user's live logins, most recently used first.
func List(ctx context.Context, d *db.DB, user string) ([]Info, error) {
	rows, err := d.Pool.Query(ctx,
		`SELECT id, created, last_used, user_agent, COALESCE(host(ip), '')
		 FROM login WHERE "user" = $1 AND `+live+` ORDER BY last_used DESC, id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Info
	for rows.Next() {
		var i Info
		if err := rows.Scan(&i.ID, &i.Created, &i.LastUsed, &i.UserAgent, &i.IP); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// Prune deletes expired logins and returns how many there were.
func Prune(ctx context.Context, d *db.DB) (int64, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM login WHERE last_used <= now() - interval '7 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
