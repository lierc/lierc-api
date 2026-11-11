// Package api is the HTTP API in front of the Perl one: it serves the
// routes ported to Go, and passes every other request to Perl.
package api

import (
	"context"

	"github.com/lierc/lierc-api/internal/connections"
	"github.com/lierc/lierc-api/internal/liercd"
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
	conns *connections.Service
}

// New routes the stream server's routes and the ported ones, and
// everything else, any method, to perl.
func New(cfg config.Config, d *db.DB, streams *stream.Server, lc *liercd.Client, perl http.Handler) http.Handler {
	a := &api{cfg: cfg, db: d, users: auth.Users{Secret: cfg.Secret, DB: d}}
	a.conns = &connections.Service{
		DB:               d,
		Liercd:           lc,
		Announce:         streams.ConnectionChanged, // in memory, no pg_notify
		NotifyHighlights: connections.NotifyHighlights(d),
		CheckHost:        func(ctx context.Context, host string, port int) error { return checkHost(ctx, host, port) },
		Limit:            5,
	}
	mux := http.NewServeMux()
	streams.Routes(mux, a.users)
	a.reads(mux)
	a.writes(mux)
	a.webPush(mux)
	a.history(mux)
	a.search(mux)
	a.connections(mux)
	mux.Handle("/", perl)
	return sameOrigin(cfg.Origins, mux)
}
