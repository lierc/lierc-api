package db

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
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
		`(SELECT count(*) FROM connection c WHERE c."user" = u.id)`,
		"CASE WHEN coalesce(u.password_hash, '') <> '' THEN 'bcrypt' ELSE 'none' END").
		From(`"user" u`).Where(where).OrderBy("u.last_login NULLS FIRST", "u.id"))
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
// t. last_login is a timestamp without a zone, stored in UTC.
func inactiveSince(t time.Time) sq.Sqlizer {
	return sq.Or{sq.Eq{"u.last_login": nil}, sq.Expr("u.last_login < (?::timestamptz AT TIME ZONE 'UTC')", t)}
}

// LookupUsers finds users whose id, username or email is q (names and
// emails match case-insensitively). A username can equal another user's
// email, so more than one can match.
func (d *DB) LookupUsers(ctx context.Context, q string) ([]UserRow, error) {
	return d.userRows(ctx, sq.Or{sq.Eq{"u.id": q}, sq.Expr("lower(u.username) = lower(?)", q), sq.Expr("lower(u.email) = lower(?)", q)})
}

// UserCounts counts, by table, the rows deleting user id would remove: those
// of the user and of its connections. The log and its archive also match by
// user id: rows of a connection deleted before the user still carry it.
func (d *DB) UserCounts(ctx context.Context, id string) (map[string]int64, error) {
	owned := sq.Eq{`"user"`: id}
	ofConns := sq.Expr("connection IN (?)", sub.Select("id").From("connection").Where(owned))
	either := sq.Or{owned, ofConns}
	tables := []struct {
		table string
		where sq.Sqlizer
	}{
		{"log", either}, {"log_archive", either}, {"seen", either},
		{"private", ofConns},
		{"pref", owned}, {"web_push", owned}, {"connection", owned}, {"login", owned},
	}
	out := make(map[string]int64, len(tables))
	for _, t := range tables {
		var n int64
		if err := queryRow(ctx, d.Pool, psql.Select("count(*)").From(t.table).Where(t.where)).Scan(&n); err != nil {
			return nil, err
		}
		out[t.table] = n
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
	q := psql.Select("c.id", `c."user"`, "coalesce(u.username, '')", "coalesce(nullif(c.liercd, ''), 'main')", "c.config::text").
		From("connection c").LeftJoin(`"user" u ON u.id = c."user"`).
		OrderBy("u.last_login NULLS FIRST", `c."user"`, "c.id")
	if f.ID != "" {
		q = q.Where(sq.Eq{"c.id": f.ID})
	}
	if f.User != "" {
		q = q.Where(sq.Eq{`c."user"`: f.User})
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

// onInstance matches a connection c on liercd instance (main or next); main
// is a NULL or empty column.
func onInstance(instance string) sq.Sqlizer {
	if instance == "main" {
		instance = ""
	}
	return sq.Expr("coalesce(c.liercd, '') = ?", instance)
}

// ConnectionCounts counts, by table, the rows deleting connection id would
// remove, apart from the connection's own.
func (d *DB) ConnectionCounts(ctx context.Context, id string) (map[string]int64, error) {
	out := map[string]int64{}
	for _, t := range []string{"log", "log_archive", "seen", "private"} {
		var n int64
		if err := queryRow(ctx, d.Pool, psql.Select("count(*)").From(t).Where(sq.Eq{"connection": id})).Scan(&n); err != nil {
			return nil, err
		}
		out[t] = n
	}
	return out, nil
}

// LastLogID is the highest log id, 0 when the log is empty.
func (d *DB) LastLogID(ctx context.Context) (int64, error) {
	var n int64
	err := queryRow(ctx, d.Pool, psql.Select("coalesce(max(id), 0)").From("log")).Scan(&n)
	return n, err
}

// staleSeen is the seen rows prune-seen deletes: their connection is gone,
// or they haven't been updated in a month.
var staleSeen = sq.Or{
	sq.Expr("NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = seen.connection)"),
	sq.Expr("updated < now() - interval '1 month'"),
}

// StaleSeenCount counts the seen rows prune-seen would delete.
func (d *DB) StaleSeenCount(ctx context.Context) (int64, error) {
	var n int64
	err := queryRow(ctx, d.Pool, psql.Select("count(*)").From("seen").Where(staleSeen)).Scan(&n)
	return n, err
}

// ErrNotFound is returned when the user or connection to delete isn't there.
var ErrNotFound = errors.New("not found")

// PruneStaleSeen deletes the seen rows StaleSeenCount counts.
func (d *DB) PruneStaleSeen(ctx context.Context) (int64, error) {
	return affected(ctx, d.Pool, psql.Delete("seen").Where(staleSeen))
}

// deleteFrom deletes from table where cond and adds the count under table.
func deleteFrom(ctx context.Context, tx pgx.Tx, counts map[string]int64, table string, cond sq.Sqlizer) error {
	n, err := affected(ctx, tx, psql.Delete(table).Where(cond))
	if err != nil {
		return err
	}
	counts[table] += n
	return nil
}

// DeleteUserData deletes user id and everything of theirs in one
// transaction: log and log_archive (also the rows of connections deleted
// before the user, which carry the user's id), seen, private, pref,
// web_push, connection, login and the user. It returns a delete change for
// each connection, to announce once this has returned, with the number of
// rows deleted per table. SweepLateLogs catches log rows written after.
// ErrNotFound if there is no such user.
func (d *DB) DeleteUserData(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var found string
	if err := queryRow(ctx, tx, psql.Select("id").From(`"user"`).Where(sq.Eq{"id": id}).Suffix("FOR UPDATE")).Scan(&found); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	rows, err := query(ctx, tx, psql.Select("id").From("connection").Where(sq.Eq{`"user"`: id}).OrderBy("id"))
	if err != nil {
		return nil, nil, err
	}
	conns := []string{}
	changes := []ConnectionChange{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return nil, nil, err
		}
		conns = append(conns, c)
		changes = append(changes, ConnectionChange{User: id, Connection: c, Action: "delete"})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	counts := map[string]int64{}
	owned := sq.Eq{`"user"`: id}
	ofConns := sq.Expr("connection = ANY(?)", conns)
	either := sq.Or{owned, ofConns}
	steps := []struct {
		table string
		cond  sq.Sqlizer
	}{
		{"log", either},
		// after the log delete, so a block the archiver moved in between is caught
		{"log_archive", either},
		{"seen", either},
		{"private", ofConns},
		{"pref", owned},
		{"web_push", owned},
		{"connection", owned},
		{"login", owned},
	}
	for _, s := range steps {
		if err := deleteFrom(ctx, tx, counts, s.table, s.cond); err != nil {
			return nil, nil, err
		}
	}
	if _, err := exec(ctx, tx, psql.Delete(`"user"`).Where(sq.Eq{"id": id})); err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return changes, counts, nil
}

// DeleteConnectionData deletes connection id and its log, log_archive, seen
// and private rows in one transaction. It returns the connection's delete
// change and the rows deleted per table. ErrNotFound if there is no such
// connection.
func (d *DB) DeleteConnectionData(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var owner string
	if err := queryRow(ctx, tx, connectionByID(id, `"user"`).Suffix("FOR UPDATE")).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil, ErrNotFound
		}
		return nil, nil, err
	}
	counts := map[string]int64{}
	ofConn := sq.Eq{"connection": id}
	for _, s := range []struct {
		table string
		cond  sq.Sqlizer
	}{
		{"log", ofConn},
		// after the log delete, so a block the archiver moved in between is caught
		{"log_archive", ofConn},
		{"seen", ofConn},
		{"private", ofConn},
		{"connection", sq.Eq{"id": id}},
	} {
		if err := deleteFrom(ctx, tx, counts, s.table, s.cond); err != nil {
			return nil, nil, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return []ConnectionChange{{User: owner, Connection: id, Action: "delete"}}, counts, nil
}

// SweepLateLogs deletes the log rows with id > after that the logger wrote
// once a delete had begun: those of user (none when empty) or of
// connections. It looks only at those, by id, so it reads seconds of log.
// It also clears the log_archive blocks of the same owners, which the
// archiver may have moved since. It returns the log rows and the archive
// blocks it deleted.
func (d *DB) SweepLateLogs(ctx context.Context, user string, connections []string, after int64) (rows, blocks int64, err error) {
	owners := sq.Or{sq.Expr("connection = ANY(?)", connections)}
	if user != "" {
		owners = append(owners, sq.Eq{`"user"`: user})
	}
	if rows, err = affected(ctx, d.Pool, psql.Delete("log").Where(sq.Gt{"id": after}).Where(owners)); err != nil {
		return 0, 0, err
	}
	if blocks, err = affected(ctx, d.Pool, psql.Delete("log_archive").Where(owners)); err != nil {
		return 0, 0, err
	}
	return rows, blocks, nil
}
