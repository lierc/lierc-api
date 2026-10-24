// Package db is the Postgres access layer, ported from lib/API/DB.pm and
// lib/API/Stream/DB.pm.
package db

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/ids"
)

// DB holds the pool; every error it returns is an *Error.
type DB struct{ Pool *Pool }

type User struct{ ID, Username, Email string }

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
	q := url.Values{"sslmode": {"prefer"}}
	u.RawQuery = q.Encode()
	pool, err := pgxpool.New(ctx, u.String())
	if err != nil {
		return nil, err
	}
	return &DB{Pool: &Pool{pool}}, nil
}

func (d *DB) Close() { d.Pool.Close() }

func (d *DB) Ping(ctx context.Context) error { return d.Pool.Ping(ctx) }

func (d *DB) UserExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "user" WHERE id=$1)`, id).Scan(&ok)
	return ok, err
}

// LookupUser returns nil, nil when the user does not exist.
func (d *DB) LookupUser(ctx context.Context, id string) (*User, error) {
	var u User
	err := d.Pool.QueryRow(ctx, `SELECT id, username, email FROM "user" WHERE id=$1`, id).
		Scan(&u.ID, &u.Username, &u.Email)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

func (d *DB) VerifyOwner(ctx context.Context, connID, user string) (bool, error) {
	var n int
	err := d.Pool.QueryRow(ctx,
		`SELECT COUNT(id) FROM connection WHERE "user"=$1 AND id=$2`, user, connID).Scan(&n)
	return n > 0, err
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

// AddUser inserts a user whose password is already hashed.
func (d *DB) AddUser(ctx context.Context, username, email, hashed string) (id, token string, err error) {
	id, token = ids.New(), ids.New()
	_, err = d.Pool.Exec(ctx,
		`INSERT INTO "user" (id, username, email, password, verify_token) VALUES ($1,$2,$3,$4,$5)`,
		id, username, email, hashed, token)
	if err != nil {
		return "", "", err
	}
	return id, token, nil
}

func (d *DB) SaveConnection(ctx context.Context, id, user string, config []byte) error {
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO connection (id, "user", config) VALUES ($1,$2,$3)`, id, user, config)
	return err
}

func (d *DB) DeleteConnection(ctx context.Context, id string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM connection WHERE id=$1`, id)
	return err
}

func (d *DB) GetToken(ctx context.Context, user string) (string, error) {
	id := ids.New()
	_, err := d.Pool.Exec(ctx, `INSERT INTO token (id, "user", created) VALUES ($1,$2,NOW())`, id, user)
	if err != nil {
		return "", err
	}
	return id, nil
}

// CheckToken consumes the token: true only the first time.
func (d *DB) CheckToken(ctx context.Context, user, token string) (bool, error) {
	tag, err := d.Pool.Exec(ctx, `DELETE FROM token WHERE "user"=$1 AND id=$2`, user, token)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() > 0, nil
}

func (d *DB) UpdateConfig(ctx context.Context, id string, config []byte) error {
	_, err := d.Pool.Exec(ctx, `UPDATE connection SET config=$1 WHERE id=$2`, config, id)
	return err
}

func (d *DB) SaveLastLogin(ctx context.Context, user string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE "user" SET last_login=NOW() WHERE id=$1`, user)
	return err
}
