package lctl

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/archive"
)

const archiveUsage = `archive [--move] [--keep N] [--block N] [--min-age DURATION] [--connection ID[,ID...]]
        [--pause DURATION (1s)] [--budget DURATION (30m)] [--max-blocks N]

Moves old log rows into compressed archive blocks (log_archive). Per
connection and channel it keeps the newest --keep (default 1000) messages and everything
newer than --min-age (default 7d; days such as 7d, or a Go duration such as 168h), and
moves the rest, oldest first, in full blocks of --block messages; fewer than
a block are left for later. A status window is different: its old messages
are deleted, in batches of --block, and never archived. Without --move it's
a dry run that lists the channels with a block or more to move and the
status windows with old messages to delete, without counting them (counting
reads every archivable row, which is slow on a large backlog).

--connection limits it to these connections (default all). --pause waits
after each block (default 1s; 0: no wait). --budget stops starting new blocks
after that long (default 30m; 0: no limit) and --max-blocks after that many blocks, status batches included
(0: no limit); run it again to carry on. Ctrl-C stops between blocks.

Each block gets a line: connection, channel, first-last message id, count
(status batches end in "deleted"). Run only one archive at a time.`

func archiveCmd(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "archive", archiveUsage)
	move := fs.Bool("move", false, "move the rows (otherwise only list what would move)")
	keep := fs.Int("keep", 1000, "newest messages to keep in each channel")
	block := fs.Int("block", 500, "messages per block")
	minAge := fs.String("min-age", "7d", "leave messages newer than this (7d, 168h)")
	pause := fs.Duration("pause", time.Second, "how long to wait between blocks (0: none)")
	budget := fs.Duration("budget", 30*time.Minute, "stop starting new blocks after this long (0: no limit)")
	maxBlocks := fs.Int("max-blocks", 0, "stop after this many blocks (0: no limit)")
	conns := fs.String("connection", "", "only these connection ids, comma separated (default all)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	age, ok := parseSince(*minAge)
	switch {
	case fs.NArg() != 0:
		return &UsageError{Msg: "archive takes no arguments"}
	case !ok:
		return &UsageError{Msg: "--min-age needs a duration such as 7d or 168h"}
	case *keep < 1:
		return &UsageError{Msg: "--keep must be at least 1"}
	case *block < 1:
		return &UsageError{Msg: "--block must be at least 1"}
	case *pause < 0:
		return &UsageError{Msg: "--pause can't be negative"}
	case *budget < 0:
		return &UsageError{Msg: "--budget can't be negative"}
	case *maxBlocks < 0:
		return &UsageError{Msg: "--max-blocks can't be negative"}
	}

	var ids []string
	seen := map[string]bool{}
	for _, id := range strings.Split(*conns, ",") {
		if id = strings.TrimSpace(id); id != "" && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if *conns != "" && len(ids) == 0 {
		return &UsageError{Msg: "--connection needs connection ids"}
	}
	for _, id := range ids {
		_, ok, err := env.DB.LookupConfig(ctx, id)
		if err != nil {
			return fmt.Errorf("looking up %s: %v", id, err)
		}
		if !ok {
			return &UsageError{Msg: fmt.Sprintf("unknown connection %s", id)}
		}
	}

	pool := env.DB.Pool.Pool
	opts := archive.Options{Keep: *keep, Block: *block, MinAge: age, Now: env.now(), Connections: ids}
	cands, err := archive.Candidates(ctx, pool, opts)
	if err != nil {
		return err
	}
	out := env.Stdout

	if !*move {
		channels, windows := 0, 0
		for _, c := range cands {
			if c.Status {
				fmt.Fprintf(out, "%s %s to delete\n", c.Connection, c.Channel)
				windows++
				continue
			}
			fmt.Fprintf(out, "%s %s\n", c.Connection, c.Channel)
			channels++
		}
		fmt.Fprintf(out, "would archive %d channel(s); would delete from %d status window(s)\n", channels, windows)
		if len(cands) > 0 {
			fmt.Fprintln(out, "run again with --move to do it")
		}
		return nil
	}

	blocks, msgs, status := 0, 0, 0
	totals := func() {
		fmt.Fprintf(out, "moved %d block(s), %d message(s); deleted %d status message(s)\n", blocks, msgs, status)
	}
	started := env.now()
	done := 0
	var stopErr error
	stopped := ""
loop:
	for _, c := range cands {
		for {
			if err := ctx.Err(); err != nil {
				stopErr, stopped = err, "interrupted"
				break loop
			}
			now := env.now()
			if *budget > 0 && now.Sub(started) >= *budget {
				stopped = "budget spent"
				break loop
			}
			o := opts
			o.Now = now
			var b archive.Block
			var more bool
			var err error
			if c.Status {
				b, more, err = archive.DropStatus(ctx, pool, c, o)
			} else {
				b, more, err = archive.MoveBlock(ctx, pool, c, o)
			}
			if err != nil {
				fmt.Fprintf(out, "stopped at %s %s: %v\n", c.Connection, c.Channel, err)
				totals()
				return err
			}
			if !more {
				break
			}
			done++
			if c.Status {
				fmt.Fprintf(out, "%s %s %d-%d %d deleted\n", b.Connection, b.Channel, b.FirstID, b.LastID, b.Count)
				status += b.Count
			} else {
				fmt.Fprintf(out, "%s %s %d-%d %d\n", b.Connection, b.Channel, b.FirstID, b.LastID, b.Count)
				blocks++
				msgs += b.Count
			}
			if *maxBlocks > 0 && done >= *maxBlocks {
				stopped = fmt.Sprintf("--max-blocks %d reached", *maxBlocks)
				break loop
			}
			// counts are capped at a block, so it can't tell which block
			// is the last; it pauses after each one
			if *pause > 0 {
				if err := env.Sleep(ctx, *pause); err != nil {
					stopErr, stopped = err, "interrupted"
					break loop
				}
			}
		}
	}
	if stopped != "" {
		fmt.Fprintf(out, "stopped: %s\n", stopped)
	}
	totals()
	return stopErr
}
