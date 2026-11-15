package db

import (
	"context"
	"errors"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
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
	err := queryRow(ctx, d.Pool, psql.Select("id", "username", "email", "verified").From(`"user"`).Where(sq.Eq{"id": id})).
		Scan(&a.ID, &a.Username, &a.Email, &a.Verified)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, false, nil
	}
	return a, err == nil, err
}

// AccountsByLogin finds the users whose email or username is name, ignoring
// case.
func (d *DB) AccountsByLogin(ctx context.Context, name string) ([]AccountAuth, error) {
	rows, err := query(ctx, d.Pool, psql.Select("id", "username", "email", "verified", "password_hash").From(`"user"`).
		Where(sq.Or{sq.Expr("lower(email) = lower(?)", name), sq.Expr("lower(username) = lower(?)", name)}))
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
// Verified is not read.
func (d *DB) AccountAuthByEmail(ctx context.Context, email string) (AccountAuth, bool, error) {
	var a AccountAuth
	err := queryRow(ctx, d.Pool, accountAuth().Where("lower(email) = lower(?)", email)).
		Scan(&a.ID, &a.Username, &a.Email, &a.PasswordHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return AccountAuth{}, false, nil
	}
	return a, err == nil, err
}

// accountAuth selects an AccountAuth's columns, but not verified.
func accountAuth() sq.SelectBuilder {
	return psql.Select("id", "username", "email", "password_hash").From(`"user"`)
}

// AccountAuthByID finds the user with id. Verified is not read.
func (d *DB) AccountAuthByID(ctx context.Context, id string) (AccountAuth, bool, error) {
	var a AccountAuth
	err := queryRow(ctx, d.Pool, accountAuth().Where(sq.Eq{"id": id})).
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
	_, err := exec(ctx, d.Pool, psql.Insert(`"user"`).
		Columns("id", "username", "email", "password_hash", "verify_sent", "registered").
		Values(id, username, email, hash, sq.Expr("now()"), sq.Expr("now()")))
	return err
}

// UserHasConnection reports whether user has any connection.
func (d *DB) UserHasConnection(ctx context.Context, user string) (bool, error) {
	var has int
	err := queryRow(ctx, d.Pool, psql.Select("1").From("connection").Where(sq.Eq{`"user"`: user}).Limit(1)).Scan(&has)
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
	n, err := affected(ctx, d.Pool, psql.Update(`"user"`).Set("verify_sent", sq.Expr("now()")).
		Where(sq.Eq{"id": id, "verified": false}).
		Where(sq.Or{sq.Eq{"verify_sent": nil}, sq.Expr("verify_sent < now() - make_interval(secs => ?)", minSeconds)}))
	return n == 1, err
}

func replacePasswordHash(ctx context.Context, r runner, id, hash string, oldHash *string) (int64, error) {
	return affected(ctx, r, psql.Update(`"user"`).Set("password_hash", hash).
		Where(sq.Eq{"id": id}).Where("password_hash IS NOT DISTINCT FROM ?", oldHash))
}

func markVerified(ctx context.Context, r runner, id string) (bool, error) {
	verify := psql.Update(`"user"`).Set("verified", true)
	n, err := affected(ctx, r, verify.Where(sq.Eq{"id": id, "verified": false}))
	if err != nil {
		return false, err
	}
	if _, err := exec(ctx, r, verify.Where(sq.Eq{"id": id, "verified": nil})); err != nil {
		return false, err
	}
	return n == 1, nil
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
