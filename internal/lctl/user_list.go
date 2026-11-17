package lctl

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

const userListUsage = `user-list

Lists every user, one per line, by username: id, username, email (with
"unverified" or "grandfathered" unless verified), last login ("never" for
an account with none), when they registered ("unknown" for accounts from
before that was recorded) and how many connections they have. The same
lines as inactive-users, so each starts with the id; on a terminal, a
table instead. The count goes to stderr.`

func userList(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "user-list", userListUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "user-list takes no arguments"}
	}
	list, err := env.Admin.Users(ctx, nil)
	if err != nil {
		return err
	}
	// the API lists by last login; this lists by username
	sort.SliceStable(list, func(i, j int) bool {
		return strings.ToLower(list[i].Username) < strings.ToLower(list[j].Username)
	})
	if env.StdoutIsTerminal {
		rows := make([][]string, len(list))
		for i, u := range list {
			rows[i] = []string{u.ID, u.Username, u.Email, verifiedState(u.Verified),
				day(env.now(), u.LastLogin, "never"), day(env.now(), u.Registered, "unknown"), strconv.Itoa(u.Connections)}
		}
		if err := writeTable(env.Stdout, []string{"ID", "USERNAME", "EMAIL", "VERIFIED", "LAST LOGIN", "REGISTERED", "CONNECTIONS"}, rows); err != nil {
			return err
		}
	} else {
		for _, u := range list {
			printUser(env, u)
		}
	}
	fmt.Fprintf(env.Stderr, "%d user(s)\n", len(list))
	return nil
}

// day is t's date and how long ago it was, or none if t is nil: when,
// shortened for a table.
func day(now time.Time, t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return fmt.Sprintf("%s (%s)", t.UTC().Format("2006-01-02"), ago(now, *t))
}
