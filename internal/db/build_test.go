package db

import (
	"context"
	"errors"
	"testing"

	sq "github.com/Masterminds/squirrel"
)

func TestBuilderUsesDollarPlaceholders(t *testing.T) {
	sql, args, err := psql.Select("id").From("users").Where(sq.Eq{"id": "a"}).Where("x < ?", 2).ToSql()
	if err != nil {
		t.Fatal(err)
	}
	if want := `SELECT id FROM users WHERE id = $1 AND x < $2`; sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 2 {
		t.Errorf("args = %v", args)
	}
}

// A builder that can't build is an *Error from every runner, and nothing
// is sent: the pool here is nil, so sending would panic.
func TestBuildErrorIsWrapped(t *testing.T) {
	ctx := context.Background()
	var p *Pool
	bad := psql.Select() // no columns
	var de *Error

	if _, err := exec(ctx, p, psql.Delete("")); !errors.As(err, &de) {
		t.Errorf("exec: %v, want *Error", err)
	}
	if _, err := query(ctx, p, bad); !errors.As(err, &de) {
		t.Errorf("query: %v, want *Error", err)
	}
	var n int
	if err := queryRow(ctx, p, bad).Scan(&n); !errors.As(err, &de) {
		t.Errorf("queryRow: %v, want *Error", err)
	}
}

// A subquery built with sub is numbered with the query it's in.
func TestSubqueryPlaceholders(t *testing.T) {
	inner := sub.Select("id").From("connections").Where(sq.Eq{"owner": "u"}).Suffix("LIMIT ?", 5)
	sql, args, err := psql.Select("count(*)").From("logs").
		Where(sq.Or{sq.Eq{"owner": "u"}, sq.Expr("connection_id IN (?)", inner)}).ToSql()
	if err != nil {
		t.Fatal(err)
	}
	if want := `SELECT count(*) FROM logs WHERE (owner = $1 OR connection_id IN (SELECT id FROM connections WHERE owner = $2 LIMIT $3))`; sql != want {
		t.Errorf("sql = %q, want %q", sql, want)
	}
	if len(args) != 3 {
		t.Errorf("args = %v", args)
	}
}
