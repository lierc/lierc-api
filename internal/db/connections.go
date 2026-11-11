package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SaveConnectionLimited saves the connection only if user has fewer than
// max, and says whether it did (DB.pm save_connection_limited). The user's
// row is locked while it counts, so two creates at once are counted one
// after the other; a user that's gone saves nothing.
func (d *DB) SaveConnectionLimited(ctx context.Context, id, user string, config []byte, max int) (bool, error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var one int
	if err := tx.QueryRow(ctx, `SELECT 1 FROM "user" WHERE id=$1 FOR UPDATE`, user).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM connection WHERE "user"=$1`, user).Scan(&n); err != nil {
		return false, err
	}
	if n >= max {
		return false, nil
	}
	if _, err := tx.Exec(ctx, `INSERT INTO connection (id, "user", config, created, updated) VALUES ($1,$2,$3,NOW(),NOW())`, id, user, string(config)); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

// SaveConnection saves a connection with no limit check.
func (d *DB) SaveConnection(ctx context.Context, id, user string, config []byte) error {
	_, err := d.Pool.Exec(ctx, `INSERT INTO connection (id, "user", config, created, updated) VALUES ($1,$2,$3,NOW(),NOW())`, id, user, string(config))
	return err
}

// DeleteConnection deletes connection id's row.
func (d *DB) DeleteConnection(ctx context.Context, id string) error {
	_, err := d.Pool.Exec(ctx, `DELETE FROM connection WHERE id=$1`, id)
	return err
}
