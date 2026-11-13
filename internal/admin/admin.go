// Package admin is the JSON admin API under /admin/, served on the API's
// loopback admin listener. It has no authentication: whoever can reach the
// listener is the operator. All SQL lives in internal/db.
package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/lierc/lierc-api/internal/db"
)

// Error is the body of every failed request.
type Error struct {
	Error string `json:"error"`
}

type api struct {
	store    db.Store
	announce func(context.Context, db.ConnectionChange)
}

// New builds the admin routes on s. announce tells the stream server of a
// connection change; it may be nil.
func New(s db.Store, announce func(context.Context, db.ConnectionChange)) http.Handler {
	a := &api{store: s, announce: announce}
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/", func(w http.ResponseWriter, r *http.Request) {
		fail(w, http.StatusNotFound, "not found")
	})
	a.userRoutes(mux)
	a.connectionRoutes(mux)
	mux.HandleFunc("GET /admin/log/last", a.lastLog)
	mux.HandleFunc("GET /admin/seen/stale", a.staleSeen)
	return mux
}

func write(w http.ResponseWriter, code int, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		log.Printf("admin: encode response: %v", err)
		write(w, http.StatusInternalServerError, Error{"internal error"})
		return
	}
	b := bytes.TrimSuffix(buf.Bytes(), []byte("\n"))
	h := w.Header()
	h.Set("Content-Type", "application/json")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	w.Write(b)
}

func fail(w http.ResponseWriter, code int, msg string) {
	write(w, code, Error{msg})
}

// internal logs what failed and answers 500 without it.
func internal(w http.ResponseWriter, what string, err error) {
	log.Printf("admin: %s: %v", what, err)
	fail(w, http.StatusInternalServerError, "internal error")
}

// sinceParam reads an optional RFC 3339 inactive_since; ok is false after
// answering 400.
func sinceParam(w http.ResponseWriter, r *http.Request) (t *time.Time, ok bool) {
	s := r.URL.Query().Get("inactive_since")
	if s == "" {
		return nil, true
	}
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		fail(w, http.StatusBadRequest, "inactive_since must be an RFC 3339 time")
		return nil, false
	}
	return &v, true
}

// utc is t as an RFC 3339 UTC time to the second, nil for NULL.
func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	v := t.UTC().Truncate(time.Second)
	return &v
}

func (a *api) lastLog(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.LastLogID(r.Context())
	if err != nil {
		internal(w, "last log id", err)
		return
	}
	write(w, http.StatusOK, map[string]int64{"id": n})
}

func (a *api) staleSeen(w http.ResponseWriter, r *http.Request) {
	n, err := a.store.StaleSeenCount(r.Context())
	if err != nil {
		internal(w, "count stale seen", err)
		return
	}
	write(w, http.StatusOK, map[string]int64{"count": n})
}
