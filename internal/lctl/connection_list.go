package lctl

import (
	"context"
	"fmt"
)

const connectionListUsage = `connection-list [--instance main|next]

Lists saved connections, one per line: id, liercd instance, owner, the
saved Host:Port, and its live state in liercd (connected as NICK, running
but not connected, or not running). With --instance, only that liercd's
connections. The count goes to stderr.`

func connectionList(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "connection-list", connectionListUsage)
	instance := fs.String("instance", "", "only this liercd's connections: main or next")
	if err := Parse(fs, args); err != nil {
		return err
	}
	switch {
	case fs.NArg() != 0:
		return &UsageError{Msg: "connection-list takes no arguments"}
	case *instance != "" && *instance != "main" && *instance != "next":
		return &UsageError{Msg: fmt.Sprintf("instances are main or next, not %q", *instance)}
	}

	// main is a NULL or empty column; "" for --instance means every one
	stored := *instance
	if stored == "main" {
		stored = ""
	}
	rows, err := env.DB.Pool.Query(ctx, `SELECT c.id, coalesce(nullif(c.liercd, ''), 'main'),
	    c."user", coalesce(u.username, ''), c.config::text
	  FROM connection c LEFT JOIN "user" u ON u.id = c."user"
	  WHERE ($1 = '' OR coalesce(c.liercd, '') = $2)
	  ORDER BY c.id`, *instance, stored)
	if err != nil {
		return err
	}
	type conn struct{ id, instance, userID, username, config string }
	var conns []conn
	for rows.Next() {
		var c conn
		if err := rows.Scan(&c.id, &c.instance, &c.userID, &c.username, &c.config); err != nil {
			rows.Close()
			return err
		}
		conns = append(conns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		user := c.username
		if user == "" {
			user = c.userID + " (no such user)"
		}
		fmt.Fprintf(env.Stdout, "%s  %s  %s  %s  %s\n",
			c.id, c.instance, user, hostPort(c.config), liercdState(ctx, env, c.id))
	}
	fmt.Fprintf(env.Stderr, "%d connection(s)\n", len(conns))
	return nil
}
