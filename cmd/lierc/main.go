// Command lierc runs one of lierc-api's two services:
//
//	lierc api      the API, the SSE event stream and the admin listener
//	lierc ingest   reads liercd's chats from NSQ: logs them, saves
//	               liercd's config changes, passes messages to the API
//	               and sends the notifications
//
// The admin commands are the separate lctl binary (cmd/lctl).
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc-api/internal/config"
)

const usage = "usage: lierc api|ingest (the admin commands are lctl)"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	// Production's secrets are files, named by NAME_FILE.
	getenv, err := config.WithFiles(os.Environ(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config: %v\n", err)
		os.Exit(1)
	}
	os.Exit(dispatch(ctx, os.Args[1:], getenv, os.Stderr))
}

// dispatch runs the command args names and returns the exit code.
func dispatch(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	switch args[0] {
	case "api":
		return runAPI(ctx, args[1:], getenv, stderr, nil)
	case "ingest":
		return runIngest(ctx, args[1:], getenv, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprintln(stderr, usage)
		return 0
	default:
		fmt.Fprintf(stderr, "lierc: unknown command %q\n%s\n", args[0], usage)
		return 2
	}
}
