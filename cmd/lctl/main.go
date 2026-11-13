// Command lctl runs lierc-api's admin commands (lctl help lists them). The
// admin ones talk to lierc-api's admin listener; archive, revive and
// delete-orphan-logs use the database directly.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc-api/internal/lctl"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := lctl.Main(ctx, os.Args[1:], lctl.OSSystem())
	stop()
	os.Exit(code)
}
