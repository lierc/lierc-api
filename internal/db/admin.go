package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The reads behind the admin API (and so lctl). Each is the query one of
// lctl's commands made itself before.

// UserRow is a user as the admin API lists it.
type UserRow struct {
	ID, Username, Email string
	Verified            *bool      // NULL: an account from before signups had to verify
	LastLogin           *time.Time // NULL on some old accounts; in time.Local as scanned (the admin layer converts to UTC)
	Registered          *time.Time // NULL for accounts from before the column; likewise
	Connections         int
	Password            string // "bcrypt", or "none" for an account that has to reset it
}

// UserFilter narrows ListUsers. A nil field doesn't filter; a non-nil empty
// IDs matches no one.
type UserFilter struct {
	InactiveSince *time.Time // never logged in, or last login before this
	IDs           []string
}

// userRows lists the users matching where, in inactive-users' order: never
// logged in first, then the oldest login, then id.
func (d *DB) userRows(ctx context.Context, where sq.Sqlizer) ([]UserRow, error) {
	rows, err := query(ctx, d.Pool, psql.Select(
		"u.id", "u.username", "u.email", "u.verified", "u.last_login", "u.registered",
		`(SELECT count(*) FROM connections c WHERE c.user_id = u.id)`,
		"CASE WHEN coalesce(u.password_hash, '') <> '' THEN 'bcrypt' ELSE 'none' END").
		From("users u").Where(where).OrderBy("u.last_login NULLS FIRST", "u.id"))
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
// oldest login, then by id.
func (d *DB) ListUsers(ctx context.Context, f UserFilter) ([]UserRow, error) {
	where := sq.And{}
	if f.InactiveSince != nil {
		where = append(where, inactiveSince(*f.InactiveSince))
	}
	if f.IDs != nil {
		where = append(where, sq.Expr("u.id = ANY(?)", f.IDs))
	}
	return d.userRows(ctx, where)
}

// inactiveSince matches a user u that never logged in, or last did before
// t.
func inactiveSince(t time.Time) sq.Sqlizer {
	return sq.Or{sq.Eq{"u.last_login": nil}, sq.Lt{"u.last_login": t}}
}

// LookupUsers finds users whose id, username or email is q (names and
// emails match case-insensitively). A username can equal another user's
// email, so more than one can match.
func (d *DB) LookupUsers(ctx context.Context, q string) ([]UserRow, error) {
	return d.userRows(ctx, sq.Or{sq.Eq{"u.id": q}, sq.Expr("lower(u.username) = lower(?)", q), sq.Expr("lower(u.email) = lower(?)", q)})
}

// countKey is the name each table's row count goes by in the counts the
// admin API returns: the table's name from before the tables were renamed
// (migration 2), so the API's output and lctl's didn't change.
var countKey = map[string]string{
	"logs": "log", "log_archives": "log_archive", "seen_markers": "seen", "private_chats": "private",
	"prefs": "pref", "web_push_subscriptions": "web_push", "connections": "connection", "logins": "login",
}

// UserCounts counts, by table, the rows deleting user id would remove: those
// of the user and of its connections.
func (d *DB) UserCounts(ctx context.Context, id string) (map[string]int64, error) {
	return userCounts(ctx, d.Pool, id)
}

func userCounts(ctx context.Context, r runner, id string) (map[string]int64, error) {
	owned := sq.Eq{"user_id": id}
	ofConns := sq.Expr("connection_id IN (?)", sub.Select("id").From("connections").Where(owned))
	either := sq.Or{owned, ofConns}
	return countRows(ctx, r, []tableRows{
		{"logs", either}, {"log_archives", either}, {"seen_markers", either},
		{"private_chats", ofConns},
		{"prefs", owned}, {"web_push_subscriptions", owned}, {"connections", owned}, {"logins", owned},
	})
}

// tableRows is the rows of table matching where.
type tableRows struct {
	table string
	where sq.Sqlizer
}

// countRows counts each tableRows, keyed by the table's countKey.
func countRows(ctx context.Context, r runner, tables []tableRows) (map[string]int64, error) {
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		if err := queryRow(ctx, r, psql.Select("count(*)").From(t.table).Where(t.where)).Scan(&n); err != nil {
			return nil, err
		}
		out[countKey[t.table]] = n
	}
	return out, nil
}

