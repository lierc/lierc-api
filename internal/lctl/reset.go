package lctl

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/bcrypt"
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

	hash, err := bcrypt.GenerateFromPassword([]byte(pw), 12)
	if err != nil {
		return err
	}
	tag, err := env.DB.Pool.Exec(ctx,
		`UPDATE "user" SET password_hash = $1, password = NULL WHERE lower(email) = lower($2)`, string(hash), email)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("no user with email %s", email)
	}
	fmt.Fprintf(env.Stdout, "password set for %s\n", email)
	return nil
}
