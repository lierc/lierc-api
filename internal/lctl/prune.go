package lctl

import (
	"context"
	"fmt"
)

const staleSeen = `NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = seen.connection)
	OR updated < now() - interval '1 month'`

// A log or archive row is an orphan when its connection or its user no
// longer exists: a connection can outlive its user (nothing ties them), and
// rows keep the user's id after the connection is gone.
const orphanArchive = `(NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = log_archive.connection)
	OR NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = log_archive."user"))`

const orphanLog = `(NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = log.connection)
	OR NOT EXISTS (SELECT 1 FROM "user" u WHERE u.id = log."user"))`

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
	fs := NewFlags(env, "prune-logs", "prune-logs [--delete] [--batch N]\n\nCounts, or with --delete deletes, log rows and archive rows whose connection or user no\nlonger exists. Deletes in batches, each its own statement, so it never holds one\nhuge transaction; interrupting it keeps the batches already deleted.")
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
		var na int
		if err := env.DB.Pool.QueryRow(ctx, `SELECT count(*) FROM log_archive WHERE `+orphanArchive).Scan(&na); err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%d archive row(s) to delete\n", na)
		return nil
	}
	// find the orphaned (connection, user) pairs once, then delete each
	// pair's rows in batches through the log (connection, ...) index
	rows, err := env.DB.Pool.Query(ctx, `SELECT DISTINCT connection, "user" FROM log WHERE `+orphanLog)
	if err != nil {
		return err
	}
	type pair struct{ conn, user string }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.conn, &p.user); err != nil {
			rows.Close()
			return err
		}
		pairs = append(pairs, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	var total int64
	for _, p := range pairs {
		for {
			if err := ctx.Err(); err != nil {
				fmt.Fprintf(env.Stdout, "deleted %d log row(s) before stopping\n", total)
				return err
			}
			tag, err := env.DB.Pool.Exec(ctx,
				`DELETE FROM log WHERE id IN (SELECT id FROM log WHERE connection = $1 AND "user" = $2 LIMIT $3)
				   AND (NOT EXISTS (SELECT 1 FROM connection WHERE id = $1)
				     OR NOT EXISTS (SELECT 1 FROM "user" WHERE id = $2))`, p.conn, p.user, *batch)
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
