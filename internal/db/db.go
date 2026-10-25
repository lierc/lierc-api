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

func (d *DB) UserExists(ctx context.Context, id string) (bool, error) {
	var ok bool
	err := d.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM "user" WHERE id=$1)`, id).Scan(&ok)
	return ok, err
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

func (d *DB) UpdateConfig(ctx context.Context, id string, config []byte) error {
	_, err := d.Pool.Exec(ctx, `UPDATE connection SET config=$1 WHERE id=$2`, config, id)
	return err
}

func (d *DB) SaveLastLogin(ctx context.Context, user string) error {
	_, err := d.Pool.Exec(ctx, `UPDATE "user" SET last_login=NOW() WHERE id=$1`, user)
	return err
}
