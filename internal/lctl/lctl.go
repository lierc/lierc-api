// Package lctl is lierc-api's admin command (`lierc-api lctl COMMAND`), run
// in production by the host's /usr/local/bin/lctl wrapper in a one-off
// container with the services' environment and host network. Each command
// lives in its own file; this one holds what they share. Anything that
// deletes is a dry run unless given --delete.
package lctl

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/term"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/connconfig"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
)

// System is the process's environment, input and output.
type System struct {
	Getenv          func(string) string
	Stdin           io.Reader
	Stdout, Stderr  io.Writer
	StdinIsTerminal bool
	StdinFd         int
}

// OSSystem is the real process's System.
func OSSystem() System {
	fd := int(os.Stdin.Fd())
	return System{
		Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		StdinIsTerminal: term.IsTerminal(fd), StdinFd: fd,
	}
}

// Env is what every command gets.
type Env struct {
	Config          config.Config
	Getenv          func(string) string
	DB              *db.DB
	Liercd          *liercd.Client
	StatsURL        string // API_STATS, default http://127.0.0.1:5003/stats
	Stdin           io.Reader
	Stdout, Stderr  io.Writer
	StdinIsTerminal bool
	// ReadPassword reads a password from the terminal without echo.
	ReadPassword func() (string, error)
	// Resolve resolves host names for the connection address check.
	Resolve connconfig.Resolver
	// Sleep waits d, returning ctx.Err() if ctx ends first.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now is the time; nil means time.Now.
	Now func() time.Time
}

func (e *Env) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Command is one lctl subcommand.
type Command struct {
	Name, Summary string
	Run           func(ctx context.Context, env *Env, args []string) error
}

// UsageError is a bad command line; lctl exits 2. An empty Msg means the
// flag package already printed the problem.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Opener builds the Env for a command; the func it returns releases it.
type Opener func(ctx context.Context, sys System) (*Env, func(), error)

// Main runs lctl with args (without "lctl") and returns the exit code.
func Main(ctx context.Context, args []string, sys System) int {
	return mainWith(ctx, args, sys, Commands, OpenEnv)
}

func mainWith(ctx context.Context, args []string, sys System, cmds []Command, open Opener) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		help(sys.Stdout, cmds)
		return 0
	}
	var cmd *Command
	for i := range cmds {
		if cmds[i].Name == args[0] {
			cmd = &cmds[i]
		}
	}
	if cmd == nil {
		fmt.Fprintf(sys.Stderr, "lctl: unknown command %s\n", args[0])
		help(sys.Stderr, cmds)
		return 2
	}
	env, release, err := open(ctx, sys)
	if err != nil {
		fmt.Fprintf(sys.Stderr, "lctl %s: %v\n", cmd.Name, err)
		return 1
	}
	defer release()

	err = cmd.Run(ctx, env, args[1:])
	var usage *UsageError
	switch {
	case err == nil, errors.Is(err, flag.ErrHelp):
		return 0
	case errors.As(err, &usage):
		if usage.Msg != "" {
			fmt.Fprintf(sys.Stderr, "lctl %s: %s\n", cmd.Name, usage.Msg)
		}
		fmt.Fprintf(sys.Stderr, "run 'lctl %s -h' for usage\n", cmd.Name)
		return 2
	default:
		fmt.Fprintf(sys.Stderr, "lctl %s: %v\n", cmd.Name, err)
		return 1
	}
}

func help(w io.Writer, cmds []Command) {
	fmt.Fprintln(w, "usage: lctl COMMAND [options] [args]   (lctl COMMAND -h for a command's usage)")
	fmt.Fprintln(w)
	width := 0
	for _, c := range cmds {
		width = max(width, len(c.Name))
	}
	for _, c := range cmds {
		fmt.Fprintf(w, "  %-*s  %s\n", width, c.Name, c.Summary)
	}
}

// OpenEnv builds the real Env from the environment the services share
// (/etc/lierc.env in production). The database name is POSTGRES_DB (default
// lierc), as the Perl API reads it.
func OpenEnv(ctx context.Context, sys System) (*Env, func(), error) {
	cfg, err := config.Load(sys.Getenv)
	if err != nil {
		return nil, nil, err
	}
	if name := sys.Getenv("POSTGRES_DB"); name != "" {
		cfg.DBName = name
	}
	cfg.DBMaxConns = 4
	d, err := db.Open(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	stats := sys.Getenv("API_STATS")
	if stats == "" {
		stats = "http://127.0.0.1:5003/stats"
	}
	env := &Env{
		Config: cfg, Getenv: sys.Getenv, DB: d, Liercd: liercd.New(cfg.LiercdHost), StatsURL: stats,
		Stdin: sys.Stdin, Stdout: sys.Stdout, Stderr: sys.Stderr, StdinIsTerminal: sys.StdinIsTerminal,
		Resolve: connconfig.SystemResolver, Sleep: sleepCtx,
		ReadPassword: func() (string, error) {
			fmt.Fprint(sys.Stderr, "New password: ")
			b, err := term.ReadPassword(sys.StdinFd)
			fmt.Fprintln(sys.Stderr)
			return string(b), err
		},
	}
	return env, d.Close, nil
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// NewFlags returns a FlagSet for a command whose -h prints
// "usage: lctl <usage>" and the flags to stderr.
func NewFlags(env *Env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "usage: lctl %s\n", usage)
		var any bool
		fs.VisitAll(func(*flag.Flag) { any = true })
		if any {
			fmt.Fprintln(env.Stderr, "\noptions:")
			fs.PrintDefaults()
		}
	}
	return fs
}

// Parse parses args, returning flag.ErrHelp for -h and a UsageError for a
// bad flag (already reported by the flag package).
func Parse(fs *flag.FlagSet, args []string) error {
	err := fs.Parse(args)
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return err
	}
	return &UsageError{}
}

// ReadIDs returns args if there are any, otherwise (when stdin isn't a
// terminal) the trimmed, non-blank lines of stdin.
func ReadIDs(env *Env, args []string) []string {
	if len(args) > 0 {
		return args
	}
	if env.StdinIsTerminal || env.Stdin == nil {
		return nil
	}
	var ids []string
	sc := bufio.NewScanner(env.Stdin)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			ids = append(ids, line)
		}
	}
	return ids
}

// sorted returns the keys of m in order.
func sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func isNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }
