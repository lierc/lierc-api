package admin

import (
	"context"
	"errors"
	"log"
	"net/http"
	"strconv"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/login"
)

// DeleteResult answers a user or connection delete.
type DeleteResult struct {
	Status string `json:"status"`
	// Counts is the delete's own rows per table.
	Counts map[string]int64 `json:"counts"`
	// Swept is the late log rows the sweep removed (0 without log_after);
	// it is not in Counts.
	Swept int64 `json:"swept"`
	// SweepError is set when the delete committed but the sweep failed.
	SweepError string `json:"sweep_error,omitempty"`
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

// logAfter reads the optional log_after, the highest log id before the
// delete began. given is whether the parameter was present, so 0 (an empty
// log) counts; ok is false after answering 400.
func logAfter(w http.ResponseWriter, r *http.Request) (n int64, given, ok bool) {
	if !r.URL.Query().Has("log_after") {
		return 0, false, true
	}
	n, err := strconv.ParseInt(r.URL.Query().Get("log_after"), 10, 64)
	if err != nil || n < 0 {
		fail(w, http.StatusBadRequest, "log_after must be a non-negative integer")
		return 0, false, false
	}
	return n, true, true
}

type deleteFunc func(ctx context.Context, id string) ([]db.ConnectionChange, map[string]int64, error)

// remove runs a delete and, once it has committed, announces each deleted
// connection, then sweeps late log rows if log_after was given. The work is
// not tied to the request: a client that hangs up mid-way must not leave
// the stream server unaware of a committed delete. A sweep failure after the
// commit still answers 200, with sweep_error: the delete is done.
func (a *api) remove(w http.ResponseWriter, r *http.Request, del deleteFunc, what string) {
	after, given, ok := logAfter(w, r)
	if !ok {
		return
	}
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
	if given {
		user := ""
		conns := make([]string, 0, len(changes))
		for _, c := range changes {
			conns = append(conns, c.Connection)
		}
		if what == "user" {
			user = id
		}
		n, err := a.store.SweepLateLogs(ctx, user, conns, after)
		if err != nil {
			log.Printf("admin: sweep late logs of %s: %v", what, err)
			res.SweepError = "internal error"
		} else {
			res.Swept = n
		}
	}
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
