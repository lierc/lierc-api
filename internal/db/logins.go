package db

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// The login table: one row per logged-in browser, keyed by the SHA-256 of
// the cookie's token.

// live is the condition for a login that has not expired.
const live = `last_used > now() - interval '7 days'`

// Login is a live login and its user's standing.
type Login struct {
	ID         int64
	User       string
	Unverified bool
	LastUsed   time.Time
}

// LoginInfo describes a login for the user's list of sessions.
type LoginInfo struct {
	ID        int64
	Created   time.Time
	LastUsed  time.Time
	UserAgent string
	IP        string
}

// InsertLogin logs user in under tokenHash. An empty ip is stored as NULL.
func (d *DB) InsertLogin(ctx context.Context, tokenHash []byte, user, userAgent, ip string) error {
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO login (token_hash, "user", user_agent, ip) VALUES ($1, $2, $3, NULLIF($4::text, '')::inet)`,
		tokenHash, user, userAgent, ip)
	return err
}

// LoginByHash finds the live login for tokenHash.
func (d *DB) LoginByHash(ctx context.Context, tokenHash []byte) (Login, bool, error) {
	var l Login
	err := d.Pool.QueryRow(ctx,
		`SELECT l.id, l."user", u.verified IS NOT DISTINCT FROM false, l.last_used
		 FROM login l JOIN "user" u ON u.id = l."user"
		 WHERE l.token_hash = $1 AND l.`+live, tokenHash).
		Scan(&l.ID, &l.User, &l.Unverified, &l.LastUsed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Login{}, false, nil
	}
	if err != nil {
		return Login{}, false, err
	}
	return l, true, nil
}

// TouchLogin marks login id as used now.
func (d *DB) TouchLogin(ctx context.Context, id int64) error {
	_, err := d.Pool.Exec(ctx, `UPDATE login SET last_used = now() WHERE id = $1`, id)
	return err
}

// DeleteLogin deletes login id if it belongs to user, and reports whether it did.
func (d *DB) DeleteLogin(ctx context.Context, user string, id int64) (bool, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM login WHERE id = $1 AND "user" = $2`, id, user)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

// DeleteLoginByHash deletes the login for tokenHash; it is no error if
// there is none.
func (d *DB) DeleteLoginByHash(ctx context.Context, tokenHash []byte) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM login WHERE token_hash = $1`, tokenHash)
	return err
}

// DeleteLogins deletes every login of user and returns how many there were.
func (d *DB) DeleteLogins(ctx context.Context, user string) (int64, error) {
	return deleteLogins(ctx, d.Pool, user)
}

// deleteLogins ends every login of user, in q (the pool or a transaction).
func deleteLogins(ctx context.Context, q Execer, user string) (int64, error) {
	tag, err := q.Exec(ctx, `DELETE FROM login WHERE "user" = $1`, user)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

// LoginAlive reports whether login id exists and has not expired.
func (d *DB) LoginAlive(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM login WHERE id = $1 AND `+live+`)`, id).Scan(&ok)
	return ok, err
}

// Logins returns user's live logins, most recently used first.
func (d *DB) Logins(ctx context.Context, user string) ([]LoginInfo, error) {
	rows, err := d.Pool.Query(ctx,
		`SELECT id, created, last_used, user_agent, COALESCE(host(ip), '')
		 FROM login WHERE "user" = $1 AND `+live+` ORDER BY last_used DESC, id DESC`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LoginInfo
	for rows.Next() {
		var i LoginInfo
		if err := rows.Scan(&i.ID, &i.Created, &i.LastUsed, &i.UserAgent, &i.IP); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// PruneLogins deletes expired logins and returns how many there were.
func (d *DB) PruneLogins(ctx context.Context) (int64, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM login WHERE last_used <= now() - interval '7 days'`)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}
