// Package accounts is login, signup, email confirmation and password
// reset, ported from lib/API/Controller/Auth.pm and the DB.pm helpers it
// uses. It keeps their order of steps, SQL conditions and messages.
package accounts

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/connections"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/ids"
	"github.com/lierc/lierc-api/internal/passwords"
)

// Error is a problem for the user, with Perl's message and status (400 or
// 401). Every other error is the database's, a 500.
type Error struct {
	Code int
	Msg  string
}

func (e *Error) Error() string { return e.Msg }

// IsError is err's user-facing error, if it has one.
func IsError(err error) (*Error, bool) {
	var e *Error
	if errors.As(err, &e) {
		return e, true
	}
	return nil, false
}

// Mailer sends one plain-text email (mail.Sender).
type Mailer interface {
	Send(to, subject, body string) error
}

// User is what GET /auth shows. Verified is nil for an account from before
// signups had to confirm their email.
type User = db.Account

// Service does the work, through the store, connections (for the
// default network) and Mail.
type Service struct {
	Store  db.Store
	Conns  *connections.Service
	Mail   Mailer
	Secret string
	// SiteURL starts the emailed links.
	SiteURL string
	// NoSMTP (LIERC_NO_SMTP) builds emails but doesn't send them.
	NoSMTP         bool
	DefaultNetwork *config.DefaultNetwork
	Now            func() time.Time // time.Now when nil
	After          func(func())     // runs mail after the response; go f() when nil
}

const (
	msgLogin        = "Invalid email or password"
	msgUsername     = "Usernames can only use letters, digits, _ and ., up to 32 characters"
	msgEmail        = "Invalid email address"
	msgVerifyBad    = "This confirmation link has expired or isn't valid."
	msgResetExpired = "This reset link has expired or was already used. Request a new one."
	msgNoPassword   = "A new password is required"

	verifySubject = "Confirm your relaychat.party email address"
	resetSubject  = "Reset your relaychat.party password"

	// resendEvery is how often a confirmation link can be resent, in seconds.
	resendEvery = 60
)

const verifyTemplate = `Hello <: $name :>,

Thanks for signing up with relaychat.party! Please confirm your email
address by opening this link:
<: $link :>

Happy chatting!`

const resetTemplate = `Hello <: $name :>,

Someone asked to reset the password for your relaychat.party account.
To choose a new password, open this link within the next hour:
<: $link :>

If you didn't ask for this, you can ignore this email; your password
hasn't changed.`

// checkPassword is every password check a login makes, the dummy one
// included; tests count the calls.
var checkPassword = passwords.Check

var (
	// Perl's /\S/ on the request's bytes: \s is ASCII whitespace only.
	nonSpace = regexp.MustCompile(`[^\t\n\v\f\r ]`)
	username = regexp.MustCompile(`^[a-zA-Z0-9_.]{1,32}$`)
	// Text::Xslate's default html escaping of <: $var :>
	xslateEscape = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
)

func userError(code int, msg string) error { return &Error{code, msg} }

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// after queues f, the sending of an email, to run after the response. It
// runs in a goroutine of its own, so a panic in it would take the whole
// process down: it's recovered and logged.
func (s *Service) after(f func()) {
	safe := func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("accounts: sending mail panicked: %v", r)
			}
		}()
		f()
	}
	if s.After != nil {
		s.After(safe)
		return
	}
	go safe()
}

// send sends one email, if there's a mailer to send it with.
func (s *Service) send(to, subject, body string) error {
	if s.Mail == nil {
		return errors.New("no mailer configured")
	}
	return s.Mail.Send(to, subject, body)
}

// stepTimeout bounds the work done after verified=true is committed.
const stepTimeout = time.Minute

// detached is ctx without its cancellation, bounded by stepTimeout: once
// verified=true is committed, the signup network must still be added if
// the browser goes away, since a later verify isn't a first one.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), stepTimeout)
}

// asciiLower is Perl's lc on bytes: only A-Z change.
func asciiLower(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}, s)
}

func render(template, name, link string) string {
	return strings.NewReplacer("<: $name :>", xslateEscape.Replace(name), "<: $link :>", xslateEscape.Replace(link)).Replace(template)
}

// Show is the logged-in user (DB.pm lookup_user); a missing one is a 401.
func (s *Service) Show(ctx context.Context, id string) (User, error) {
	u, ok, err := s.Store.AccountByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, userError(401, "unauthorized")
	}
	return u, nil
}

