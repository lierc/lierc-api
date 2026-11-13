package db

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// The user table's account columns: signup, login, email confirmation and
// password reset.

// Account is a user as GET /auth shows it. Verified is nil for an account
// from before signups had to confirm their email.
type Account struct {
	ID, Username, Email string
	Verified            *bool
}

// AccountAuth is an Account with its stored bcrypt password hash, nil for
// an account with no password (one that has to reset it).
type AccountAuth struct {
	Account
	PasswordHash *string
}

// AccountByID finds the user with id.
func (d *DB) AccountByID(ctx context.Context, id string) (Account, bool, error) {
	var a Account
	err := d.Pool.QueryRow(ctx, `SELECT id, username, email, verified FROM "user" WHERE id=$1`, id).
		Scan(&a.ID, &a.Username, &a.Email, &a.Verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, false, nil
	}
	return a, err == nil, err
}

// AccountsByLogin finds the users whose email or username is name, ignoring
// case.
func (d *DB) AccountsByLogin(ctx context.Context, name string) ([]AccountAuth, error) {
	rows, err := d.Pool.Query(ctx,
		`SELECT id, username, email, verified, password_hash FROM "user" WHERE lower(email) = lower($1) OR lower(username) = lower($1)`,
		name)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AccountAuth
	for rows.Next() {
		var r AccountAuth
		if err := rows.Scan(&r.ID, &r.Username, &r.Email, &r.Verified, &r.PasswordHash); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// AccountAuthByEmail finds the user whose email is email, ignoring case.
func (d *DB) AccountAuthByEmail(ctx context.Context, email string) (AccountAuth, bool, error) {
	var a AccountAuth
	err := d.Pool.QueryRow(ctx,
		`SELECT id, username, email, password_hash FROM "user" WHERE lower(email) = lower($1)`, email).
		Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountAuth{}, false, nil
	}
	return a, err == nil, err
}

// AccountAuthByID finds the user with id. Verified is not read.
func (d *DB) AccountAuthByID(ctx context.Context, id string) (AccountAuth, bool, error) {
	var a AccountAuth
	err := d.Pool.QueryRow(ctx, `SELECT id, username, email, password_hash FROM "user" WHERE id=$1`, id).
		Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountAuth{}, false, nil
	}
	return a, err == nil, err
}

// InsertUser adds an unverified user whose first confirmation email is
// being sent (verify_sent). A taken username or email is a Postgres unique
// violation, 23505.
func (d *DB) InsertUser(ctx context.Context, id, username, email, hash string) error {
	_, err := d.Pool.Exec(ctx,
		`INSERT INTO "user" (id, username, email, password_hash, verify_sent, registered) VALUES ($1, $2, $3, $4, now(), now())`,
		id, username, email, hash)
	return err
}

// UserHasConnection reports whether user has any connection.
func (d *DB) UserHasConnection(ctx context.Context, user string) (bool, error) {
	var has int
	err := d.Pool.QueryRow(ctx, `SELECT 1 FROM connection WHERE "user"=$1 LIMIT 1`, user).Scan(&has)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// ClaimVerifyResend records that a confirmation email is being sent to
// user now, if it is unverified and none was sent within the last
// minSeconds; it reports whether it was claimed. The update both checks and
// records, so two at once claim once.
func (d *DB) ClaimVerifyResend(ctx context.Context, id string, minSeconds float64) (bool, error) {
	tag, err := d.Pool.Exec(ctx,
		`UPDATE "user" SET verify_sent = now()
		 WHERE id = $1 AND verified = false
		   AND (verify_sent IS NULL OR verify_sent < now() - make_interval(secs => $2))`,
		id, minSeconds)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// Execer is a pgx.Tx or a pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

func replacePasswordHash(ctx context.Context, q Execer, id, hash string, oldHash *string) (int64, error) {
	tag, err := q.Exec(ctx,
		`UPDATE "user" SET password_hash=$2 WHERE id=$1 AND password_hash IS NOT DISTINCT FROM $3`,
		id, hash, oldHash)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func markVerified(ctx context.Context, q Execer, id string) (bool, error) {
	tag, err := q.Exec(ctx, `UPDATE "user" SET verified=true WHERE id=$1 AND verified=false`, id)
	if err != nil {
		return false, err
	}
	if _, err := q.Exec(ctx, `UPDATE "user" SET verified=true WHERE id=$1 AND verified IS NULL`, id); err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ReplacePasswordHash saves hash only if the stored one is still oldHash
// (nil: none). It reports whether it changed the user.
func (d *DB) ReplacePasswordHash(ctx context.Context, id, hash string, oldHash *string) (bool, error) {
	n, err := replacePasswordHash(ctx, d.Pool, id, hash, oldHash)
	return n > 0, err
}

// MarkVerified sets a user verified, and reports whether this is a signup's
// first verification (verified was false). NULL, an account from before
// signups had to verify, becomes true too.
func (d *DB) MarkVerified(ctx context.Context, id string) (bool, error) {
	return markVerified(ctx, d.Pool, id)
}

// ReplacePasswordAndVerify is ReplacePasswordHash, and if it changed the
// user, MarkVerified and the end of every login of the user, whoever held
// the old password, all in one transaction. firstVerify is MarkVerified's
// answer.
func (d *DB) ReplacePasswordAndVerify(ctx context.Context, id, hash string, oldHash *string) (changed, firstVerify bool, err error) {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return false, false, err
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	n, err := replacePasswordHash(ctx, tx, id, hash, oldHash)
	if err != nil {
		return false, false, err
	}
	if n > 0 {
		if firstVerify, err = markVerified(ctx, tx, id); err != nil {
			return false, false, err
		}
		if _, err := deleteLogins(ctx, tx, id); err != nil {
			return false, false, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, false, err
	}
	return n > 0, firstVerify, nil
}
