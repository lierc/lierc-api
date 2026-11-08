package lctl

import (
	"context"
	"fmt"
)

const staleSeen = `NOT EXISTS (SELECT 1 FROM connection c WHERE c.id = seen.connection)
	OR updated < now() - interval '1 month'`

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
