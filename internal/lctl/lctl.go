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

// OSSystem is the real process's System. The host wrapper sets
// LCTL_STDIN_TTY=1 when the operator's stdin is a terminal but it can't give
// the container one (stdout piped or redirected); lctl then treats stdin as
// a terminal and doesn't wait for ids on it.
func OSSystem() System {
	fd := int(os.Stdin.Fd())
	return System{
		Getenv: os.Getenv, Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr,
		StdinIsTerminal: term.IsTerminal(fd) || os.Getenv("LCTL_STDIN_TTY") == "1", StdinFd: fd,
	}
}

// Env is what every command gets.
type Env struct {
	Config          config.Config
	Getenv          func(string) string
	Store           db.Store // nil unless the command has NeedsDB
	Liercd          *liercd.Client
	Admin           *Admin // the API's admin server (API_ADMIN)
	StatsURL        string // API_STATS, default <API_ADMIN>/stats
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
	// NeedsDB is whether the command uses Env.Store. The others talk to
	// the API's admin server and liercd, and run with Postgres unreachable.
	NeedsDB bool
}

// UsageError is a bad command line; lctl exits 2. An empty Msg means the
// flag package already printed the problem.
type UsageError struct{ Msg string }

func (e *UsageError) Error() string { return e.Msg }

// Opener builds the Env for a command; the func it returns releases it.
// The Env has a Store only if needsDB.
type Opener func(ctx context.Context, sys System, needsDB bool) (*Env, func(), error)

// Main runs lctl with args (without "lctl") and returns the exit code.
func Main(ctx context.Context, args []string, sys System) int {
	return mainGroups(ctx, args, sys, Commands, AdminCommands, OpenEnv)
}

// mainGroups runs a top-level command from cmds, or "admin NAME" from
// adminCmds.
func mainGroups(ctx context.Context, args []string, sys System, cmds, adminCmds []Command, open Opener) int {
	if len(args) == 0 || isHelp(args[0]) {
		help(sys.Stdout, cmds, adminCmds != nil)
		return 0
	}
	group, label := cmds, ""
	if args[0] == "admin" && adminCmds != nil {
		if len(args) == 1 || isHelp(args[1]) {
			adminHelp(sys.Stdout, adminCmds)
			return 0
		}
		group, label, args = adminCmds, "admin ", args[1:]
	}
	var cmd *Command
	for i := range group {
		if group[i].Name == args[0] {
			cmd = &group[i]
		}
	}
	if cmd == nil {
		if label == "" {
			for _, c := range adminCmds {
				if c.Name == args[0] {
					fmt.Fprintf(sys.Stderr, "lctl: %s is now \"lctl admin %[1]s\"\n", args[0])
					return 2
				}
			}
			fmt.Fprintf(sys.Stderr, "lctl: unknown command %s\n", args[0])
			help(sys.Stderr, cmds, adminCmds != nil)
		} else {
			fmt.Fprintf(sys.Stderr, "lctl: unknown admin command %s\n", args[0])
			adminHelp(sys.Stderr, adminCmds)
		}
		return 2
	}
	name := label + cmd.Name
	env, release, err := open(ctx, sys, cmd.NeedsDB)
	if err != nil {
		fmt.Fprintf(sys.Stderr, "lctl %s: %v\n", name, err)
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
			fmt.Fprintf(sys.Stderr, "lctl %s: %s\n", name, usage.Msg)
		}
		fmt.Fprintf(sys.Stderr, "run 'lctl %s -h' for usage\n", name)
		return 2
	default:
		fmt.Fprintf(sys.Stderr, "lctl %s: %v\n", name, err)
		return 1
	}
}

func isHelp(a string) bool { return a == "help" || a == "-h" || a == "--help" }

// help lists the top-level commands, and the admin group if there is one.
func help(w io.Writer, cmds []Command, admin bool) {
	fmt.Fprintln(w, "usage: lctl COMMAND [options] [args]   (lctl COMMAND -h for a command's usage)")
	fmt.Fprintln(w, "over ssh, use ssh -t unless you pipe ids in: ssh -t relaychat sudo lctl COMMAND")
	fmt.Fprintln(w)
	list := cmds
	if admin {
		list = append(append([]Command{}, cmds...), Command{Name: "admin", Summary: "Commands that go through the API's admin server (lctl admin help lists them)"})
	}
	printCommands(w, list)
}

