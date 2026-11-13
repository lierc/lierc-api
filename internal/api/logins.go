package api

import (
	"net/http"
	"strconv"
	"time"

	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/login"
	"github.com/lierc/lierc-api/internal/respond"
)

// logins are a user's own logins, for the UI's Logins dialog: list them,
// and end one. lctl admin logins and lctl admin end-login do the same for any user.
func (a *api) logins(mux *http.ServeMux) {
	mux.Handle("GET /logins", a.users.Require(a.listLogins))
	mux.Handle("DELETE /login/{id}", a.users.Require(a.deleteLogin))
}

type loginJSON struct {
	ID        int64  `json:"id"`
	Created   string `json:"created"`
	LastUsed  string `json:"last_used"`
	UserAgent string `json:"user_agent"`
	IP        string `json:"ip"`
	Current   bool   `json:"current"`
	// Connected is whether the login has an event stream open now.
	Connected bool `json:"connected"`
}

func (a *api) listLogins(w http.ResponseWriter, r *http.Request, user string) {
	infos, err := login.List(r.Context(), a.db, user)
	if err != nil {
		respond.Internal(w, "list logins", err)
		return
	}
	current, _ := auth.LoginID(r.Context())
	connected := a.streams.ConnectedLogins(user)
	list := make([]loginJSON, 0, len(infos))
	for _, l := range infos {
		list = append(list, loginJSON{
			ID:        l.ID,
			Created:   l.Created.UTC().Format(time.RFC3339),
			LastUsed:  l.LastUsed.UTC().Format(time.RFC3339),
			UserAgent: l.UserAgent,
			IP:        l.IP,
			Current:   l.ID == current,
			Connected: connected[l.ID],
		})
	}
	respond.JSON(w, http.StatusOK, list)
}

// deleteLogin ends one of user's own logins; any other id, an ended one or
// a non-number is "no such login". Ending the current login is allowed.
func (a *api) deleteLogin(w http.ResponseWriter, r *http.Request, user string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		respond.Error(w, http.StatusNotFound, "no such login")
		return
	}
	deleted, err := login.Delete(r.Context(), a.db, user, id)
	if err != nil {
		respond.Internal(w, "end login", err)
		return
	}
	if !deleted {
		respond.Error(w, http.StatusNotFound, "no such login")
		return
	}
	ok(w)
}
