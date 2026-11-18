package lctl

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/lierc/lierc-api/internal/admin"
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
	stopped, users, failed, err := stopConnections(ctx, env, list, *stop)
	if err != nil {
		return err
	}
	out := env.Stdout
	if *stop {
		fmt.Fprintf(out, "stopped %d connection(s) of %d inactive user(s)\n", stopped, users)
	} else {
		fmt.Fprintf(out, "would stop %d connection(s) of %d inactive user(s)\n", stopped, users)
		if stopped > 0 {
			fmt.Fprintln(out, "run again with --stop to stop them")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d connection(s) couldn't be checked or stopped", failed)
	}
	return nil
}

// stopConnections destroys each connection in list that liercd is running,
// on its instance, printing a line for each (id, username, host:port,
// instance); with stop false it only asks liercd which are running. It
// returns how many were (or would be) stopped, of how many users, and how
// many liercd couldn't answer for, which are reported as it goes. Its error
// is only the context's.
func stopConnections(ctx context.Context, env *Env, list []admin.Connection, stop bool) (stopped, users, failed int, err error) {
	out := env.Stdout
	owners := map[string]bool{}
	for _, c := range list {
		if err := ctx.Err(); err != nil {
			return stopped, len(owners), failed, err
		}
		lc, err := env.Liercd.For(c.Instance)
		if err != nil {
			fmt.Fprintf(out, "%s: %v\n", c.ID, err)
			failed++
			continue
		}
		method, path := http.MethodGet, c.ID+"/status"
		if stop {
			method, path = http.MethodPost, c.ID+"/destroy"
		}
		res, err := lc.Do(ctx, method, path, nil)
		if err != nil {
			fmt.Fprintf(out, "%s: liercd %s isn't reachable: %v\n", c.ID, c.Instance, err)
			failed++
			continue
		}
		switch res.Status {
		case http.StatusOK:
			fmt.Fprintf(out, "%s  %s  %s  %s\n", c.ID, c.Username, hostPort(string(c.Config)), c.Instance)
			stopped++
			owners[c.User] = true
		case http.StatusNotFound:
			// not running: nothing to stop
		default:
			fmt.Fprintf(out, "%s: liercd said %s\n", c.ID, statusLine(res.Status))
			failed++
		}
	}
	return stopped, len(owners), failed, nil
}
