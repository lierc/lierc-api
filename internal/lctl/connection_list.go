package lctl

import (
	"context"
	"fmt"
	"sort"
)

const connectionListUsage = `connection-list [--instance main|next]

Lists saved connections, one per line: id, liercd instance, owner and the
saved Host:Port. It doesn't ask liercd: connection-info shows a
connection's live state. With --instance, only that liercd's connections.
The count goes to stderr.`

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

	conns, err := env.Admin.Connections(ctx, *instance, nil)
	if err != nil {
		return err
	}
	// the API lists by owner's last login; this lists by id
	sort.SliceStable(conns, func(i, j int) bool { return conns[i].ID < conns[j].ID })

	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		user := c.Username
		if user == "" {
			user = c.User + " (no such user)"
		}
		fmt.Fprintf(env.Stdout, "%s  %s  %s  %s\n", c.ID, c.Instance, user, hostPort(string(c.Config)))
	}
	fmt.Fprintf(env.Stderr, "%d connection(s)\n", len(conns))
	return nil
}
