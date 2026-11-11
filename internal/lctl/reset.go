package lctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/passwords"
)

const resetUsage = `reset EMAIL

Sets the password of the user with this email (matched case-insensitively).
On a terminal it prompts for the password without echo; otherwise it reads
the first line of stdin. The password isn't taken as an argument, so it
stays out of shell history and the process list.`

func reset(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "reset", resetUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return &UsageError{Msg: "give one email address"}
	}
	email := fs.Arg(0)

	var pw string
	if env.StdinIsTerminal {
		p, err := env.ReadPassword()
		if err != nil {
			return err
		}
		pw = p
	} else {
		line, err := bufio.NewReader(env.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return errors.New("password is empty")
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if pw == "" {
		return errors.New("password is empty")
	}

	hash, err := passwords.Hash(pw)
	if err != nil {
		return err
	}
	tx, err := env.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	rows, err := tx.Query(ctx,
		`UPDATE "user" SET password_hash = $1, password = NULL WHERE lower(email) = lower($2) RETURNING id`, hash, email)
	if err != nil {
		return err
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return fmt.Errorf("no user with email %s", email)
	}
	// a new password ends the logins made with the old one
	var ended int64
	for _, id := range ids {
		n, err := login.DeleteAll(ctx, tx, id)
		if err != nil {
			return err
		}
		ended += n
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "password set for %s\n", email)
	noun := "logins"
	if ended == 1 {
		noun = "login"
	}
	fmt.Fprintf(env.Stdout, "ended %d %s\n", ended, noun)
	return nil
}
