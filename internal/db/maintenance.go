package db

import (
	"context"
	"time"

	sq "github.com/Masterminds/squirrel"
)

// A log or archive row is an orphan when its connection or its user no
// longer exists: a connection can outlive its user (nothing ties them), and
// rows keep the user's id after the connection is gone.
var orphanArchive = sq.Or{
	sq.Expr(`NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = log_archive.connection)`),
	sq.Expr(`NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = log_archive."user")`),
}

// goneConnections and goneUsers are recursive CTEs, kept as SQL. They list the connection and user ids in log
// that no longer exist. Each is a skip scan: one index lookup per distinct
// id (log_connection_channel_id_idx, log_user_highlight_idx), so finding
// them takes seconds rather than a read of the whole table.
const goneConnections = `WITH RECURSIVE ids AS (
	(SELECT connection AS id FROM log ORDER BY connection LIMIT 1)
	UNION ALL
	SELECT (SELECT connection FROM log WHERE connection > ids.id ORDER BY connection LIMIT 1)
	FROM ids WHERE ids.id IS NOT NULL)
SELECT id FROM ids WHERE id IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = ids.id)`

const goneUsers = `WITH RECURSIVE ids AS (
	(SELECT "user" AS id FROM log ORDER BY "user" LIMIT 1)
	UNION ALL
	SELECT (SELECT "user" FROM log WHERE "user" > ids.id ORDER BY "user" LIMIT 1)
	FROM ids WHERE ids.id IS NOT NULL)
SELECT id FROM ids WHERE id IS NOT NULL
	AND NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = ids.id)`

// GoneConnections lists the connection ids in log that no longer exist.
func (d *DB) GoneConnections(ctx context.Context) ([]string, error) {
	return d.idList(ctx, goneConnections)
}

// GoneUsers lists the user ids in log that no longer exist.
func (d *DB) GoneUsers(ctx context.Context) ([]string, error) {
	return d.idList(ctx, goneUsers)
}

func (d *DB) idList(ctx context.Context, query string) ([]string, error) {
	rows, err := d.Pool.Query(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{} // not nil: pgx sends a nil slice as NULL
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CountOrphanArchive counts the log_archive rows whose connection or user is gone.
func (d *DB) CountOrphanArchive(ctx context.Context) (int, error) {
	var n int
	err := queryRow(ctx, d.Pool, psql.Select("count(*)").From("log_archive").Where(orphanArchive)).Scan(&n)
	return n, err
}

// DeleteGoneConnectionLogs deletes up to batch log rows of connection id,
// if it is still gone, and returns how many.
func (d *DB) DeleteGoneConnectionLogs(ctx context.Context, id string, batch int) (int64, error) {
	return deleteGoneLogs(ctx, d, "connection", "connection", id, batch)
}

// DeleteGoneUserLogs deletes up to batch log rows of user id, if it is still
// gone, and returns how many.
func (d *DB) DeleteGoneUserLogs(ctx context.Context, id string, batch int) (int64, error) {
	return deleteGoneLogs(ctx, d, `"user"`, `"user"`, id, batch)
}

// deleteGoneLogs deletes up to batch log rows whose column is id, if table
// has no row with that id.
func deleteGoneLogs(ctx context.Context, d *DB, column, table, id string, batch int) (int64, error) {
	some := sub.Select("id").From("log").Where(sq.Eq{column: id}).Suffix("LIMIT ?", batch)
	return affected(ctx, d.Pool, psql.Delete("log").
		Where(sq.Expr("id IN (?)", some)).
		Where(sq.Expr("NOT EXISTS (?)", sub.Select("1").From(table).Where(sq.Eq{"id": id}))))
}

// DeleteOrphanArchive deletes up to batch orphaned log_archive rows and
// returns how many.
func (d *DB) DeleteOrphanArchive(ctx context.Context, batch int) (int64, error) {
	some := sub.Select("id").From("log_archive").Where(orphanArchive).Suffix("LIMIT ?", batch)
	return affected(ctx, d.Pool, psql.Delete("log_archive").Where(sq.Expr("id IN (?)", some)))
}

// ReviveConnection is a saved connection and its stored config.
type ReviveConnection struct{ ID, Config string }

// ReviveCandidates lists the connections of users who logged in within
// since, in id order. A non-empty instance (main or next) limits it to that
// liercd's connections; main is a NULL or empty column.
func (d *DB) ReviveCandidates(ctx context.Context, since time.Duration, instance string) ([]ReviveConnection, error) {
	q := psql.Select("c.id", "c.config::text").From("connection c").Join(`"user" u ON u.id = c."user"`).
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
