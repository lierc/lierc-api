// Command lctl runs lierc-api's admin commands (lctl help lists them). The
// admin ones talk to lierc-api's admin listener; archive and revive use
// the database directly.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/lctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	sys := lctl.OSSystem()
	getenv, err := config.WithFiles(os.Environ(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "lctl: %v\n", err)
		os.Exit(1)
	}
	sys.Getenv = getenv
	code := lctl.Main(ctx, os.Args[1:], sys)
	stop()
	os.Exit(code)
}
