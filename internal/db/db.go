// Package db is the stream's Postgres access layer, ported from
// lib/API/Stream/DB.pm.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lierc/lierc-api/internal/config"
)

// DB holds the pool; every error it returns is an *Error.
type DB struct{ Pool *Pool }

type Connection struct {
	ID     string
	Config json.RawMessage
}

// Open connects using cfg. DBHost may carry a port ("host:5433"); without one
// the Postgres default is used.
func Open(ctx context.Context, cfg config.Config) (*DB, error) {
	u := url.URL{Scheme: "postgres", Path: "/" + cfg.DBName}
	host := cfg.DBHost
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "5432")
	}
	u.Host = host
	if cfg.DBUser != "" {
		if cfg.DBPass != "" {
			u.User = url.UserPassword(cfg.DBUser, cfg.DBPass)
		} else {
			u.User = url.User(cfg.DBUser)
		}
	}
	maxConns := cfg.DBMaxConns
	if maxConns <= 0 {
		maxConns = config.DefaultDBMaxConns
	}
	q := url.Values{"sslmode": {"prefer"}, "pool_max_conns": {strconv.Itoa(maxConns)}}
	u.RawQuery = q.Encode()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return &DB{Pool: &Pool{pool}}, nil
}

func (d *DB) Close() { d.Pool.Close() }

// UserState is whether the user exists, and whether it has yet to verify
// its email address (and so can't use the app). A NULL verified is an
// account from before signups had to verify, and is let in.
func (d *DB) UserState(ctx context.Context, id string) (exists, unverified bool, err error) {
	err = d.Pool.QueryRow(ctx, `SELECT verified IS NOT DISTINCT FROM false FROM "user" WHERE id=$1`, id).Scan(&unverified)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	return true, unverified, nil
}

func (d *DB) Connections(ctx context.Context, user string) ([]Connection, error) {
	rows, err := d.Pool.Query(ctx, `SELECT id, config FROM connection WHERE "user"=$1`, user)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	conns := []Connection{}
	for rows.Next() {
		var c Connection
		if err := rows.Scan(&c.ID, &c.Config); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}

func (d *DB) LookupConfig(ctx context.Context, id string) ([]byte, bool, error) {
	var cfg []byte
	err := d.Pool.QueryRow(ctx, `SELECT config FROM connection WHERE id=$1`, id).Scan(&cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return cfg, true, nil
}

// LookupLiercd returns the liercd instance connection id is on: "" for main
// LookupConnection returns a connection's owner and saved config; ok is
// false when there's no such connection.
func (d *DB) LookupConnection(ctx context.Context, id string) (owner string, cfg json.RawMessage, ok bool, err error) {
	err = d.Pool.QueryRow(ctx, `SELECT "user", config FROM connection WHERE id=$1`, id).Scan(&owner, &cfg)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, false, nil
	}
	if err != nil {
		return "", nil, false, err
	}
	return owner, cfg, true, nil
}

// (a NULL column) or when there is no such connection.
func (d *DB) LookupLiercd(ctx context.Context, id string) (string, error) {
	var inst *string
	err := d.Pool.QueryRow(ctx, `SELECT liercd FROM connection WHERE id=$1`, id).Scan(&inst)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil || inst == nil {
		return "", err
	}
	return *inst, nil
}

func (d *DB) LookupOwner(ctx context.Context, id string) (string, bool, error) {
	var user string
	err := d.Pool.QueryRow(ctx, `SELECT "user" FROM connection WHERE id=$1`, id).Scan(&user)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return user, true, nil
}

// LatestMessageID is the id of user's newest log row, or 0 if there is none.
// It takes the max for each highlight value, so each is one probe of
// log_user_highlight ("user", highlight, id DESC); a plain max(id) for the
// user can instead scan the primary key back through every newer row.
func (d *DB) LatestMessageID(ctx context.Context, user string) (int64, error) {
	var id int64
	err := d.Pool.QueryRow(ctx, `SELECT GREATEST(
		COALESCE((SELECT max(id) FROM log WHERE "user"=$1 AND highlight), 0),
		COALESCE((SELECT max(id) FROM log WHERE "user"=$1 AND NOT highlight), 0))`, user).Scan(&id)
	return id, err
}

func (d *DB) UpdateConfig(ctx context.Context, id string, config []byte) error {
	_, err := d.Pool.Exec(ctx, `UPDATE connection SET config=$1, updated=NOW() WHERE id=$2`, config, id)
	return err
}

func (d *DB) SaveLastLogin(ctx context.Context, user string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE "user" SET last_login=NOW() WHERE id=$1`, user)
	return err
}

// SetLiercd records which liercd instance connection id is on; "" stores NULL
// (main).
func (d *DB) SetLiercd(ctx context.Context, id, instance string) error {
	var inst *string
	if instance != "" {
		inst = &instance
	}
	_, err := d.Pool.Exec(ctx, `UPDATE connection SET liercd=$2 WHERE id=$1`, id, inst)
	return err
}
