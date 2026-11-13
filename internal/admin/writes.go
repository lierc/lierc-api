package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
)

// result is the body of every successful write; unset fields are left out.
type result struct {
	Status string           `json:"status"`
	Counts map[string]int64 `json:"counts,omitempty"`
	Ended  *int64           `json:"ended,omitempty"`
	Pruned *int64           `json:"pruned,omitempty"`
}

func ptr(n int64) *int64 { return &n }

func (a *api) writeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /admin/users/{id}", a.deleteUser)
	mux.HandleFunc("DELETE /admin/connections/{id}", a.deleteConnection)
	mux.HandleFunc("DELETE /admin/users/{id}/logins/{login}", a.endLogin)
	mux.HandleFunc("DELETE /admin/users/{id}/logins", a.endAllLogins)
	mux.HandleFunc("POST /admin/logins/prune", a.pruneLogins)
	mux.HandleFunc("POST /admin/seen/prune", a.pruneSeen)
}

// logAfter reads the optional log_after, the highest log id before the
// delete began; ok is false after answering 400.
func logAfter(w http.ResponseWriter, r *http.Request) (n int64, ok bool) {
	s := r.URL.Query().Get("log_after")
	if s == "" {
		return 0, true
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		fail(w, http.StatusBadRequest, "log_after must be a non-negative integer")
		return 0, false
	}
	return n, true
}

type deleteFunc func(ctx context.Context, id string, logAfter int64) ([]db.ConnectionChange, map[string]int64, error)

// remove runs a delete and, once it has committed, announces each deleted
// connection. The delete is not tied to the request: a client that hangs up
// mid-way must not leave it half-known to the stream server (the database
// is all-or-nothing either way, but the announcements need the commit).
func (a *api) remove(w http.ResponseWriter, r *http.Request, del deleteFunc, what string) {
	after, ok := logAfter(w, r)
	if !ok {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	changes, counts, err := del(ctx, r.PathValue("id"), after)
	if errors.Is(err, db.ErrNotFound) {
		fail(w, http.StatusNotFound, "no such "+what)
		return
	}
	if err != nil {
		internal(w, "delete "+what, err)
		return
	}
	if a.announce != nil {
		for _, c := range changes {
			a.announce(ctx, c)
		}
	}
	write(w, http.StatusOK, result{Status: "ok", Counts: counts})
}

func (a *api) deleteUser(w http.ResponseWriter, r *http.Request) {
	a.remove(w, r, a.store.DeleteUserData, "user")
}

func (a *api) deleteConnection(w http.ResponseWriter, r *http.Request) {
	a.remove(w, r, a.store.DeleteConnectionData, "connection")
}

func (a *api) endLogin(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("login"), 10, 64)
	if err != nil {
		fail(w, http.StatusNotFound, "no such login")
		return
	}
	ended, err := login.Delete(r.Context(), a.store, r.PathValue("id"), id)
	if err != nil {
		internal(w, "end login", err)
		return
	}
	if !ended {
		fail(w, http.StatusNotFound, "no such login")
		return
	}
	write(w, http.StatusOK, result{Status: "ok", Ended: ptr(1)})
}

func (a *api) endAllLogins(w http.ResponseWriter, r *http.Request) {
	n, err := login.DeleteAll(r.Context(), a.store, r.PathValue("id"))
	if err != nil {
		internal(w, "end logins", err)
		return
	}
	write(w, http.StatusOK, result{Status: "ok", Ended: &n})
}

func (a *api) pruneLogins(w http.ResponseWriter, r *http.Request) {
	n, err := login.Prune(r.Context(), a.store)
	if err != nil {
		internal(w, "prune logins", err)
		return
	}
	write(w, http.StatusOK, result{Status: "ok", Pruned: &n})
}

func (a *api) pruneSeen(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.PruneStaleSeen(r.Context())
	if err != nil {
		internal(w, "prune seen", err)
		return
	}
	write(w, http.StatusOK, result{Status: "ok", Pruned: &n})
}
