package lctl

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const stopInactiveUsage = `stop-inactive --since DURATION [--stop]

Stops the running IRC connections of users who haven't logged in within
DURATION (days such as 90d, or a Go duration such as 720h), including
accounts that never logged in: each is destroyed in liercd, on the
instance it is on. Nothing is deleted: the saved connection stays, and it
starts again when its user's stream next opens. Without --stop it's a dry
run that asks liercd which are running and lists them.

DURATION must be at least 7d: every liercd restart revives the connections
of users active in the last 7 days.

Each running connection gets a line: id, username, host:port, instance. A
connection liercd can't answer for is reported and the rest carry on.`

// stopInactiveFloor is revive's --since in the liercd units: anything
// shorter would be started again by the next restart.
const stopInactiveFloor = 168 * time.Hour

func stopInactive(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "stop-inactive", stopInactiveUsage)
	since := fs.String("since", "", "how long without a login (90d, 720h)")
	stop := fs.Bool("stop", false, "stop them (otherwise only list them)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *since == "" {
		return &UsageError{Msg: "--since is required, e.g. --since 30d"}
	}
	d, ok := parseSince(*since)
	if !ok {
		return &UsageError{Msg: "--since needs a duration such as 30d or 720h"}
	}
	if d < stopInactiveFloor {
		return &UsageError{Msg: "--since must be at least 7d: every liercd restart revives users active in the last 7 days (revive --since 168h)"}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "stop-inactive takes no arguments"}
	}

	cutoff := env.now().Add(-d).UTC()
	// the same users as inactive-users
	list, err := env.Admin.Connections(ctx, "", &cutoff)
	if err != nil {
		return err
	}
	type conn struct{ id, instance, config, user, username string }
	var conns []conn
	for _, c := range list {
		conns = append(conns, conn{c.ID, c.Instance, string(c.Config), c.User, c.Username})
	}

	out := env.Stdout
	stopped, failed := 0, 0
	users := map[string]bool{}
	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		lc, err := env.Liercd.For(c.instance)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", c.id, err)
			failed++
			continue
		}
		method, path := http.MethodGet, c.id+"/status"
		if *stop {
			method, path = http.MethodPost, c.id+"/destroy"
		}
		res, err := lc.Do(ctx, method, path, nil)
		if err != nil {
			fmt.Fprintf(out, "%s: liercd %s isn't reachable: %v\n", c.id, c.instance, err)
			failed++
			continue
		}
		switch res.Status {
		case http.StatusOK:
			fmt.Fprintf(out, "%s  %s  %s  %s\n", c.id, c.username, hostPort(c.config), c.instance)
			stopped++
			users[c.user] = true
		case http.StatusNotFound:
			// not running: nothing to stop
		default:
			fmt.Fprintf(out, "%s: liercd said %s\n", c.id, statusLine(res.Status))
			failed++
		}
	}

	if *stop {
		fmt.Fprintf(out, "stopped %d connection(s) of %d inactive user(s)\n", stopped, len(users))
	} else {
		fmt.Fprintf(out, "would stop %d connection(s) of %d inactive user(s)\n", stopped, len(users))
		if stopped > 0 {
			fmt.Fprintln(out, "run again with --stop to stop them")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d connection(s) couldn't be checked or stopped", failed)
	}
	return nil
}
