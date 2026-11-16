package db

import (
	"context"
	"time"
)

// ReviveConnection is a saved connection and its stored config.
type ReviveConnection struct{ ID, Config string }

// ReviveCandidates lists the connections of users who logged in within
// since, in id order. A non-empty instance (main or next) limits it to that
// liercd's connections; main is a NULL or empty column.
func (d *DB) ReviveCandidates(ctx context.Context, since time.Duration, instance string) ([]ReviveConnection, error) {
	q := psql.Select("c.id", "c.config::text").From("connections c").Join(`users u ON u.id = c.user_id`).
		Where("u.last_login > now() - make_interval(secs => ?)", since.Seconds()).
		OrderBy("c.id")
	if instance != "" {
		q = q.Where(onInstance(instance))
	}
	rows, err := query(ctx, d.Pool, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []ReviveConnection
	for rows.Next() {
		var c ReviveConnection
		if err := rows.Scan(&c.ID, &c.Config); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}
