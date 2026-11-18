// Package auth checks the login of the "chats" cookie: a random token naming
// a row of the login table.
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
)

// Users checks logins against the login table.
type Users struct {
	DB db.Store
	// Secure marks the login cookie Secure.
	Secure bool

	// now is time.Now unless a test sets it.
	now func() time.Time
	// touch is login.Touch unless a test sets it.
	touch func(ctx context.Context, id int64) error
}

// Handler serves a request for a logged-in user.
type Handler func(w http.ResponseWriter, r *http.Request, user string)

type loginKey struct{}

// LoginID returns the id of the login that Require accepted
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
// renewing the login when it has gone unused for a while; otherwise it
// answers 401. A signup that hasn't confirmed its email address yet is let
// in: it can use relaychat while it confirms, and is deleted if it doesn't
// (lctl admin delete-unverified).
func (u Users) Require(h Handler) http.Handler {
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
		if !ok {
			login.ExpireCookie(w, u.Secure)
			respond.Error(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		h(w, r.WithContext(context.WithValue(ctx, loginKey{}, l.ID)), l.User)
	})
}

func (u Users) clock() time.Time {
	if u.now != nil {
		return u.now()
	}
	return time.Now()
}

// check finds the login for the cookie value v, renewing it and setting the
// login cookie as needed. Anything but a login token (a cookie from
// before login tokens, say) is no login.
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
	return login.Login{}, false, nil
}