// Login finds the user whose email or username is email, ignoring case,
// and whose password_hash checks against pass. There's usually one row,
// and more if one user's email is another's username; each is tried in
// turn. A hash that isn't cost 12 is replaced by one that is.
func (s *Service) Login(ctx context.Context, email, pass string) (User, error) {
	found, err := s.Store.AccountsByLogin(ctx, email)
	if err != nil {
		return User{}, err
	}

	checked := false
	for _, r := range found {
		// a legacy-only row never matches: those users reset
		if r.PasswordHash == nil {
			continue
		}
		checked = true
		if !checkPassword(pass, *r.PasswordHash) {
			continue
		}
		if passwords.NeedsRehash(*r.PasswordHash) {
			if err := s.rehash(ctx, r, pass); err != nil {
				return User{}, err
			}
		}
		return r.Account, nil
	}
	// so that no match takes as long as a wrong password
	if !checked {
		checkPassword(pass, passwords.Dummy)
	}
	return User{}, userError(401, msgLogin)
}

// rehash saves a cost-12 hash of pass for r, unless the stored hashes
// changed since they were read; losing that race isn't an error.
func (s *Service) rehash(ctx context.Context, r db.AccountAuth, pass string) error {
	hash, err := passwords.Hash(pass)
	if err != nil {
		return err
	}
	_, err = s.Store.ReplacePasswordHash(ctx, r.ID, hash, r.PasswordHash, r.Password)
	return err
}

// Register adds a user (Auth.pm register and DB.pm add_user) and queues
// the confirmation email. The default network comes at confirmation.
func (s *Service) Register(ctx context.Context, name, email, pass string) (User, error) {
	for _, f := range []struct{ field, value string }{{"username", name}, {"email", email}, {"pass", pass}} {
		if !nonSpace.MatchString(f.value) {
			return User{}, userError(400, f.field+" is required")
		}
	}
	// stored as typed; unique and looked up regardless of case
	if !username.MatchString(name) {
		return User{}, userError(400, msgUsername)
	}
	// stored lowercase; unique and looked up regardless of case
	email = asciiLower(email)
	if !IsEmail(email) {
		return User{}, userError(400, msgEmail)
	}

	hash, err := passwords.Hash(pass)
	if err != nil {
		return User{}, err
	}
	id := ids.New()
	// verify_sent: signup sends the first verify email
	err = s.Store.InsertUser(ctx, id, name, email, hash)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return User{}, userError(400, pgErr.Detail)
		}
		return User{}, err
	}

	u, err := s.Show(ctx, id)
	if err != nil {
		return User{}, err
	}
	s.after(func() { s.sendVerifyEmail(id, name, email) })
	return u, nil
}

func (s *Service) sendVerifyEmail(id, name, email string) {
	token := MakeToken(s.Secret, "verify", id, email, s.now().Unix()+VerifyLifetime)
	body := render(verifyTemplate, name, s.SiteURL+"/verify/#"+token)
	if s.NoSMTP {
		return
	}
	if err := s.send(email, verifySubject, body); err != nil {
		log.Printf("Unable to send verification email for %s: %v", name, err)
	}
}

// Verify confirms the email address in a confirmation link's token. It
// doesn't log anyone in: links get forwarded.
func (s *Service) Verify(ctx context.Context, token string) error {
	id, ok := TokenUser(token)
	if !ok {
		return userError(400, msgVerifyBad)
	}
	u, ok, err := s.Store.AccountByID(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return userError(400, msgVerifyBad)
	}
	if !TokenValid(s.Secret, "verify", token, u.Email, s.now().Unix()) {
		return userError(400, msgVerifyBad)
	}
	first, err := s.Store.MarkVerified(ctx, u.ID)
	if err != nil {
		return err
	}
	if first {
		ctx, cancel := detached(ctx)
		defer cancel()
		return s.addFirstNetwork(ctx, u.ID, u.Username)
	}
	return nil
}

// addFirstNetwork adds the default network unless the user has a
// connection already: an account that signed up before verifying was
// required got the network then. A failure to add it is logged and doesn't
// fail the verification.
func (s *Service) addFirstNetwork(ctx context.Context, user, name string) error {
	has, err := s.Store.UserHasConnection(ctx, user)
	if err != nil {
		return err
	}
	if has {
		return nil
	}
	if err := s.addDefaultNetwork(ctx, user, name); err != nil {
		log.Printf("Unable to add default network for %s: %v", name, err)
	}
	return nil
}

