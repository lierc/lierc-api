package lctl

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

const deleteConnectionsUsage = `delete-connections [--delete] [--settle SECONDS] ID...

Deletes IRC connections and their data (log, seen and private rows), given
connection ids as arguments or one per line on stdin. The users are kept.
Without --delete it's a dry run: it prints what each connection has and
changes nothing.

Each connection is destroyed in liercd first, so it stops running. If
liercd can't be reached or refuses (anything but 200, or 404 for a
connection that isn't running), that connection is skipped. Destroying
makes it QUIT, and that message reaches the log through NSQ a moment
later, so the command waits --settle seconds, then deletes each
connection's rows in one transaction, and finally sweeps log rows that
arrived late.`

func deleteConnections(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "delete-connections", deleteConnectionsUsage)
	del := fs.Bool("delete", false, "delete the connections")
	settle := fs.Int("settle", 5, "seconds to wait for QUIT messages to reach the log before deleting")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *settle < 0 {
		return &UsageError{Msg: "--settle needs a number of seconds"}
	}
	ids, err := ReadIDs(env, fs.Args())
	if err != nil && !errors.Is(err, ErrNoIDsOnStdin) {
		return err
	}
	if len(ids) == 0 {
		return &UsageError{Msg: "give connection ids as arguments or on stdin"}
	}
	out := env.Stdout
	pool := env.pool()

	var found []string
	for _, id := range ids {
		var user, config string
		var username *string
		err := pool.QueryRow(ctx, `SELECT c."user", c.config::text, u.username FROM connection c
		  LEFT JOIN "user" u ON u.id = c."user" WHERE c.id = $1`, id).Scan(&user, &config, &username)
		if isNoRows(err) {
			fmt.Fprintf(out, "%s: no such connection\n", id)
			continue
		}
		if err != nil {
			return err
		}
		counts := make([]string, len(connectionTables))
		for i, t := range connectionTables {
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+t+` WHERE `+connectionWhere(t), id).Scan(&n); err != nil {
				return err
			}
			counts[i] = fmt.Sprintf("%s=%d", t, n)
		}
		name := "no such user"
		if username != nil {
			name = *username
		}
		fmt.Fprintf(out, "%s (user %s, %s, %s): %s\n", id, user, name, hostPort(config), strings.Join(counts, " "))
		found = append(found, id)
	}

	if !*del {
		fmt.Fprintln(out, "dry run: nothing deleted; run with --delete to delete")
		return nil
	}
	if len(found) == 0 {
		return nil
	}

	deleted, skipped, err := destroyAndDelete(ctx, env, found, time.Duration(*settle)*time.Second,
		connectionTables, connectionWhere)
	fmt.Fprintf(out, "deleted %d connection(s), skipped %d\n", deleted, skipped)
	if err != nil {
		return err
	}
	if skipped > 0 {
		return fmt.Errorf("%d connection(s) skipped", skipped)
	}
	return nil
}
