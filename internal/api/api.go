// Package api is the HTTP API in front of the Perl one: it serves the
// routes ported to Go, and passes every other request to Perl.
package api

import (
	"net/http"

	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/stream"
)

type api struct {
	cfg   config.Config
	db    *db.DB
	users auth.Users
}

// New routes the stream server's routes and the ported ones, and
// everything else, any method, to perl.
func New(cfg config.Config, d *db.DB, streams *stream.Server, perl http.Handler) http.Handler {
	a := &api{cfg: cfg, db: d, users: auth.Users{Secret: cfg.Secret, DB: d}}
	mux := http.NewServeMux()
	streams.Routes(mux, a.users)
	a.reads(mux)
	mux.Handle("/", perl)
	return mux
}
