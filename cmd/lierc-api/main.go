// Command lierc-api runs the SSE event stream (`lierc-api stream`) and the
// admin commands (`lierc-api lctl COMMAND`). The JSON API is still
// bin/api.psgi.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/lctl"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/proxy"
	"github.com/lierc/lierc-api/internal/stream"
)

// shutdownTimeout bounds the graceful drain: closing streams saves their
// channels and last_login.
const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "lctl" {
		os.Exit(lctl.Main(ctx, os.Args[2:], lctl.OSSystem()))
	}
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stderr, nil))
}

// run executes one command and returns the process exit code. onListen, if
// non-nil, is called with the stream and admin addresses once both are bound.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, onListen func(stream, admin net.Addr)) int {
	if len(args) != 1 || args[0] != "stream" {
		fmt.Fprintln(stderr, "usage: lierc-api stream | lierc-api lctl COMMAND")
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)

	cfg, err := config.Load(getenv)
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

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nsqErr := make(chan error, 1)

	s := stream.New(cfg, d, liercd.NewRouted(liercd.Instances{Main: cfg.LiercdHost, Next: cfg.LiercdNextHost}, d.LookupLiercd))
	go s.Run(ctx)
	go func() {
		if err := s.Consume(ctx, cfg.NSQAddr); err != nil && ctx.Err() == nil {
			nsqErr <- err
		}
	}()
	// Every path is the event stream; /stats is on the admin listener.
	srv := &http.Server{Handler: proxy.Wrap(cfg.TrustedProxies, s)}
	srv.RegisterOnShutdown(s.CloseStreams)
	admin := &http.Server{Handler: s.Admin()}

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
	logger.Printf("lierc-api stream listening on %s, admin on %s", ln.Addr(), adminLn.Addr())
	if onListen != nil {
		onListen(ln.Addr(), adminLn.Addr())
	}

	serveErr := make(chan error, 2)
	go func() { serveErr <- srv.Serve(ln) }()
	go func() { serveErr <- admin.Serve(adminLn) }()

	code := 0
	select {
	case <-ctx.Done():
	case err := <-nsqErr:
		logger.Printf("nsq: %v", err)
		code = 1
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
