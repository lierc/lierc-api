package lctl

import (
	"context"
	"fmt"
)

const staleSeen = `NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = seen.connection)
	OR updated < now() - interval '1 month'`

const orphanLog = `NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = log.connection)`

func pruneSeen(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "prune-seen", "prune-seen [--delete]\n\nCounts, or with --delete deletes, seen rows whose connection no longer\nexists or that weren't updated in a month.")
	del := fs.Bool("delete", false, "delete the rows")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if !*del {
		var n int
		if err := env.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM seen WHERE `+staleSeen).Scan(&n); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%d seen row(s) to delete; run with --delete to delete them\n", n)
		return nil
	}
	tag, err := env.DB.Pool.Exec(ctx, `DELETE FROM seen WHERE `+staleSeen)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "deleted %d seen row(s)\n", tag.RowsAffected())
	return nil
}

func pruneLogs(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "prune-logs", "prune-logs [--delete] [--batch N]\n\nCounts, or with --delete deletes, log rows whose connection no longer\nexists. Deletes in batches, each its own statement, so it never holds one\nhuge transaction; interrupting it keeps the batches already deleted.")
	del := fs.Bool("delete", false, "delete the rows")
	batch := fs.Int("batch", 10000, "rows per delete")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *batch < 1 {
		return &UsageError{Msg: "--batch needs a positive number"}
	}
	if !*del {
		var n int
		if err := env.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM log WHERE `+orphanLog).Scan(&n); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%d log row(s) to delete; run with --delete to delete them\n", n)
		return nil
	}
	// find the deleted connections once, then delete each one's rows in
	// batches through the log (connection, ...) index
	rows, err := env.DB.Pool.Query(ctx, `SELECT DISTINCT connection FROM log WHERE `+orphanLog)
	if err != nil {
		return err
	}
	var conns []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			rows.Close()
			return err
		}
		conns = append(conns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var total int64
	for _, c := range conns {
		for {
			if err := ctx.Err(); err != nil {
				fmt.Fprintf(env.Stdout, "deleted %d log row(s) before stopping\n", total)
				return err
			}
			tag, err := env.DB.Pool.Exec(ctx,
				`DELETE FROM log WHERE id IN (SELECT id FROM log WHERE connection = $1 LIMIT $2)
				   AND NOT EXISTS (SELECT 1 FROM connection WHERE id = $1)`, c, *batch)
			if err != nil {
				return err
			}
			total += tag.RowsAffected()
			if tag.RowsAffected() < int64(*batch) {
				break
			}
		}
	}
	fmt.Fprintf(env.Stdout, "deleted %d log row(s)\n", total)
	return nil
}
