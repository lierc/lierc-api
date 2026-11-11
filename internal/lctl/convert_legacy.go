package lctl

import (
	"context"
	"fmt"

	"github.com/lierc/lierc-api/internal/passwords"
)

const convertLegacyUsage = `convert-legacy-passwords [--dry-run]

Rewrites the legacy Perl password hashes (the "password" column, made with
API_SECRET as the salt) into standard $2b$15$ bcrypt strings in
"password_hash", for users who have no password_hash yet. "password" is left
as it is, so setting password_hash back to NULL undoes the conversion. A
user's first login rehashes at cost 12 and clears "password".

Needs the same API_SECRET the Perl API runs with. If it is longer than 16
bytes nothing could have been hashed with it, so the command writes nothing.
A row whose legacy value isn't 46 hex characters is skipped and listed.
--dry-run counts and lists without writing. Hashes are never printed.`

func convertLegacyPasswords(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "convert-legacy-passwords", convertLegacyUsage)
	dry := fs.Bool("dry-run", false, "count and list without writing")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "convert-legacy-passwords takes no arguments"}
	}
	secret := env.Config.Secret
	if len(secret) > 16 {
		return fmt.Errorf("API_SECRET is %d bytes; a legacy hash can't have been made with more than 16, nothing written", len(secret))
	}
	pool := env.DB.Pool

	var already int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM "user" WHERE password_hash IS NOT NULL`).Scan(&already); err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `SELECT id, username, password FROM "user" WHERE password_hash IS NULL AND password IS NOT NULL AND password <> '' ORDER BY id`)
	if err != nil {
		return err
	}
	type todo struct{ id, username, hash string }
	var work []todo
	var skipped []string
	for rows.Next() {
		var id, username, legacy string
		if err := rows.Scan(&id, &username, &legacy); err != nil {
			rows.Close()
			return err
		}
		h, err := passwords.FromLegacy(secret, legacy)
		if err != nil {
			skipped = append(skipped, username)
			continue
		}
		work = append(work, todo{id, username, h})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	converted := len(work)
	if !*dry {
		converted = 0
		for _, w := range work {
			tag, err := pool.Exec(ctx, `UPDATE "user" SET password_hash=$2 WHERE id=$1 AND password_hash IS NULL`, w.id, w.hash)
			if err != nil {
				return err
			}
			converted += int(tag.RowsAffected())
		}
	}
	out := env.Stdout
	fmt.Fprintf(out, "converted %d, skipped %d (malformed), already done %d\n", converted, len(skipped), already)
	for _, u := range skipped {
		fmt.Fprintf(out, "%s\n", u)
	}
	if *dry {
		fmt.Fprintln(out, "dry run: nothing written")
	}
	return nil
}
