package db

import (
	"context"
	"encoding/json"
	"time"
)

// The reads behind the admin API (and so lctl). Each is the query one of
// lctl's commands made itself before.

// UserRow is a user as the admin API lists it.
type UserRow struct {
	ID, Username, Email string
	Verified            *bool      // NULL: an account from before signups had to verify
	LastLogin           *time.Time // NULL on some old accounts; UTC
	Registered          *time.Time // NULL for accounts from before the column; UTC
	Connections         int
	Password            string // how it's stored: "bcrypt", "legacy" or "none"
}

// UserFilter narrows ListUsers. A nil field doesn't filter; a non-nil empty
// IDs matches no one.
type UserFilter struct {
	InactiveSince *time.Time // never logged in, or last login before this
	IDs           []string
}

const userColumns = `u.id, u.username, u.email, u.verified, u.last_login, u.registered,
	(SELECT count(*) FROM connection c WHERE c."user" = u.id),
	CASE WHEN coalesce(u.password_hash, '') <> '' THEN 'bcrypt'
	     WHEN coalesce(u.password, '') <> '' THEN 'legacy' ELSE 'none' END`

// userOrder is inactive-users' order: never logged in first, then the
// oldest login.
const userOrder = ` ORDER BY u.last_login NULLS FIRST, u.id`

func (d *DB) userRows(ctx context.Context, where string, args ...any) ([]UserRow, error) {
	rows, err := d.Pool.Query(ctx, `SELECT `+userColumns+` FROM "user" u WHERE `+where+userOrder, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []UserRow{}
	for rows.Next() {
		var u UserRow
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Verified, &u.LastLogin, &u.Registered, &u.Connections, &u.Password); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListUsers lists the users matching f, never-logged-in first, then the
// oldest login, then by id. last_login is a timestamp without a zone,
// stored in UTC.
func (d *DB) ListUsers(ctx context.Context, f UserFilter) ([]UserRow, error) {
	return d.userRows(ctx, `($1::timestamptz IS NULL OR u.last_login IS NULL OR u.last_login < ($1::timestamptz AT TIME ZONE 'UTC'))
	  AND ($2::text[] IS NULL OR u.id = ANY($2))`, f.InactiveSince, f.IDs)
}

// LookupUsers finds users whose id, username or email is q (names and
// emails match case-insensitively). A username can equal another user's
// email, so more than one can match.
func (d *DB) LookupUsers(ctx context.Context, q string) ([]UserRow, error) {
	return d.userRows(ctx, `(u.id = $1 OR lower(u.username) = lower($1) OR lower(u.email) = lower($1))`, q)
}

// userCountTables are the tables a user delete clears, counted by these
// conditions ($1 the user id; a user's connections are looked up). The log
// and its archive also match by user id: rows of a connection deleted
// before the user still carry it.
var userCountTables = []struct{ key, table, where string }{
	{"log", "log", `"user" = $1 OR connection IN (SELECT id FROM connection WHERE "user" = $1)`},
	{"log_archive", "log_archive", `"user" = $1 OR connection IN (SELECT id FROM connection WHERE "user" = $1)`},
	{"seen", "seen", `"user" = $1 OR connection IN (SELECT id FROM connection WHERE "user" = $1)`},
	{"private", "private", `connection IN (SELECT id FROM connection WHERE "user" = $1)`},
	{"pref", "pref", `"user" = $1`},
	{"web_push", "web_push", `"user" = $1`},
	{"connection", "connection", `"user" = $1`},
	{"login", "login", `"user" = $1`},
}

// UserCounts counts, by table, the rows deleting user id would remove.
func (d *DB) UserCounts(ctx context.Context, id string) (map[string]int64, error) {
	out := make(map[string]int64, len(userCountTables))
	for _, t := range userCountTables {
		var n int64
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+t.table+` WHERE `+t.where, id).Scan(&n); err != nil {
			return nil, err
		}
		out[t.key] = n
	}
	return out, nil
}

// ConnectionRow is a connection as the admin API lists it.
type ConnectionRow struct {
	ID, User string
	Username string // empty when the owner is gone
	Instance string // "main" or "next": the liercd column, main when NULL or empty
	Config   json.RawMessage
}

// ConnectionFilter narrows ListConnections; zero fields don't filter.
type ConnectionFilter struct {
	ID            string
	User          string     // the owner's id
	Instance      string     // "main" or "next"
	InactiveSince *time.Time // owner never logged in, or last login before this; connections without an owner don't match
}

// ListConnections lists the connections matching f, by their owner's last
// login (never first), then user id, then connection id.
func (d *DB) ListConnections(ctx context.Context, f ConnectionFilter) ([]ConnectionRow, error) {
	// main is a NULL or empty column
	stored := f.Instance
	if stored == "main" {
		stored = ""
	}
	rows, err := d.Pool.Query(ctx, `SELECT c.id, c."user", coalesce(u.username, ''), coalesce(nullif(c.liercd, ''), 'main'), c.config::text
	  FROM connection c LEFT JOIN "user" u ON u.id = c."user"
	  WHERE ($1 = '' OR c.id = $1) AND ($5 = '' OR c."user" = $5)
	    AND ($2 = '' OR coalesce(c.liercd, '') = $3)
	    AND ($4::timestamptz IS NULL OR (u.id IS NOT NULL AND (u.last_login IS NULL OR u.last_login < ($4::timestamptz AT TIME ZONE 'UTC'))))
	  ORDER BY u.last_login NULLS FIRST, c."user", c.id`, f.ID, f.Instance, stored, f.InactiveSince, f.User)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectionRow{}
	for rows.Next() {
		var c ConnectionRow
		var cfg string
		if err := rows.Scan(&c.ID, &c.User, &c.Username, &c.Instance, &cfg); err != nil {
			return nil, err
		}
		c.Config = json.RawMessage(cfg)
		out = append(out, c)
	}
	return out, rows.Err()
}

// ConnectionCounts counts, by table, the rows deleting connection id would
// remove, apart from the connection's own.
func (d *DB) ConnectionCounts(ctx context.Context, id string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range []string{"log", "log_archive", "seen", "private"} {
		var n int64
		if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM `+t+` WHERE connection = $1`, id).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// LastLogID is the highest log id, 0 when the log is empty.
func (d *DB) LastLogID(ctx context.Context) (int64, error) {
	var n int64
	err := d.Pool.QueryRow(ctx, `SELECT coalesce(max(id), 0) FROM log`).Scan(&n)
	return n, err
}

// staleSeen is the seen rows prune-seen deletes: their connection is gone,
// or they haven't been updated in a month.
const staleSeen = `NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = seen.connection)
	OR updated < now() - interval '1 month'`

// StaleSeenCount counts the seen rows prune-seen would delete.
func (d *DB) StaleSeenCount(ctx context.Context) (int64, error) {
	var n int64
	err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM seen WHERE `+staleSeen).Scan(&n)
	return n, err
}
