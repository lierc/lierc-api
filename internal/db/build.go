package db

import (
	"context"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// psql builds queries with Postgres's $1 placeholders. Queries a builder
// doesn't fit (CTEs, unnest over arrays, the channel reads in sql/) stay as
// SQL text.
var psql = sq.StatementBuilder.PlaceholderFormat(sq.Dollar)

// sub builds subqueries, with ? placeholders: the query a subquery is put
// in numbers them. A subquery built with psql would number its own from $1.
var sub = sq.StatementBuilder

// runner is the pool or a transaction.
type runner interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// exec, query and queryRow build b and run it on r. Every error they return,
// a build error included, is an *Error.
func exec(ctx context.Context, r runner, b sq.Sqlizer) (pgconn.CommandTag, error) {
	sql, args, err := b.ToSql()
	if err != nil {
		return pgconn.CommandTag{}, wrap(err)
	}
	tag, err := r.Exec(ctx, sql, args...)
	return tag, wrap(err)
}

func query(ctx context.Context, r runner, b sq.Sqlizer) (pgx.Rows, error) {
	sql, args, err := b.ToSql()
	if err != nil {
		return nil, wrap(err)
	}
	res, err := r.Query(ctx, sql, args...)
	if err != nil {
		return nil, wrap(err)
	}
	return rows{res}, nil
}

func queryRow(ctx context.Context, r runner, b sq.Sqlizer) pgx.Row {
	sql, args, err := b.ToSql()
	if err != nil {
		return errRow{wrap(err)}
	}
	return row{r.QueryRow(ctx, sql, args...)}
}

// affected is the rows a write changed, or its error.
func affected(ctx context.Context, r runner, b sq.Sqlizer) (int64, error) {
	tag, err := exec(ctx, r, b)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }
