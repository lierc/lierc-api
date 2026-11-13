package lctl

import (
	"context"
	"fmt"
)

func pruneSeen(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "prune-seen", "prune-seen [--delete]\n\nCounts, or with --delete deletes, seen rows whose connection no longer\nexists or that weren't updated in a month.")
	del := fs.Bool("delete", false, "delete the rows")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if !*del {
		n, err := env.Admin.StaleSeen(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "%d seen row(s) to delete; run with --delete to delete them\n", n)
		return nil
	}
	n, err := env.Admin.PruneSeen(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "deleted %d seen row(s)\n", n)
	return nil
}
