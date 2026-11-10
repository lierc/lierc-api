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
	"github.com/lierc/lierc-api/internal/respond"
)

// The small writes, ported from lib/API/Controller/*.pm.
func (a *api) writes(mux *http.ServeMux) {
	mux.Handle("POST /preference/{pref}", a.users.Require(a.setPreference))
	mux.Handle("GET /unsubscribe", a.users.Require(a.unsubscribe))
	mux.Handle("POST /connection/{id}/channel/{channel}/seen", a.users.Require(a.owned(a.setSeen)))
	mux.Handle("POST /connection/{id}/nick/{nick}", a.users.Require(a.owned(a.addPrivate)))
	mux.Handle("DELETE /connection/{id}/nick/{nick}", a.users.Require(a.owned(a.deletePrivate)))
}

// owned runs h for a user who owns connection {id} (Perl verify_owner,
// run by Dispatch.pm for every route with an id).
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

// readBody is r's body with invalid UTF-8 replaced, as Perl's decode utf8
// does. Over maxBody it answers 400 and reports false.
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
	return strings.ToValidUTF8(string(b), "�"), true
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
	a.done(w, "set preference", a.db.SetPref(r.Context(), user, name, value))
}

// unsubscribe turns email notifications off (Pref.pm unsubscribe).
func (a *api) unsubscribe(w http.ResponseWriter, r *http.Request, user string) {
	a.done(w, "unsubscribe", a.db.SetPref(r.Context(), user, "email", "false"))
}

// setSeen records the body's message id as the last one seen in
// {channel}.
func (a *api) setSeen(w http.ResponseWriter, r *http.Request, user, id string) {
	channel := strings.ToLower(pathText(r, "channel"))
	body, ok := readBody(w, r)
	if !ok || tooLong(w, channel, 32, "channel name is too long") {
		return
	}
	msg, err := strconv.ParseInt(strings.TrimSpace(body), 10, 32)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "message id must be a number")
		return
	}
	a.done(w, "set seen", a.db.SetSeen(r.Context(), user, id, channel, int32(msg)))
}

func (a *api) addPrivate(w http.ResponseWriter, r *http.Request, user, id string) {
	nick := pathText(r, "nick")
	if tooLong(w, nick, 32, "nick is too long") {
		return
	}
	a.done(w, "add private", a.db.AddPrivate(r.Context(), id, nick))
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
