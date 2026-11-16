package db

import (
	"context"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
)

// SaveConnectionLimited saves the connection only if user has fewer than
// max, and says whether it did. The user's
// row is locked while it counts, so two creates at once are counted one
// after the other; a user that's gone saves nothing.
func (d *DB) SaveConnectionLimited(ctx context.Context, id, user string, config []byte, max int) (bool, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var one int
	if err := queryRow(ctx, tx, psql.Select("1").From("users").Where(sq.Eq{"id": user}).Suffix("FOR UPDATE")).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var n int
	if err := queryRow(ctx, tx, psql.Select("count(*)").From("connections").Where(sq.Eq{"user_id": user})).Scan(&n); err != nil {
		return false, err
	}
	if n >= max {
		return false, nil
	}
	if _, err := exec(ctx, tx, insertConnection(id, user, config)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func insertConnection(id, user string, config []byte) sq.InsertBuilder {
	return psql.Insert("connections").Columns("id", "user_id", "config", "created", "updated").
		Values(id, user, string(config), sq.Expr("now()"), sq.Expr("now()"))
}

// SaveConnection saves a connection with no limit check.
func (d *DB) SaveConnection(ctx context.Context, id, user string, config []byte) error {
	_, err := exec(ctx, d.Pool, insertConnection(id, user, config))
	return err
}

// DeleteConnection deletes connection id's row.
func (d *DB) DeleteConnection(ctx context.Context, id string) error {
	_, err := exec(ctx, d.Pool, psql.Delete("connections").Where(sq.Eq{"id": id}))
	return err
}

// MergeConfig merges fields, a JSON object, into the connection's saved
// config in one statement: liercd's own changes (its channels as it joins
// and parts, a nick), from the config topic. It waits for an EditConfig
// of the same row, then lands on top of it. A missing connection, or a
// config or fields that aren't objects, change nothing and aren't errors.
func (d *DB) MergeConfig(ctx context.Context, id string, fields []byte) error {
	f := string(fields)
	_, err := exec(ctx, d.Pool, psql.Update("connections").Set("config", sq.Expr("config::jsonb || ?::jsonb", f)).
		Where(sq.Eq{"id": id}).
		Where("jsonb_typeof(config::jsonb) = 'object'").
		Where("jsonb_typeof(?::jsonb) = 'object'", f))
	return err
}

// EditConfig changes a connection's saved config with the row locked:
// edit gets the saved config and returns the new one, and a MergeConfig
// meanwhile waits and lands on top, so it isn't overwritten. found is
// false, and edit isn't called, for a missing connection. edit's error
// is returned as it is, and nothing is saved.
func (d *DB) EditConfig(ctx context.Context, id string, edit func(saved []byte) ([]byte, error)) (found bool, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var saved []byte
	if err := queryRow(ctx, tx, connectionByID(id, "config").Suffix("FOR UPDATE")).Scan(&saved); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	config, err := edit(saved)
	if err != nil {
		return true, err
	}
	if _, err := exec(ctx, tx, psql.Update("connections").Set("config", config).Set("updated", sq.Expr("now()")).
		Where(sq.Eq{"id": id})); err != nil {
		return true, err
	}
	return true, tx.Commit(ctx)
}
