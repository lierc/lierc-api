// Command lierc-api runs the JSON API (`lierc-api api`) or the SSE event
// stream (`lierc-api stream`).
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

	"github.com/lierc/lierc-api/internal/api"
	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/mail"
	"github.com/lierc/lierc-api/internal/proxy"
	"github.com/lierc/lierc-api/internal/session"
	"github.com/lierc/lierc-api/internal/stream"
)

// shutdownTimeout bounds the graceful drain; SSE streams never end on their own.
const shutdownTimeout = 10 * time.Second

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Getenv, os.Stderr, nil))
}

// run executes one command and returns the process exit code. onListen, if
// non-nil, is called with the bound address once the server is accepting.
func run(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer, onListen func(net.Addr)) int {
	if len(args) != 1 || (args[0] != "api" && args[0] != "stream") {
		fmt.Fprintln(stderr, "usage: lierc-api api|stream")
		return 2
	}
	mode := args[0]
	logger := log.New(stderr, "", log.LstdFlags)

	cfg, err := config.Load(getenv, mode)
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

	lc := liercd.New(cfg.LiercdHost)
	codec := session.Codec{Secret: cfg.Secret, Secure: cfg.Secure}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	nsqErr := make(chan error, 1)

	var handler http.Handler
	if mode == "api" {
		handler = api.New(cfg, d, lc, mail.New(cfg.SMTP, cfg.FromAddress, cfg.NoSMTP))
	} else {
		s := stream.New(cfg, d, lc)
		go s.Run(ctx)
		go func() {
			if err := s.Consume(ctx, cfg.NSQAddr); err != nil && ctx.Err() == nil {
				nsqErr <- err
			}
		}()
		handler = s
	}
	// No ServeMux here: it would clean paths such as /notification/web_push/https://x/y.
	srv := &http.Server{Handler: proxy.Wrap(cfg.TrustedProxies, codec.Middleware(handler))}

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		logger.Printf("listen: %v", err)
		return 1
	}
	logger.Printf("lierc-api %s listening on %s", mode, ln.Addr())
	if onListen != nil {
		onListen(ln.Addr())
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	code := 0
	select {
	case <-ctx.Done():
	case err := <-nsqErr:
		logger.Printf("nsq: %v", err)
		code = 1
	case err := <-serveErr:
		logger.Printf("serve: %v", err)
		return 1
	}
	cancel()

	sctx, scancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer scancel()
	if err := srv.Shutdown(sctx); err != nil {
		if !errors.Is(err, context.DeadlineExceeded) {
			logger.Printf("shutdown: %v", err)
		}
		srv.Close() // long-lived SSE streams outlast the grace period
	}
	return code
}
