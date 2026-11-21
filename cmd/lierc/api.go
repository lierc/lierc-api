package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"time"

	adminapi "github.com/lierc/lierc-api/internal/admin"
	"github.com/lierc/lierc-api/internal/api"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/proxy"
	"github.com/lierc/lierc-api/internal/stream"
)

// shutdownTimeout bounds the graceful drain: closing streams saves their
// channels and last_login.
const shutdownTimeout = 10 * time.Second

// runAPI runs `lierc api`: the API, the SSE event stream and the admin
// listener, after applying any pending schema migrations. It returns the
// process exit code; any argument is a usage error. onListen, if non-nil,
// is called with the stream and admin addresses once both are bound.
func runAPI(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, onListen func(stream, admin net.Addr)) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: lierc api (no arguments; the admin commands are lctl)")
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)

	cfg, err := config.Load(getenv)
	if err == nil {
		err = cfg.RequireSecrets()
	}
	if err != nil {
		logger.Printf("config: %v", err)
		return 1
	}
	d, err := db.Open(ctx, cfg)
	if err != nil {
		logger.Printf("database: %v", err)
		return 1
	}
	defer d.Close()
	// The schema first: nothing below may run against an older one.
	if err := d.Migrate(ctx, db.MigrateOptions{Logf: logger.Printf}); err != nil {
		logger.Printf("migrate: %v", err)
		return 1
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	lc := liercd.NewRouted(liercd.Instances{Main: cfg.LiercdHost, Next: cfg.LiercdNextHost}, d.LookupLiercd)
	s := stream.New(cfg, d, lc)
	go s.Run(ctx)
	// the logger's messages, liercd's status lines among them, as Postgres
	// NOTIFYs on "logged"
	go d.ListenStream(ctx, 5*time.Second, s.Deliver, func(err error) {
		logger.Printf("stream listener: %v (retrying in 5s)", err)
	})
	// The whole API, the event stream included; /stats and the /admin/ API
	// are on the admin listener only.
	srv := newServer(proxy.Wrap(cfg.TrustedProxies, api.New(cfg, d, s, lc)), serverTimeouts)
	srv.RegisterOnShutdown(s.CloseStreams)
	adminMux := http.NewServeMux()
	adminMux.Handle("GET /stats", s.Admin())
	adminMux.Handle("/admin/", adminapi.New(d, s.ConnectionChanged, adminapi.Streaming(s.HasStreams)))
	admin := newServer(proxy.AccessLog(adminMux), serverTimeouts)

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Printf("listen: %v", err)
		return 1
	}
	adminLn, err := net.Listen("tcp", cfg.AdminListen)
	if err != nil {
		ln.Close()
		logger.Printf("admin listen: %v", err)
		return 1
	}
	logger.Printf("lierc-api listening on %s, admin on %s", ln.Addr(), adminLn.Addr())
	if onListen != nil {
		onListen(ln.Addr(), adminLn.Addr())
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.Serve(ln) }()
	go func() { serveErr <- admin.Serve(adminLn) }()

	code := 0
	select {
	case <-ctx.Done():
	case err := <-serveErr:
		logger.Printf("serve: %v", err)
		code = 1
	}
	cancel()

	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	admin.Close()
	if err := srv.Shutdown(sctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			logger.Printf("shutdown: %v", err)
		}
		srv.Close()
	}
	return code
}

// timeouts are the http.Server timeouts both listeners get.
type timeouts struct{ ReadHeader, Read, Idle time.Duration }

var serverTimeouts = timeouts{ReadHeader: 10 * time.Second, Read: 30 * time.Second, Idle: 120 * time.Second}

// newServer serves h with t's timeouts and no WriteTimeout, which would end
// every event stream and date search. ReadTimeout doesn't cut long
// responses short: net/http clears the read deadline before its
// background read once the request has been read.
func newServer(h http.Handler, t timeouts) *http.Server {
	return &http.Server{
		Handler:           h,
		ReadHeaderTimeout: t.ReadHeader,
		ReadTimeout:       t.Read,
		IdleTimeout:       t.Idle,
	}
}
