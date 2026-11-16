package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
)

// The small writes.
func (a *api) writes(mux *http.ServeMux) {
	mux.Handle("POST /preference/{pref}", a.users.Require(a.setPreference))
	mux.Handle("POST /connection/{id}/channel/{channel}/seen", a.users.Require(a.owned(a.setSeen)))
	mux.Handle("POST /connection/{id}/nick/{nick}", a.users.Require(a.owned(a.addPrivate)))
	mux.Handle("DELETE /connection/{id}/nick/{nick}", a.users.Require(a.owned(a.deletePrivate)))
}

// owned runs h for a user who owns connection {id}; every route with an id
// goes through it.
func (a *api) owned(h func(w http.ResponseWriter, r *http.Request, user, id string)) auth.Handler {
	return func(w http.ResponseWriter, r *http.Request, user string) {
		id := r.PathValue("id")
		owner, ok, err := a.db.LookupOwner(r.Context(), id)
		if err != nil {
			respond.Internal(w, "lookup owner", err)
			return
		}
		if !ok || owner != user {
			respond.Error(w, http.StatusUnauthorized, fmt.Sprintf("Invalid connection id '%s'", id))
			return
		}
		h(w, r, user, id)
	}
}

// maxBody caps request bodies; the largest real one, a web push
// subscription, is well under 1 KiB.
const maxBody = 64 << 10

// invalidUTF8 answers 400 for a request body that isn't valid UTF-8.
func invalidUTF8(w http.ResponseWriter) {
	respond.Error(w, http.StatusBadRequest, "request body isn't valid UTF-8")
}

// readBody is r's body. Over maxBody, or not valid UTF-8, it answers 400
// and reports false.
func readBody(w http.ResponseWriter, r *http.Request) (string, bool) {
	b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			respond.Error(w, http.StatusBadRequest, "request body is too large")
		} else {
			respond.Error(w, http.StatusBadRequest, "could not read the request body")
		}
		return "", false
	}
	if !utf8.Valid(b) {
		invalidUTF8(w)
		return "", false
	}
	return string(b), true
}

// pathText is r's path value name with invalid UTF-8 replaced.
func pathText(r *http.Request, name string) string {
	return strings.ToValidUTF8(r.PathValue(name), "�")
}

// tooLong answers 400 msg and reports true if s is over max characters
// (the column size).
func tooLong(w http.ResponseWriter, s string, max int, msg string) bool {
	if utf8.RuneCountInString(s) > max {
		respond.Error(w, http.StatusBadRequest, msg)
		return true
	}
	return false
}

func (a *api) setPreference(w http.ResponseWriter, r *http.Request, user string) {
	name := pathText(r, "pref")
	value, ok := readBody(w, r)
	if !ok || tooLong(w, name, 128, "preference name is too long") || tooLong(w, value, 512, "preference value is too long") {
		return
	}
	a.doneUser(w, "set preference", a.db.SetPref(r.Context(), user, name, value))
}

// setSeen records the body's message id as the last one seen in
// {channel}.
func (a *api) setSeen(w http.ResponseWriter, r *http.Request, user, id string) {
	channel := strings.ToLower(pathText(r, "channel"))
	body, ok := readBody(w, r)
	if !ok || tooLong(w, channel, 64, "channel name is too long") {
		return
	}
	msg, err := strconv.ParseInt(strings.TrimSpace(body), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "message id must be a number")
		return
	}
	a.doneConn(w, "set seen", id, a.db.SetSeen(r.Context(), user, id, channel, int32(msg)))
}

func (a *api) addPrivate(w http.ResponseWriter, r *http.Request, user, id string) {
	nick := pathText(r, "nick")
	if tooLong(w, nick, 32, "nick is too long") {
		return
	}
	a.doneConn(w, "add private", id, a.db.AddPrivate(r.Context(), id, nick))
}

func (a *api) deletePrivate(w http.ResponseWriter, r *http.Request, user, id string) {
	if err := a.db.DeletePrivate(r.Context(), id, pathText(r, "nick")); err != nil {
		respond.Internal(w, "delete private", err)
		return
	}
	respond.JSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"ok"})
}

// done answers 204, or 500 for err.
func (a *api) done(w http.ResponseWriter, what string, err error) {
	if err != nil {
		respond.Internal(w, what, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// doneConn is done for a write to connection id: one deleted since owned()
// checked it (db.ErrGone) is answered as owned() would answer now.
func (a *api) doneConn(w http.ResponseWriter, what, id string, err error) {
	if errors.Is(err, db.ErrGone) {
		respond.Error(w, http.StatusUnauthorized, fmt.Sprintf("Invalid connection id '%s'", id))
		return
	}
	a.done(w, what, err)
}

// doneUser is done for a write to the logged-in user: one deleted since the
// login was checked (db.ErrGone) is answered as the login check would answer
// now.
func (a *api) doneUser(w http.ResponseWriter, what string, err error) {
	if errors.Is(err, db.ErrGone) {
		respond.Error(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	a.done(w, what, err)
}
