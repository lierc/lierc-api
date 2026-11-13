package lctl

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const inactiveUsersUsage = `inactive-users --since DURATION

Lists users who haven't logged in within DURATION (days such as 90d, or a
Go duration such as 48h), oldest login first: id, username, email, last
login ("never" for an account with none, listed first), when they
registered ("unknown" for accounts from before that was recorded) and
how many connections they have. Changes nothing. Each line starts with
the id, so the list can be fed to delete-users:

  lctl admin inactive-users --since 365d | awk '{print $1}' | lctl admin delete-users

The count goes to stderr.`

func inactiveUsers(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "inactive-users", inactiveUsersUsage)
	since := fs.String("since", "", "how long without a login (90d, 48h)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if *since == "" {
		return &UsageError{Msg: "--since is required, e.g. --since 90d"}
	}
	d, ok := parseSince(*since)
	if !ok {
		return &UsageError{Msg: "--since needs a duration such as 90d or 48h"}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "inactive-users takes no arguments"}
	}

	cutoff := env.now().Add(-d).UTC()
	list, err := env.Admin.Users(ctx, &cutoff)
	if err != nil {
		return err
	}
	for _, u := range list {
		email := u.Email
		if state := verifiedState(u.Verified); state != "verified" {
			email += " (" + state + ")"
		}
		fmt.Fprintf(env.Stdout, "%s  %s  %s  last login %s  registered %s  %d connections\n", u.ID, u.Username, email,
			when(env.now(), u.LastLogin, "never"), when(env.now(), u.Registered, "unknown"), u.Connections)
	}
	n := len(list)
	fmt.Fprintf(env.Stderr, "%d user(s) haven't logged in since %s UTC (%s)\n", n, cutoff.Format("2006-01-02 15:04"), *since)
	return nil
}

// parseSince reads a positive duration: whole days ("90d") or a Go
// duration ("48h", "90m").
func parseSince(s string) (time.Duration, bool) {
	if days, ok := strings.CutSuffix(s, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n <= 0 || n > 100000 {
			return 0, false
		}
		return time.Duration(n) * 24 * time.Hour, true
	}
	d, err := time.ParseDuration(s)
	return d, err == nil && d > 0
}
