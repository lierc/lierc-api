// Package api is relaychat's HTTP API. It ported the Perl API's routes
// (lib/API in git history); an unknown path is a 404.
package api

import (
	"context"
	"github.com/lierc/lierc-api/internal/respond"

	"github.com/lierc/lierc-api/internal/accounts"

	"github.com/lierc/lierc-api/internal/connections"
	"github.com/lierc/lierc-api/internal/liercd"
	"net/http"
	"time"

	"github.com/lierc/lierc-api/internal/auth"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/ratelimit"
	"github.com/lierc/lierc-api/internal/stream"
)

type api struct {
	cfg   config.Config
	db    db.Store
	users auth.Users
	conns *connections.Service
	acct  *accounts.Service

	// per-account limits on failed logins and reset emails
	loginLimit, resetLimit *ratelimit.Limiter
}

// New routes the stream server's routes and the ported ones, and
// answers anything else, any method, with a 404.
func New(cfg config.Config, s db.Store, streams *stream.Server, lc *liercd.Client) http.Handler {
	a := &api{
		cfg:        cfg,
		db:         s,
		users:      auth.Users{Secret: cfg.Secret, DB: s, Secure: cfg.Secure},
		loginLimit: ratelimit.New(10, 15*time.Minute),
		resetLimit: ratelimit.New(3, time.Hour),
	}
	a.conns = &connections.Service{
		DB:               s,
		Liercd:           lc,
		Announce:         streams.ConnectionChanged, // in memory, no pg_notify
		NotifyHighlights: connections.NotifyHighlights(s),
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
	a.accounts(mux)
	a.logins(mux)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		respond.Error(w, http.StatusNotFound, "not found")
	})
	return sameOrigin(cfg.Origins, mux)
}