func adminHelp(w io.Writer, cmds []Command) {
	fmt.Fprintln(w, "usage: lctl admin COMMAND [options] [args]   (lctl admin COMMAND -h for a command's usage)")
	fmt.Fprintln(w)
	printCommands(w, cmds)
}

func printCommands(w io.Writer, cmds []Command) {
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
// lierc), as the Perl API reads it. Only a command with NeedsDB gets a
// Store, and its liercd calls are routed by the store; the others route by
// asking the admin API.
func OpenEnv(ctx context.Context, sys System, needsDB bool) (*Env, func(), error) {
	cfg, err := config.Load(sys.Getenv)
	if err != nil {
		return nil, nil, err
	}
	if name := sys.Getenv("POSTGRES_DB"); name != "" {
		cfg.DBName = name
	}
	cfg.DBMaxConns = 4
	adminURL := strings.TrimSuffix(sys.Getenv("API_ADMIN"), "/")
	if adminURL == "" {
		adminURL = "http://127.0.0.1:5009"
	}
	stats := sys.Getenv("API_STATS")
	if stats == "" {
		stats = adminURL + "/stats"
	}
	adm := NewAdmin(adminURL)
	env := &Env{
		Config: cfg, Getenv: sys.Getenv, Liercd: liercd.NewRouted(liercd.Instances{Main: cfg.LiercdHost, Next: cfg.LiercdNextHost}, adm.instanceOf), Admin: adm, StatsURL: stats,
		Stdin: sys.Stdin, Stdout: sys.Stdout, Stderr: sys.Stderr, StdinIsTerminal: sys.StdinIsTerminal,
		Resolve: connconfig.SystemResolver, Sleep: sleepCtx,
		ReadPassword: func() (string, error) {
			if !term.IsTerminal(sys.StdinFd) {
				return "", errors.New("can't prompt for a password without a terminal (over ssh, use ssh -t); or give it as one line on stdin")
			}
			fmt.Fprint(sys.Stderr, "New password: ")
			b, err := term.ReadPassword(sys.StdinFd)
			fmt.Fprintln(sys.Stderr)
			return string(b), err
		},
	}
	release := func() {}
	if needsDB {
		d, err := db.Open(ctx, cfg)
		if err != nil {
			return nil, nil, err
		}
		env.Store, release = d, d.Close
		env.Liercd = liercd.NewRouted(liercd.Instances{Main: cfg.LiercdHost, Next: cfg.LiercdNextHost}, d.LookupLiercd)
	}
	return env, release, nil
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
// "usage: lctl <usage>" (or "lctl admin <usage>" for an admin command) and
// the flags to stderr.
func NewFlags(env *Env, name, usage string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	prefix := ""
	if adminNames[name] {
		prefix = "admin "
	}
	fs.Usage = func() {
		fmt.Fprintf(env.Stderr, "usage: lctl %s%s\n", prefix, usage)
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

// ErrNoIDsOnStdin is ReadIDs reading stdin and finding no ids.
var ErrNoIDsOnStdin = errors.New("no ids on stdin")

// ReadIDs returns args if there are any, otherwise (when stdin isn't a
// terminal) the trimmed, non-blank lines of stdin; ErrNoIDsOnStdin if
// that gave none.
func ReadIDs(env *Env, args []string) ([]string, error) {
	if len(args) > 0 {
		return args, nil
	}
	if env.StdinIsTerminal || env.Stdin == nil {
		return nil, nil
	}
	var ids []string
	sc := bufio.NewScanner(env.Stdin)
	for sc.Scan() {
		if line := strings.TrimSpace(sc.Text()); line != "" {
			ids = append(ids, line)
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("reading ids from stdin: %w", err)
	}
	if len(ids) == 0 {
		return nil, ErrNoIDsOnStdin
	}
	return ids, nil
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
