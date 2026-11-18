package lctl

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

const stopUnverifiedUsage = `stop-unverified --since DURATION [--stop]

Stops the running IRC connections of users who haven't confirmed their
email address and haven't had a stream open within DURATION (a Go duration
such as 3h, or days such as 2d): each is destroyed in liercd, on the
instance it is on. A user with a stream open now is left alone, however
long ago it opened. When a user last had a stream is their last login,
which a stream records as it opens and as it closes, or when they
registered if they never had one. Users from before email confirmation
(verified unset) are never stopped. Nothing is deleted: the saved
connection stays, and it starts again when its user's stream next opens.
Without --stop it's a dry run that asks liercd which are running and lists
them.

A liercd restart revives the connections of every user active in the
last few days, these included; the next run stops them again.

Each running connection gets a line: id, username, host:port, instance. A
connection liercd can't answer for is reported and the rest carry on.`

func stopUnverified(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "stop-unverified", stopUnverifiedUsage)
	since := fs.String("since", "", "how long without a stream (3h, 2d)")
	stop := fs.Bool("stop", false, "stop them (otherwise only list them)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *since == "" {
		return &UsageError{Msg: "--since is required, e.g. --since 3h"}
	}
	d, ok := parseSince(*since)
	if !ok {
		return &UsageError{Msg: "--since needs a duration such as 3h or 2d"}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "stop-unverified takes no arguments"}
	}

	list, err := env.Admin.UnverifiedIdleConnections(ctx, env.now().Add(-d).UTC())
	if err != nil {
		return err
	}
	stopped, users, failed, err := stopConnections(ctx, env, list, *stop)
	if err != nil {
		return err
	}
	out := env.Stdout
	if *stop {
		fmt.Fprintf(out, "stopped %d connection(s) of %d unverified user(s)\n", stopped, users)
	} else {
		fmt.Fprintf(out, "would stop %d connection(s) of %d unverified user(s)\n", stopped, users)
		if stopped > 0 {
			fmt.Fprintln(out, "run again with --stop to stop them")
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d connection(s) couldn't be checked or stopped", failed)
	}
	return nil
}

const deleteUnverifiedUsage = `delete-unverified --since DURATION [--delete] [--settle SECONDS]

Deletes users who haven't confirmed their email address and registered
more than DURATION ago (days such as 3d, or a Go duration such as 96h; at
least 1d), with all their data, as delete-users does: their connections
are stopped first, and each user is deleted in one transaction. Users from
before email confirmation (verified unset) are never deleted. Without
--delete it's a dry run that prints what each has and changes nothing.`

// deleteUnverifiedFloor keeps a typo (3h for 3d) from deleting signups
// that are still confirming.
const deleteUnverifiedFloor = 24 * time.Hour

func deleteUnverified(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "delete-unverified", deleteUnverifiedUsage)
	since := fs.String("since", "", "how long ago they registered (3d, 96h)")
	del := fs.Bool("delete", false, "delete the users")
	settle := fs.Int("settle", 5, "seconds to wait for QUIT messages to reach the log before deleting")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *since == "" {
		return &UsageError{Msg: "--since is required, e.g. --since 3d"}
	}
	d, ok := parseSince(*since)
	if !ok {
		return &UsageError{Msg: "--since needs a duration such as 3d or 96h"}
	}
	if d < deleteUnverifiedFloor {
		return &UsageError{Msg: "--since must be at least 1d"}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "delete-unverified takes no arguments"}
	}

	cutoff := env.now().Add(-d).UTC()
	list, err := env.Admin.UnverifiedUsers(ctx, cutoff)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintf(env.Stdout, "no unverified users registered before %s UTC (%s)\n", cutoff.Format("2006-01-02 15:04"), *since)
		return nil
	}
	rest := []string{"--settle", strconv.Itoa(*settle)}
	if *del {
		rest = append(rest, "--delete")
	}
	for _, u := range list {
		rest = append(rest, u.ID)
	}
	return deleteUsers(ctx, env, rest)
}
