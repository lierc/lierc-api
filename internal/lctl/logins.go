package lctl

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/lierc/lierc-api/internal/login"
)

const loginsUsage = `logins USER

Lists a user's logins (by id, username or email; names and emails match
case-insensitively), most recently used first, one per line: id, when it
started, when it was last used (UTC), IP address and browser. Expired
logins aren't listed.`

const endLoginUsage = `end-login USER ID
end-login USER --all

Ends one of a user's logins by the id logins shows, or every login of the
user with --all. The user is named by id, username or email.`

const pruneLoginsUsage = `prune-logins

Deletes logins that haven't been used for 7 days. They're already refused;
this only removes the rows.`

// findUser returns the id of the user named who (id, username or email,
// case-insensitively). Without one it prints "no such user" and returns ok
// false.
func findUser(ctx context.Context, env *Env, who string) (id string, ok bool, err error) {
	err = env.DB.Pool.QueryRow(ctx, `SELECT id FROM "user"
	  WHERE id = $1 OR lower(username) = lower($1) OR lower(email) = lower($1)
	  ORDER BY id = $1 DESC LIMIT 1`, who).Scan(&id)
	if isNoRows(err) {
		fmt.Fprintf(env.Stdout, "%s: no such user\n", who)
		return "", false, nil
	}
	return id, err == nil, err
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func logins(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "logins", loginsUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return &UsageError{Msg: "give a user id, username or email"}
	}
	who := fs.Arg(0)
	id, ok, err := findUser(ctx, env, who)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: no such user", who)
	}
	list, err := login.List(ctx, env.DB, id)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		fmt.Fprintln(env.Stdout, "no logins")
		return nil
	}
	for _, l := range list {
		ip := l.IP
		if ip == "" {
			ip = "-"
		}
		fmt.Fprintf(env.Stdout, "%d  started %s  last used %s  %s  %q\n", l.ID,
			l.Created.UTC().Format(time.RFC3339), l.LastUsed.UTC().Format(time.RFC3339), ip, l.UserAgent)
	}
	return nil
}

func endLogin(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "end-login", endLoginUsage)
	all := fs.Bool("all", false, "end every login of the user")
	if err := Parse(fs, args); err != nil {
		return err
	}
	rest := fs.Args()
	// flags may follow the user, as in "end-login ann --all"
	if len(rest) > 1 {
		if err := Parse(fs, rest[1:]); err != nil {
			return err
		}
		rest = append(rest[:1], fs.Args()...)
	}
	if len(rest) == 0 {
		return &UsageError{Msg: "give a user"}
	}
	if *all != (len(rest) == 1) || len(rest) > 2 {
		return &UsageError{Msg: "give a login id or --all, not both"}
	}
	var loginID int64
	if !*all {
		n, err := strconv.ParseInt(rest[1], 10, 64)
		if err != nil {
			return &UsageError{Msg: "the login id is a number, as logins shows"}
		}
		loginID = n
	}
	who := rest[0]
	id, ok, err := findUser(ctx, env, who)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s: no such user", who)
	}
	if *all {
		n, err := login.DeleteAll(ctx, env.DB.Pool, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(env.Stdout, "ended %s\n", plural(n, "login", "logins"))
		return nil
	}
	deleted, err := login.Delete(ctx, env.DB.Pool, id, loginID)
	if err != nil {
		return err
	}
	if !deleted {
		fmt.Fprintln(env.Stdout, "no such login")
		return fmt.Errorf("no such login")
	}
	fmt.Fprintln(env.Stdout, "ended 1 login")
	return nil
}

func pruneLogins(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "prune-logins", pruneLoginsUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "prune-logins takes no arguments"}
	}
	n, err := login.Prune(ctx, env.DB)
	if err != nil {
		return err
	}
	noun := "logins"
	if n == 1 {
		noun = "login"
	}
	fmt.Fprintf(env.Stdout, "deleted %d expired %s\n", n, noun)
	return nil
}
