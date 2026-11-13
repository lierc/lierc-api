package lctl

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

const deleteUsersUsage = `delete-users [--delete] [--settle SECONDS] USER_ID...

Deletes users and all their data, given user ids as arguments or one per
line on stdin. Without --delete it's a dry run: it prints what each user has
and changes nothing.

Every listed user's IRC connections are destroyed in liercd first, so they
stop running. If liercd can't be reached or refuses (anything but 200, or
404 for a connection that isn't running), that user is skipped and nothing
of theirs is deleted. Destroying a connection makes it QUIT, and that
message reaches the log through NSQ a moment later, so the command waits
--settle seconds before deleting. Each user's rows are then deleted in one
transaction: a user is either fully deleted or left as they were. A last
sweep removes any log rows that still arrived late (it looks only at rows
written after the command started, so it takes moments).`

// userTables are the tables a user's dry run lists, in this order.
var userTables = []string{"log", "seen", "private", "pref", "web_push", "connection", "user"}

type foundUser struct {
	id, username, email string
	conns               []string
}

func deleteUsers(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "delete-users", deleteUsersUsage)
	del := fs.Bool("delete", false, "delete the users")
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
		return &UsageError{Msg: "give user ids as arguments or on stdin"}
	}
	out := env.Stdout

	skipped := 0
	var found []foundUser
	for _, id := range ids {
		d, err := env.Admin.User(ctx, id)
		if errors.Is(err, ErrNotFound) {
			fmt.Fprintf(out, "%s: no such user, skipped\n", id)
			skipped++
			continue
		}
		if err != nil {
			return err
		}
		u := foundUser{id: id, username: d.Username, email: d.Email, conns: []string{}}
		for _, c := range d.ConnectionList {
			u.conns = append(u.conns, c.ID)
		}
		sort.Strings(u.conns)

		have, err := env.Admin.UserCounts(ctx, id)
		if err != nil {
			return err
		}
		have["user"] = 1
		counts := make([]string, len(userTables))
		for i, t := range userTables {
			counts[i] = fmt.Sprintf("%s=%d", t, have[t])
		}
		fmt.Fprintf(out, "%s (%s, %s): %s\n", id, u.username, u.email, strings.Join(counts, " "))
		found = append(found, u)
	}

	if !*del {
		fmt.Fprintln(out, "dry run: nothing deleted; run with --delete to delete")
		return nil
	}

	// log rows from here on are new: the API's last sweep looks only at
	// those, not at each user's whole (just deleted) history. This is also
	// the first call that must reach the admin server, before liercd is touched.
	logAfter, err := env.Admin.LastLogID(ctx)
	if err != nil {
		return err
	}

	// stop every connection first, so nothing keeps running for these users
	var ready []foundUser
	wait := false
	for _, u := range found {
		ok := true
		for _, c := range u.conns {
			if problem := destroy(ctx, env, c); problem != "" {
				fmt.Fprintf(out, "%s: connection %s: liercd said %s, skipping this user\n", u.id, c, problem)
				ok = false
				break
			}
		}
		if !ok {
			skipped++
			continue
		}
		ready = append(ready, u)
		wait = wait || len(u.conns) > 0
	}

	// let the connections' QUIT messages reach the log before deleting it
	if wait {
		if err := env.Sleep(ctx, time.Duration(*settle)*time.Second); err != nil {
			return err
		}
	}

	var deleted []foundUser
	late := map[string]int64{}
	for _, u := range ready {
		if err := ctx.Err(); err != nil {
			return err
		}
		// what the log holds now, so the sweep's rows can be told apart
		var logBefore int64
		if c, err := env.Admin.UserCounts(ctx, u.id); err == nil {
			logBefore = c["log"]
		}
		counts, err := env.Admin.DeleteUser(ctx, u.id, logAfter)
		if errors.Is(err, ErrMaybeDone) {
			return err
		}
		if err != nil {
			fmt.Fprintf(out, "%s: failed, nothing deleted: %v\n", u.id, err)
			skipped++
			continue
		}
		late[u.id] = lateRows(logBefore, counts["log"])
		fmt.Fprintf(out, "%s: deleted\n", u.id)
		deleted = append(deleted, u)
	}

	for _, u := range deleted {
		if n := late[u.id]; n > 0 {
			fmt.Fprintf(out, "%s: removed %d late log row(s)\n", u.id, n)
		}
	}

	fmt.Fprintf(out, "deleted %d user(s), skipped %d\n", len(deleted), skipped)
	if len(ready) < len(found) || len(deleted) < len(ready) {
		return fmt.Errorf("%d user(s) not deleted", len(found)-len(deleted))
	}
	return nil
}
