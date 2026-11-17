package lctl

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

const userListUsage = `user-list

Lists every user, one per line, by username: id, username, email (with
"unverified" or "grandfathered" unless verified), last login ("never" for
an account with none), when they registered ("unknown" for accounts from
before that was recorded) and how many connections they have. The same
lines as inactive-users, so each starts with the id. The count goes to
stderr.`

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
	for _, u := range list {
		printUser(env, u)
	}
	fmt.Fprintf(env.Stderr, "%d user(s)\n", len(list))
	return nil
}
