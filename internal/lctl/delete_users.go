package lctl

import (
	"context"
	"errors"
	"fmt"
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
sweep removes any log rows that still arrived late.`

// What each table holds for a user, in delete order. A where clause uses
// the user id, their connection ids, or both ($1 user, $2 connections).
var userTables = []struct{ name, where, kind string }{
	{"log", `connection = ANY($1)`, "conns"},
	{"seen", `"user" = $1 OR connection = ANY($2)`, "both"},
	{"private", `connection = ANY($1)`, "conns"},
	{"pref", `"user" = $1`, "user"},
	{"web_push", `"user" = $1`, "user"},
	{"connection", `"user" = $1`, "user"},
	{`"user"`, `id = $1`, "user"},
}

func tableArgs(kind, id string, conns []string) []any {
	switch kind {
	case "conns":
		return []any{conns}
	case "both":
		return []any{id, conns}
	}
	return []any{id}
}

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
	pool := env.DB.Pool

	skipped := 0
	var found []foundUser
	for _, id := range ids {
		u := foundUser{id: id}
		err := pool.QueryRow(ctx, `SELECT username, email FROM "user" WHERE id = $1`, id).Scan(&u.username, &u.email)
		if isNoRows(err) {
			fmt.Fprintf(out, "%s: no such user, skipped\n", id)
			skipped++
			continue
		}
		if err != nil {
			return err
		}
		rows, err := pool.Query(ctx, `SELECT id FROM connection WHERE "user" = $1 ORDER BY id`, id)
		if err != nil {
			return err
		}
		for rows.Next() {
			var c string
			if err := rows.Scan(&c); err != nil {
				rows.Close()
				return err
			}
			u.conns = append(u.conns, c)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if u.conns == nil {
			u.conns = []string{}
		}

		counts := make([]string, len(userTables))
		for i, t := range userTables {
			var n int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+t.name+` WHERE `+t.where, tableArgs(t.kind, id, u.conns)...).Scan(&n); err != nil {
				return err
			}
			counts[i] = fmt.Sprintf("%s=%d", strings.Trim(t.name, `"`), n)
		}
		fmt.Fprintf(out, "%s (%s, %s): %s\n", id, u.username, u.email, strings.Join(counts, " "))
		found = append(found, u)
	}

	if !*del {
		fmt.Fprintln(out, "dry run: nothing deleted; run with --delete to delete")
		return nil
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
	for _, u := range ready {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := deleteUser(ctx, env, u); err != nil {
			fmt.Fprintf(out, "%s: failed, nothing deleted: %v\n", u.id, err)
			skipped++
			continue
		}
		fmt.Fprintf(out, "%s: deleted\n", u.id)
		deleted = append(deleted, u)
	}

	// anything the logger still wrote after the deletes
	for _, u := range deleted {
		tag, err := pool.Exec(ctx, `DELETE FROM log WHERE connection = ANY($1)`, u.conns)
		if err != nil {
			return err
		}
		if n := tag.RowsAffected(); n > 0 {
			fmt.Fprintf(out, "%s: removed %d late log row(s)\n", u.id, n)
		}
	}

	fmt.Fprintf(out, "deleted %d user(s), skipped %d\n", len(deleted), skipped)
	if len(ready) < len(found) || len(deleted) < len(ready) {
		return fmt.Errorf("%d user(s) not deleted", len(found)-len(deleted))
	}
	return nil
}

func deleteUser(ctx context.Context, env *Env, u foundUser) error {
	tx, err := env.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	for _, t := range userTables {
		if _, err := tx.Exec(ctx, `DELETE FROM `+t.name+` WHERE `+t.where, tableArgs(t.kind, u.id, u.conns)...); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
