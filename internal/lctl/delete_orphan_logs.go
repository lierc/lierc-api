package lctl

import (
	"context"
	"fmt"
)

// A log or archive row is an orphan when its connection or its user no
// longer exists: a connection can outlive its user (nothing ties them), and
// rows keep the user's id after the connection is gone.
const orphanArchive = `(NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = log_archive.connection)
	OR NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = log_archive."user"))`

// goneConnections and goneUsers list the connection and user ids in log
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

func deleteOrphanLogs(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "delete-orphan-logs", "delete-orphan-logs [--delete] [--batch N]\n\nFinds the connections and users that no longer exist but still have log rows,\nand counts their archive rows; with --delete, deletes those log and archive rows.\nDeletes in batches, each its own statement, so it never holds one huge\ntransaction; interrupting it keeps the batches already deleted.")
	del := fs.Bool("delete", false, "delete the rows")
	batch := fs.Int("batch", 10000, "rows per delete")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *batch < 1 {
		return &UsageError{Msg: "--batch needs a positive number"}
	}
	conns, err := ids(ctx, env, goneConnections)
	if err != nil {
		return err
	}
	users, err := ids(ctx, env, goneUsers)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "%d gone connection(s) and %d gone user(s) still in log\n", len(conns), len(users))
	if !*del {
		// no row counts: counting them reads every orphan row, which is
		// most of the work of deleting them
		fmt.Fprintf(env.Stdout, "run with --delete to delete their log rows\n")
		var na int
		if err := env.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM log_archive WHERE `+orphanArchive).Scan(&na); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%d archive row(s) to delete\n", na)
		return nil
	}
	// each gone id's rows, in batches through its index; each batch checks
	// again that the id is still gone
	var total int64
	stopped := func(err error) error {
		fmt.Fprintf(env.Stdout, "deleted %d log row(s) before stopping\n", total)
		return err
	}
	// one line per id that had rows, and a line every orphanProgressEvery
	// rows while a big one runs
	run := func(kind, id, del string) error {
		n, err := deleteBatches(ctx, env, *batch, del, id, func(n int64) {
			fmt.Fprintf(env.Stdout, "%s %s: %d row(s) so far\n", kind, id, n)
		})
		total += n
		if n > 0 {
			fmt.Fprintf(env.Stdout, "%s %s: deleted %d row(s) (%d so far)\n", kind, id, n, total)
		}
		return err
	}
	for _, c := range conns {
		if err := run("connection", c,
			`DELETE FROM log WHERE id IN (SELECT id FROM log WHERE connection = $1 LIMIT $2)
			   AND NOT EXISTS (SELECT 1 FROM connection WHERE id = $1)`); err != nil {
			return stopped(err)
		}
	}
	for _, u := range users {
		if err := run("user", u,
			`DELETE FROM log WHERE id IN (SELECT id FROM log WHERE "user" = $1 LIMIT $2)
			   AND NOT EXISTS (SELECT 1 FROM "user" WHERE id = $1)`); err != nil {
			return stopped(err)
		}
	}
	fmt.Fprintf(env.Stdout, "deleted %d log row(s)\n", total)

	// orphaned archive rows, in the same batches
	var archived int64
	for {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(env.Stdout, "deleted %d archive row(s) before stopping\n", archived)
			return err
		}
		tag, err := env.DB.Pool.Exec(ctx,
			`DELETE FROM log_archive WHERE id IN (SELECT id FROM log_archive WHERE `+orphanArchive+` LIMIT $1)`, *batch)
		if err != nil {
			return err
		}
		archived += tag.RowsAffected()
		if tag.RowsAffected() < int64(*batch) {
			break
		}
	}
	fmt.Fprintf(env.Stdout, "deleted %d archive row(s)\n", archived)
	return nil
}

// ids returns the ids query lists.
func ids(ctx context.Context, env *Env, query string) ([]string, error) {
	rows, err := env.DB.Pool.Query(ctx, query)
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

// orphanProgressEvery is how many of one id's rows delete-orphan-logs
// deletes between progress lines.
var orphanProgressEvery int64 = 100000

// deleteBatches runs del, a delete taking (id, batch), until a run deletes
// fewer than batch rows, and returns how many it deleted. Each time the
// count passes a multiple of orphanProgressEvery it calls progress with it.
// It stops between batches when ctx is done.
func deleteBatches(ctx context.Context, env *Env, batch int, del, id string, progress func(int64)) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		tag, err := env.DB.Pool.Exec(ctx, del, id, batch)
		if err != nil {
			return total, err
		}
		before := total
		total += tag.RowsAffected()
		if total/orphanProgressEvery > before/orphanProgressEvery {
			progress(total)
		}
		if tag.RowsAffected() < int64(batch) {
			return total, nil
		}
	}
}
