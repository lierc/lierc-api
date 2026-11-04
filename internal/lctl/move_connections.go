package lctl

import (
	"context"
	"fmt"
	"time"
)

const moveConnectionsUsage = `move-connections [--move] [--batch N] [--pause DURATION] [--limit N] FROM TO

Moves every connection on liercd instance FROM (main or next) to TO, as
move-connection does for one, a batch at a time with a pause between batches:
each move reconnects its user, and many at once to one network from one
address can trip its connection throttling. Without --move it only lists what
it would move.

It stops at the first connection that fails to move. Run it again to carry
on: connections already moved aren't on FROM any more.`

func moveConnections(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "move-connections", moveConnectionsUsage)
	move := fs.Bool("move", false, "move them (otherwise only list them)")
	batch := fs.Int("batch", 5, "connections to move before each pause")
	pause := fs.Duration("pause", 30*time.Second, "how long to wait between batches")
	limit := fs.Int("limit", 0, "move at most this many (0: all)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return &UsageError{Msg: "give the instance to move from and the one to move to"}
	}
	from, to := fs.Arg(0), fs.Arg(1)
	for _, inst := range []string{from, to} {
		if inst != "main" && inst != "next" {
			return &UsageError{Msg: fmt.Sprintf("instances are main or next, not %q", inst)}
		}
	}
	switch {
	case from == to:
		return &UsageError{Msg: "the instances must differ"}
	case *batch < 1:
		return &UsageError{Msg: "--batch must be at least 1"}
	case *limit < 0:
		return &UsageError{Msg: "--limit can't be negative"}
	case *pause < 0:
		return &UsageError{Msg: "--pause can't be negative"}
	}

	conns, err := connectionsOn(ctx, env, from)
	if err != nil {
		return err
	}
	out := env.Stdout
	if len(conns) == 0 {
		fmt.Fprintf(out, "no connections on %s\n", from)
		return nil
	}
	todo := conns
	if *limit > 0 && *limit < len(todo) {
		todo = todo[:*limit]
	}

	if !*move {
		if len(todo) < len(conns) {
			fmt.Fprintf(out, "would move %d of %d connection(s) from %s to %s:\n", len(todo), len(conns), from, to)
		} else {
			fmt.Fprintf(out, "would move %d connection(s) from %s to %s:\n", len(todo), from, to)
		}
		for _, c := range todo {
			fmt.Fprintf(out, "  %s (%s)\n", c.id, c.username)
		}
		fmt.Fprintln(out, "run again with --move to move them")
		return nil
	}
	if to == "next" && env.Config.LiercdNextHost == "" {
		return fmt.Errorf("can't move to next: LIERCD_NEXT_HOST is not set")
	}

	for i, c := range todo {
		if i > 0 && i%*batch == 0 {
			fmt.Fprintf(out, "moved %d, pausing %s\n", i, *pause)
			if err := env.Sleep(ctx, *pause); err != nil {
				fmt.Fprintf(out, "stopped after moving %d of %d connection(s)\n", i, len(todo))
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(out, "stopped after moving %d of %d connection(s)\n", i, len(todo))
			return err
		}
		if err := moveOne(ctx, env, c.id, to); err != nil {
			fmt.Fprintf(out, "stopped after moving %d of %d connection(s): %s: %v\n", i, len(todo), c.id, err)
			return err
		}
	}
	if left := len(conns) - len(todo); left > 0 {
		fmt.Fprintf(out, "moved %d connection(s) from %s to %s (%d left on %s)\n", len(todo), from, to, left, from)
	} else {
		fmt.Fprintf(out, "moved %d connection(s) from %s to %s\n", len(todo), from, to)
	}
	return nil
}

type connOwner struct{ id, username string }

// connectionsOn lists the connections on liercd instance inst, by id.
// main is a NULL or empty column.
func connectionsOn(ctx context.Context, env *Env, inst string) ([]connOwner, error) {
	stored := inst
	if inst == "main" {
		stored = ""
	}
	rows, err := env.DB.Pool.Query(ctx, `SELECT c.id, coalesce(u.username, '') FROM connection c
	  LEFT JOIN "user" u ON u.id = c."user"
	  WHERE coalesce(c.liercd, '') = $1
	  ORDER BY c.id`, stored)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var conns []connOwner
	for rows.Next() {
		var c connOwner
		if err := rows.Scan(&c.id, &c.username); err != nil {
			return nil, err
		}
		conns = append(conns, c)
	}
	return conns, rows.Err()
}