// addDefaultNetwork is Auth.pm add_default_network: the configured
// network, with the user's name as nick, username and highlight.
func (s *Service) addDefaultNetwork(ctx context.Context, user, name string) error {
	n := s.DefaultNetwork
	if n == nil {
		return nil
	}
	nick := IrcNick(name)
	channels := n.Channels
	if channels == nil {
		channels = []string{} // Perl's [ split ' ', "" ]
	}
	_, err := s.Conns.CreateDefault(ctx, user, map[string]any{
		"Host":      n.Host,
		"Port":      n.Port,
		"Ssl":       n.Ssl,
		"Channels":  channels,
		"Nick":      nick,
		"User":      nick,
		"Highlight": []string{nick},
	})
	return err
}

// Resend sends a logged-in user who hasn't verified a new confirmation
// link, at most once a minute (counting the one sent at signup). The
// update both checks and records that, so two resends at once send one
// email.
func (s *Service) Resend(ctx context.Context, id string) error {
	u, err := s.Show(ctx, id)
	if _, ok := IsError(err); ok {
		return nil
	}
	if err != nil {
		return err
	}
	claimed, err := s.Store.ClaimVerifyResend(ctx, u.ID, float64(resendEvery))
	if err != nil {
		return err
	}
	if claimed {
		s.after(func() { s.sendVerifyEmail(u.ID, u.Username, u.Email) })
	}
	return nil
}

// Reset emails a reset link to the account with this address, if there is
// one. The answer is the same either way, and the email goes out after
// it, so the time taken doesn't reveal whether the address has an account.
func (s *Service) Reset(ctx context.Context, email string) error {
	if !IsEmail(email) {
		return userError(400, msgEmail)
	}
	acct, ok, err := s.Store.AccountAuthByEmail(ctx, email)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	id, name, addr := acct.ID, acct.Username, acct.Email
	fingerprint := resetFingerprint(acct.PasswordHash, acct.Password)
	s.after(func() {
		token := MakeToken(s.Secret, "reset", id, fingerprint, s.now().Unix()+ResetLifetime)
		body := render(resetTemplate, name, s.SiteURL+"/reset/#"+token)
		if s.NoSMTP {
			return
		}
		if err := s.send(addr, resetSubject, body); err != nil {
			log.Printf("Unable to send reset email for %s: %v", name, err)
		}
	})
	return nil
}

// resetFingerprint is password_hash // password: the token stops working
// once the password changes.
func resetFingerprint(hash, password *string) string {
	switch {
	case hash != nil:
		return *hash
	case password != nil:
		return *password
	}
	return ""
}

// ResetConfirm sets a new password from a reset link's token, and answers
// the user, to log in.
func (s *Service) ResetConfirm(ctx context.Context, token, pass string) (User, error) {
	if !nonSpace.MatchString(pass) {
		return User{}, userError(400, msgNoPassword)
	}
	id, ok := TokenUser(token)
	if !ok {
		return User{}, userError(400, msgResetExpired)
	}
	acct, ok, err := s.Store.AccountAuthByID(ctx, id)
	if err != nil {
		return User{}, err
	}
	if !ok {
		return User{}, userError(400, msgResetExpired)
	}
	uid, name, hash, password := acct.ID, acct.Username, acct.PasswordHash, acct.Password
	if !TokenValid(s.Secret, "reset", token, resetFingerprint(hash, password), s.now().Unix()) {
		return User{}, userError(400, msgResetExpired)
	}

	// The new password and verified (the link proves they own the address)
	// change together or not at all. The password only changes if it's
	// still the one the token was checked against, so of two confirms with
	// the same link at once, one wins and the other gets the expired error.
	newHash, err := passwords.Hash(pass)
	if err != nil {
		return User{}, err
	}
	changed, first, err := s.Store.ReplacePasswordAndVerify(ctx, uid, newHash, hash, password)
	if err != nil {
		return User{}, err
	}
	if !changed {
		return User{}, userError(400, msgResetExpired)
	}
	// the password has changed: finish even if the browser goes away
	ctx, cancel := detached(ctx)
	defer cancel()
	if first {
		if err := s.addFirstNetwork(ctx, uid, name); err != nil {
			return User{}, err
		}
	}
	return s.Show(ctx, uid)
}
