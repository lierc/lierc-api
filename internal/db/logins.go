package db

import (
	"context"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
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
	_, err := exec(ctx, d.Pool, psql.Insert("logins").Columns("token_hash", "user_id", "user_agent", "ip").
		Values(tokenHash, user, userAgent, sq.Expr("NULLIF(?::text, '')::inet", ip)))
	return err
}

// LoginByHash finds the live login for tokenHash.
func (d *DB) LoginByHash(ctx context.Context, tokenHash []byte) (Login, bool, error) {
	var l Login
	err := queryRow(ctx, d.Pool, psql.Select("l.id", "l.user_id", "u.verified IS NOT DISTINCT FROM false", "l.last_used").
		From("logins l").Join(`users u ON u.id = l.user_id`).
		Where(sq.Eq{"l.token_hash": tokenHash}).Where("l."+live)).
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
	_, err := exec(ctx, d.Pool, psql.Update("logins").Set("last_used", sq.Expr("now()")).Where(sq.Eq{"id": id}))
	return err
}

// DeleteLogin deletes login id if it belongs to user, and reports whether it did.
func (d *DB) DeleteLogin(ctx context.Context, user string, id int64) (bool, error) {
	n, err := affected(ctx, d.Pool, psql.Delete("logins").Where(sq.Eq{"id": id, "user_id": user}))
	return n > 0, err
}

// DeleteLoginByHash deletes the login for tokenHash; it is no error if
// there is none.
func (d *DB) DeleteLoginByHash(ctx context.Context, tokenHash []byte) error {
	_, err := exec(ctx, d.Pool, psql.Delete("logins").Where(sq.Eq{"token_hash": tokenHash}))
	return err
}

// DeleteLogins deletes every login of user and returns how many there were.
func (d *DB) DeleteLogins(ctx context.Context, user string) (int64, error) {
	return deleteLogins(ctx, d.Pool, user)
}

// deleteLogins ends every login of user, in r (the pool or a transaction).
func deleteLogins(ctx context.Context, r runner, user string) (int64, error) {
	return affected(ctx, r, psql.Delete("logins").Where(sq.Eq{"user_id": user}))
}

// LoginAlive reports whether login id exists and has not expired.
func (d *DB) LoginAlive(ctx context.Context, id int64) (bool, error) {
	var ok bool
	err := queryRow(ctx, d.Pool, psql.Select().
		Column(sq.Expr("EXISTS (?)", sub.Select("1").From("logins").Where(sq.Eq{"id": id}).Where(live)))).Scan(&ok)
	return ok, err
}

// Logins returns user's live logins, most recently used first.
func (d *DB) Logins(ctx context.Context, user string) ([]LoginInfo, error) {
	rows, err := query(ctx, d.Pool, psql.Select("id", "created", "last_used", "user_agent", "COALESCE(host(ip), '')").
		From("logins").Where(sq.Eq{"user_id": user}).Where(live).OrderBy("last_used DESC", "id DESC"))
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
	return affected(ctx, d.Pool, psql.Delete("logins").Where("NOT ("+live+")"))
}
