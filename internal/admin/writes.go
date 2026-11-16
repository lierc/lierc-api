package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
)

// DeleteResult answers a user or connection delete.
type DeleteResult struct {
	Status string `json:"status"`
	// Counts is the rows deleted per table.
	Counts map[string]int64 `json:"counts"`
}

// EndedResult answers ending a user's logins: how many ended.
type EndedResult struct {
	Status string `json:"status"`
	Ended  int64  `json:"ended"`
}

// PrunedResult answers a prune: how many rows went.
type PrunedResult struct {
	Status string `json:"status"`
	Pruned int64  `json:"pruned"`
}

func (a *api) writeRoutes(mux *http.ServeMux) {
	mux.HandleFunc("DELETE /admin/users/{id}", a.deleteUser)
	mux.HandleFunc("DELETE /admin/connections/{id}", a.deleteConnection)
	mux.HandleFunc("DELETE /admin/users/{id}/logins/{login}", a.endLogin)
	mux.HandleFunc("DELETE /admin/users/{id}/logins", a.endAllLogins)
	mux.HandleFunc("POST /admin/logins/prune", a.pruneLogins)
	mux.HandleFunc("POST /admin/seen/prune", a.pruneSeen)
}

type deleteFunc func(ctx context.Context, id string) ([]db.ConnectionChange, map[string]int64, error)

// remove runs a delete and, once it has committed, announces each deleted
// connection. The work is not tied to the request: a client that hangs up
// mid-way must not leave the stream server unaware of a committed delete.
// (An older lctl's log_after is ignored: the keys leave no late rows.)
func (a *api) remove(w http.ResponseWriter, r *http.Request, del deleteFunc, what string) {
	id := r.PathValue("id")
	ctx := context.WithoutCancel(r.Context())
	changes, counts, err := del(ctx, id)
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
	res := DeleteResult{Status: "ok", Counts: counts}
	write(w, http.StatusOK, res)
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
	write(w, http.StatusOK, EndedResult{Status: "ok", Ended: 1})
}

func (a *api) endAllLogins(w http.ResponseWriter, r *http.Request) {
	n, err := login.DeleteAll(r.Context(), a.store, r.PathValue("id"))
	if err != nil {
		internal(w, "end logins", err)
		return
	}
	write(w, http.StatusOK, EndedResult{Status: "ok", Ended: n})
}

func (a *api) pruneLogins(w http.ResponseWriter, r *http.Request) {
	n, err := login.Prune(r.Context(), a.store)
	if err != nil {
		internal(w, "prune logins", err)
		return
	}
	write(w, http.StatusOK, PrunedResult{Status: "ok", Pruned: n})
}

func (a *api) pruneSeen(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.PruneStaleSeen(r.Context())
	if err != nil {
		internal(w, "prune seen", err)
		return
	}
	write(w, http.StatusOK, PrunedResult{Status: "ok", Pruned: n})
}
