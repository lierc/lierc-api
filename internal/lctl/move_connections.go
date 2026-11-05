package lctl

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const moveConnectionsUsage = `move-connections [--move] [--dormant] [--batch N] [--pause DURATION] [--limit N] FROM TO

Moves every connection on liercd instance FROM (main or next) to TO, as
move-connection does for one, a batch at a time with a pause between batches:
each move reconnects its user, and many at once to one network from one
address can trip its connection throttling. Connections that aren't running
stay dormant, so they don't count toward a batch. With --dormant it moves
only those, asking FROM which are running and leaving those where they are.
Without --move it only lists what it would move.

It stops at the first connection that fails to move. Run it again to carry
on: connections already moved aren't on FROM any more.`

func moveConnections(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "move-connections", moveConnectionsUsage)
	move := fs.Bool("move", false, "move them (otherwise only list them)")
	dormantOnly := fs.Bool("dormant", false, "move only connections that aren't running")
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
	kind, aside, running := "", "", 0
	if *dormantOnly {
		if conns, running, err = dormantOn(ctx, env, from, conns); err != nil {
			return err
		}
		kind, aside = "dormant ", fmt.Sprintf(" (%d running left alone)", running)
		if len(conns) == 0 {
			fmt.Fprintf(out, "no dormant connections on %s%s\n", from, aside)
			return nil
		}
	}
	todo := conns
	if *limit > 0 && *limit < len(todo) {
		todo = todo[:*limit]
	}

	if !*move {
		if len(todo) < len(conns) {
			fmt.Fprintf(out, "would move %d of %d %sconnection(s) from %s to %s%s:\n", len(todo), len(conns), kind, from, to, aside)
		} else {
			fmt.Fprintf(out, "would move %d %sconnection(s) from %s to %s%s:\n", len(todo), kind, from, to, aside)
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

	started := 0
	for i, c := range todo {
		if err := ctx.Err(); err != nil {
			fmt.Fprintf(out, "stopped after moving %d of %d connection(s)\n", i, len(todo))
			return err
		}
		s, err := moveOne(ctx, env, c.id, to)
		if err != nil {
			fmt.Fprintf(out, "stopped after moving %d of %d connection(s): %s: %v\n", i, len(todo), c.id, err)
			return err
		}
		if !s {
			continue
		}
		// pace only the connections that were started: a dormant move
		// connects nothing (with --dormant, one is started only if its
		// user's stream opened since it was found dormant)
		if started++; started%*batch == 0 && i < len(todo)-1 {
			fmt.Fprintf(out, "started %d, pausing %s\n", started, *pause)
			if err := env.Sleep(ctx, *pause); err != nil {
				fmt.Fprintf(out, "stopped after moving %d of %d connection(s)\n", i+1, len(todo))
				return err
			}
		}
	}
	if left := len(conns) - len(todo); left > 0 {
		fmt.Fprintf(out, "moved %d connection(s) from %s to %s (%d %sleft on %s)\n", len(todo), from, to, left, kind, from)
	} else {
		fmt.Fprintf(out, "moved %d connection(s) from %s to %s\n", len(todo), from, to)
	}
	if running > 0 {
		fmt.Fprintf(out, "left %d running connection(s) on %s\n", running, from)
	}
	if dormant := len(todo) - started; dormant > 0 {
		fmt.Fprintf(out, "started %d on %s; %d weren't running and will start when their users' streams next open\n", started, to, dormant)
	}
	return nil
}

type connOwner struct{ id, username string }

// dormantOn returns the connections in conns that aren't running on
// liercd instance inst, and how many are.
func dormantOn(ctx context.Context, env *Env, inst string, conns []connOwner) ([]connOwner, int, error) {
	c, err := env.Liercd.For(inst)
	if err != nil {
		return nil, 0, err
	}
	var dormant []connOwner
	running := 0
	for _, conn := range conns {
		res, err := c.Do(ctx, http.MethodGet, conn.id+"/status", nil)
		if err != nil {
			return nil, 0, fmt.Errorf("asking %s whether %s is running: %v", inst, conn.id, err)
		}
		switch res.Status {
		case http.StatusOK:
			running++
		case http.StatusNotFound:
			dormant = append(dormant, conn)
		default:
			return nil, 0, fmt.Errorf("asking %s whether %s is running: liercd said %s", inst, conn.id, statusLine(res.Status))
		}
	}
	return dormant, running, nil
}

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
