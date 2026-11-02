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
login and how many connections they have. Changes nothing. Each line starts
with the id, so the list can be fed to delete-users:

  lctl inactive-users --since 365d | awk '{print $1}' | lctl delete-users

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
	// last_login is a timestamp without a zone, stored in UTC
	rows, err := env.DB.Pool.Query(ctx, `SELECT u.id, u.username, u.email, u.verified, u.last_login,
	    (SELECT count(*) FROM connection c WHERE c."user" = u.id)
	  FROM "user" u WHERE u.last_login < ($1::timestamptz AT TIME ZONE 'UTC')
	  ORDER BY u.last_login, u.id`, cutoff)
	if err != nil {
		return err
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var id, username, email string
		var verified bool
		var lastLogin time.Time
		var conns int
		if err := rows.Scan(&id, &username, &email, &verified, &lastLogin, &conns); err != nil {
			return err
		}
		if !verified {
			email += " (unverified)"
		}
		fmt.Fprintf(env.Stdout, "%s  %s  %s  last login %s UTC (%s)  %d connections\n",
			id, username, email, lastLogin.UTC().Format("2006-01-02 15:04"), ago(env.now(), lastLogin), conns)
		n++
	}
	if err := rows.Err(); err != nil {
		return err
	}
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
