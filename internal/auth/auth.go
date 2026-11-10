// Package auth checks the login of Perl's "chats" cookie, as the Perl API's
// dispatch does (lib/API/Dispatch.pm handle).
package auth

import (
	"net/http"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
	"github.com/lierc/lierc-api/internal/session"
)

// Users checks logins against the cookie secret and the database.
type Users struct {
	Secret string
	DB     *db.DB
}

// Handler serves a request for a logged-in user.
type Handler func(w http.ResponseWriter, r *http.Request, user string)

type unverifiedBody struct {
	Status     string `json:"status"`
	Error      string `json:"error"`
	Unverified bool   `json:"unverified"`
}

// Require runs h for the existing user logged in by r's "chats" cookie.
// Otherwise it answers 401, or 403 for a signup that hasn't confirmed its
// email address yet (verified NULL, from before signups had to verify, is
// let in).
func (u Users) Require(h Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := session.User(r, u.Secret)
		ok, unverified := false, false
		if user != "" {
			var err error
			if ok, unverified, err = u.DB.UserState(r.Context(), user); err != nil {
				respond.Internal(w, "lookup user", err)
				return
			}
		}
		switch {
		case !ok:
			respond.Error(w, http.StatusUnauthorized, "unauthorized")
		case unverified:
			// not a 401, so the UI doesn't take it for logged out
			respond.JSON(w, http.StatusForbidden, unverifiedBody{"error", "Confirm your email address first", true})
		default:
			h(w, r, user)
		}
	})
}
