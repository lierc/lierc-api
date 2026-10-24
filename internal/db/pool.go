package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Error is any error from the database layer: Postgres errors, connect and
// pool errors, context errors from a query, pgx.ErrNoRows. Its text can name
// the host, user and database, so the API never shows it (Perl answers null
// for every DBD error). errors.Is and errors.As see the wrapped error.
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var de *Error
	if errors.As(err, &de) {
		return err
	}
	return &Error{Err: err}
}

// Pool is a pgxpool.Pool whose Exec, Query, QueryRow, Ping and Acquire return
// every error, including those from rows and scans, as an *Error.
type Pool struct{ *pgxpool.Pool }

func (p *Pool) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	tag, err := p.Pool.Exec(ctx, sql, args...)
	return tag, wrap(err)
}

func (p *Pool) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	r, err := p.Pool.Query(ctx, sql, args...)
	if r != nil {
		r = rows{r}
	}
	return r, wrap(err)
}

func (p *Pool) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	return row{p.Pool.QueryRow(ctx, sql, args...)}
}

func (p *Pool) Ping(ctx context.Context) error { return wrap(p.Pool.Ping(ctx)) }

func (p *Pool) Acquire(ctx context.Context) (*pgxpool.Conn, error) {
	c, err := p.Pool.Acquire(ctx)
	return c, wrap(err)
}

type rows struct{ pgx.Rows }

func (r rows) Err() error { return wrap(r.Rows.Err()) }

func (r rows) Scan(dest ...any) error { return wrap(r.Rows.Scan(dest...)) }

func (r rows) Values() ([]any, error) {
	v, err := r.Rows.Values()
	return v, wrap(err)
}

type row struct{ pgx.Row }

func (r row) Scan(dest ...any) error { return wrap(r.Row.Scan(dest...)) }
