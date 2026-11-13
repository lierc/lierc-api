// Package auth checks the login of the "chats" cookie: a random token naming
// a row of the login table. A Perl-format cookie from before the switch is
// swapped for a login.
package auth

import (
	"context"
	"log"
	"net"
	"net/http"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/respond"
	"github.com/lierc/lierc-api/internal/session"
)

// Users checks logins against the login table and the cookie secret.
type Users struct {
	// Secret reads the Perl-format cookies of logins from before the switch.
	Secret string
	DB     db.Store
	// Secure marks the login cookie Secure.
	Secure bool

	// now is time.Now unless a test sets it.
	now func() time.Time
	// touch is login.Touch unless a test sets it.
	touch func(ctx context.Context, id int64) error
}

// Handler serves a request for a logged-in user.
type Handler func(w http.ResponseWriter, r *http.Request, user string)

type unverifiedBody struct {
	Status     string `json:"status"`
	Error      string `json:"error"`
	Unverified bool   `json:"unverified"`
}

type loginKey struct{}

// LoginID returns the id of the login that Require or RequireAny accepted
// for the request in ctx.
func LoginID(ctx context.Context) (int64, bool) {
	id, ok := ctx.Value(loginKey{}).(int64)
	return id, ok
}

// Client returns the User-Agent and the IP address (without the port) of r,
// for creating a login.
func Client(r *http.Request) (userAgent, ip string) {
	ip = r.RemoteAddr
	if host, _, err := net.SplitHostPort(ip); err == nil {
		ip = host
	}
	return r.UserAgent(), ip
}

// Require runs h for the existing user logged in by r's "chats" cookie,
// renewing the login when it has gone unused for a while.
// Otherwise it answers 401, or 403 for a signup that hasn't confirmed its
// email address yet (verified NULL, from before signups had to verify, is
// let in).
func (u Users) Require(h Handler) http.Handler { return u.require(h, false) }

// RequireAny is Require, but lets in a signup that hasn't confirmed its
// email address yet (GET /auth, POST /verify/resend).
func (u Users) RequireAny(h Handler) http.Handler { return u.require(h, true) }

// perlCutoff is when Perl cookies stop working. After the deploy nothing
// issues them, so every legitimate browser's copy is swapped for a login or
// has expired within 7 days; from this time on a Perl cookie is refused, so a
// copied one can't come back after a password reset or end-login. A later
// change deletes the Perl path entirely.
var perlCutoff = time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)

func (u Users) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

func (u Users) require(h Handler, any bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		c, err := r.Cookie(login.CookieName)
		if err != nil {
			respond.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		l, ok, err := u.check(w, r, c.Value)
		if err != nil {
			respond.Internal(w, "check login", err)
			return
		}
		switch {
		case !ok:
			login.ExpireCookie(w, u.Secure)
			respond.Error(w, http.StatusUnauthorized, "unauthorized")
		case l.Unverified && !any:
			// not a 401, so the UI doesn't take it for logged out
			respond.JSON(w, http.StatusForbidden, unverifiedBody{"error", "Confirm your email address first", true})
		default:
			h(w, r.WithContext(context.WithValue(ctx, loginKey{}, l.ID)), l.User)
		}
	})
}

// check finds the login for the cookie value v, renewing it or swapping a
// Perl cookie for a new login, and setting the login cookie as needed.
func (u Users) check(w http.ResponseWriter, r *http.Request, v string) (login.Login, bool, error) {
	ctx := r.Context()
	if login.IsToken(v) {
		l, ok, err := login.Lookup(ctx, u.DB, v)
		if err != nil || !ok {
			return login.Login{}, false, err
		}
		if u.clock().Sub(l.LastUsed) > login.RenewAfter {
			touch := u.touch
			if touch == nil {
				touch = func(ctx context.Context, id int64) error { return login.Touch(ctx, u.DB, id) }
			}
			if err := touch(ctx, l.ID); err != nil {
				log.Printf("renew login: %v", err)
			} else {
				login.SetCookie(w, v, u.Secure, u.clock())
			}
		}
		return l, true, nil
	}
	if !u.clock().Before(perlCutoff) {
		return login.Login{}, false, nil
	}
	user, ok := session.ReadPerl(v, u.Secret)
	if !ok {
		return login.Login{}, false, nil
	}
	exists, _, err := u.DB.UserState(ctx, user)
	if err != nil || !exists {
		return login.Login{}, false, err
	}
	agent, ip := Client(r)
	token, err := login.Create(ctx, u.DB, user, agent, ip)
	if err != nil {
		return login.Login{}, false, err
	}
	l, ok, err := login.Lookup(ctx, u.DB, token)
	if err != nil || !ok {
		return login.Login{}, false, err
	}
	login.SetCookie(w, token, u.Secure, u.clock())
	return l, true, nil
}
