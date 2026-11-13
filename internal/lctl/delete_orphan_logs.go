package lctl

import (
	"context"
	"fmt"
)

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
	conns, err := env.Store.GoneConnections(ctx)
	if err != nil {
		return err
	}
	users, err := env.Store.GoneUsers(ctx)
	if err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "%d gone connection(s) and %d gone user(s) still in log\n", len(conns), len(users))
	if !*del {
		// no row counts: counting them reads every orphan row, which is
		// most of the work of deleting them
		fmt.Fprintf(env.Stdout, "run with --delete to delete their log rows\n")
		na, err := env.Store.CountOrphanArchive(ctx)
		if err != nil {
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
	run := func(kind, id string, del func(context.Context, string, int) (int64, error)) error {
		n, err := deleteBatches(ctx, *batch, del, id, func(n int64) {
			fmt.Fprintf(env.Stdout, "%s %s: %d row(s) so far\n", kind, id, n)
		})
		total += n
		if n > 0 {
			fmt.Fprintf(env.Stdout, "%s %s: deleted %d row(s) (%d so far)\n", kind, id, n, total)
		}
		return err
	}
	for _, c := range conns {
		if err := run("connection", c, env.Store.DeleteGoneConnectionLogs); err != nil {
			return stopped(err)
		}
	}
	for _, u := range users {
		if err := run("user", u, env.Store.DeleteGoneUserLogs); err != nil {
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
		n, err := env.Store.DeleteOrphanArchive(ctx, *batch)
		if err != nil {
			return err
		}
		archived += n
		if n < int64(*batch) {
			break
		}
	}
	fmt.Fprintf(env.Stdout, "deleted %d archive row(s)\n", archived)
	return nil
}

// orphanProgressEvery is how many of one id's rows delete-orphan-logs
// deletes between progress lines.
var orphanProgressEvery int64 = 100000

// deleteBatches runs del, a delete of up to batch rows of id, until a run deletes
// fewer than batch rows, and returns how many it deleted. Each time the
// count passes a multiple of orphanProgressEvery it calls progress with it.
// It stops between batches when ctx is done.
func deleteBatches(ctx context.Context, batch int, del func(context.Context, string, int) (int64, error), id string, progress func(int64)) (int64, error) {
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, err := del(ctx, id, batch)
		if err != nil {
			return total, err
		}
		before := total
		total += n
		if total/orphanProgressEvery > before/orphanProgressEvery {
			progress(total)
		}
		if n < int64(batch) {
			return total, nil
		}
	}
}
