package lctl

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const connectionListUsage = `connection-list [--instance main|next]

Lists saved connections, one per line: id, liercd instance, owner and the
saved Host:Port. It doesn't ask liercd: connection-info shows a
connection's live state. With --instance, only that liercd's connections.
On a terminal, a table instead. The count goes to stderr.`

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

	rows := make([][]string, len(conns))
	for i, c := range conns {
		user := c.Username
		if user == "" {
			user = c.User + " (no such user)"
		}
		rows[i] = []string{c.ID, c.Instance, user, hostPort(string(c.Config))}
	}
	if env.StdoutIsTerminal {
		if err := writeTable(env.Stdout, []string{"ID", "INSTANCE", "OWNER", "HOST:PORT"}, rows); err != nil {
			return err
		}
	} else {
		for _, r := range rows {
			fmt.Fprintln(env.Stdout, strings.Join(r, "  "))
		}
	}
	fmt.Fprintf(env.Stderr, "%d connection(s)\n", len(conns))
	return nil
}