// ConnectionRow is a connection as the admin API lists it.
type ConnectionRow struct {
	ID, User string
	Username string // empty when the owner is gone
	Instance string // "main" or "next": the liercd column
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
	q := psql.Select("c.id", "c.user_id", "coalesce(u.username, '')", "c.liercd", "c.config::text").
		From("connections c").LeftJoin(`users u ON u.id = c.user_id`).
		OrderBy("u.last_login NULLS FIRST", "c.user_id", "c.id")
	if f.ID != "" {
		q = q.Where(sq.Eq{"c.id": f.ID})
	}
	if f.User != "" {
		q = q.Where(sq.Eq{"c.user_id": f.User})
	}
	if f.Instance != "" {
		q = q.Where(onInstance(f.Instance))
	}
	if f.InactiveSince != nil {
		q = q.Where(sq.NotEq{"u.id": nil}).Where(inactiveSince(*f.InactiveSince))
	}
	rows, err := query(ctx, d.Pool, q)
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

// onInstance matches a connection c on liercd instance (main or next).
func onInstance(instance string) sq.Sqlizer {
	return sq.Eq{"c.liercd": instance}
}

// ConnectionCounts counts, by table, the rows deleting connection id would
// remove, apart from the connection's own.
func (d *DB) ConnectionCounts(ctx context.Context, id string) (map[string]int64, error) {
	return connectionCounts(ctx, d.Pool, id)
}

func connectionCounts(ctx context.Context, r runner, id string) (map[string]int64, error) {
	ofConn := sq.Eq{"connection_id": id}
	return countRows(ctx, r, []tableRows{
		{"logs", ofConn}, {"log_archives", ofConn}, {"seen_markers", ofConn}, {"private_chats", ofConn},
	})
}

// staleSeen is the seen rows prune-seen deletes: those not updated in a
// month. (A connection's rows go with it: seen_markers_connection_id_fkey.)
var staleSeen = sq.Expr("updated < now() - interval '1 month'")

// StaleSeenCount counts the seen rows prune-seen would delete.
func (d *DB) StaleSeenCount(ctx context.Context) (int64, error) {
	var n int64
	err := queryRow(ctx, d.Pool, psql.Select("count(*)").From("seen_markers").Where(staleSeen)).Scan(&n)
	return n, err
}

// ErrNotFound is returned when the user or connection to delete isn't there.
var ErrNotFound = errors.New("not found")

// PruneStaleSeen deletes the seen rows StaleSeenCount counts.
func (d *DB) PruneStaleSeen(ctx context.Context) (int64, error) {
	return affected(ctx, d.Pool, psql.Delete("seen_markers").Where(staleSeen))
}

// DeleteUserData deletes user id, and so (the keys cascade) everything of
// theirs: connections, their logs, archive, seen markers and private chats,
// prefs, web push subscriptions and logins. It returns a delete change for
// each connection, to announce once this has returned, and the rows deleted
// per table. ErrNotFound if there is no such user.
func (d *DB) DeleteUserData(ctx context.Context, id string) (changes []ConnectionChange, counts map[string]int64, err error) {
	err = retryDeadlock(ctx, func() error {
		changes, counts, err = d.deleteUser(ctx, id)
		return err
	})
	return changes, counts, err
}

func (d *DB) deleteUser(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var found string
	if err := queryRow(ctx, tx, psql.Select("id").From("users").Where(sq.Eq{"id": id}).Suffix("FOR UPDATE")).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	rows, err := query(ctx, tx, psql.Select("id").From("connections").Where(sq.Eq{"user_id": id}).OrderBy("id"))
	if err != nil {
		return nil, nil, err
	}
	changes, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ConnectionChange, error) {
		c := ConnectionChange{User: id, Action: "delete"}
		return c, row.Scan(&c.Connection)
	})
	if err != nil {
		return nil, nil, err
	}
	counts, err := userCounts(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	if _, err := exec(ctx, tx, psql.Delete("users").Where(sq.Eq{"id": id})); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return changes, counts, nil
}

// DeleteConnectionData deletes connection id, and so (the keys cascade) its
// logs, archive, seen markers and private chats. It returns the
// connection's delete change and the rows deleted per table. ErrNotFound if
// there is no such connection.
func (d *DB) DeleteConnectionData(ctx context.Context, id string) (changes []ConnectionChange, counts map[string]int64, err error) {
	err = retryDeadlock(ctx, func() error {
		changes, counts, err = d.deleteConnection(ctx, id)
		return err
	})
	return changes, counts, err
}

func (d *DB) deleteConnection(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var owner string
	if err := queryRow(ctx, tx, connectionByID(id, "user_id").Suffix("FOR UPDATE")).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	counts, err := connectionCounts(ctx, tx, id)
	if err != nil {
		return nil, nil, err
	}
	n, err := affected(ctx, tx, psql.Delete("connections").Where(sq.Eq{"id": id}))
	if err != nil {
		return nil, nil, err
	}
	counts[countKey["connections"]] = n
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return []ConnectionChange{{User: owner, Connection: id, Action: "delete"}}, counts, nil
}

// deadlockTries is how many times a delete runs before a deadlock (with the
// archiver, say, moving the same rows) is returned as an error.
const deadlockTries = 3

// retryDeadlock runs f again, 100ms later, when it lost a deadlock.
func retryDeadlock(ctx context.Context, f func() error) error {
	for try := 1; ; try++ {
		err := f()
		if !isCode(err, "40P01") || try == deadlockTries {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// isCode reports whether err is a PostgreSQL error with SQLSTATE code.
func isCode(err error, code string) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == code
}
