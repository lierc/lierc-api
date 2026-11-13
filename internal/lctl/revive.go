package lctl

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const reviveUsage = `revive [--since DURATION] [--instance main|next] [--batch N] [--pause DURATION] [--wait DURATION]

For users who logged in within --since (default 48h), starts every saved
connection liercd isn't running, e.g. after a liercd restart. Connections
liercd is already running are left alone. With --instance, only that
liercd's connections (main or next).

Starts are paced, a batch at a time with a pause between batches: many
connections at once to one network from one address can trip its
connection throttling. A failed start is reported and the rest carry on.

With --wait (it needs --instance), it first waits up to that long for the
instance's command API to answer, as after a restart; the liercd units
run it this way from ExecStartPost.`

// reviveProbe is how often --wait tries the instance's command API.
const reviveProbe = 2 * time.Second

func revive(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "revive", reviveUsage)
	since := fs.String("since", "48h", "how recently the user logged in (a Go duration: 48h, 90m)")
	instance := fs.String("instance", "", "only this liercd's connections: main or next")
	batch := fs.Int("batch", 5, "connections to start before each pause")
	pause := fs.Duration("pause", 30*time.Second, "how long to wait between batches")
	wait := fs.Duration("wait", 0, "wait up to this long for the instance to answer first")
	if err := Parse(fs, args); err != nil {
		return err
	}
	d, err := time.ParseDuration(*since)
	if err != nil || d <= 0 {
		return &UsageError{Msg: "--since needs a duration such as 48h"}
	}
	switch {
	case fs.NArg() != 0:
		return &UsageError{Msg: "revive takes no arguments"}
	case *instance != "" && *instance != "main" && *instance != "next":
		return &UsageError{Msg: fmt.Sprintf("instances are main or next, not %q", *instance)}
	case *batch < 1:
		return &UsageError{Msg: "--batch must be at least 1"}
	case *pause < 0:
		return &UsageError{Msg: "--pause can't be negative"}
	case *wait < 0:
		return &UsageError{Msg: "--wait can't be negative"}
	case *wait > 0 && *instance == "":
		return &UsageError{Msg: "--wait needs --instance"}
	}

	if *wait > 0 {
		if err := waitForInstance(ctx, env, *instance, *wait); err != nil {
			return err
		}
	}

	conns, err := env.Store.ReviveCandidates(ctx, d, *instance)
	if err != nil {
		return err
	}

	started, failed := 0, 0
	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, err := env.Liercd.Do(ctx, http.MethodGet, c.ID+"/status", nil)
		if err == nil && status.Status == http.StatusOK {
			continue
		}
		// pause before each batch after the first; connections already
		// running don't count
		if n := started + failed; n > 0 && n%*batch == 0 {
			fmt.Fprintf(env.Stdout, "pausing %s\n", *pause)
			if err := env.Sleep(ctx, *pause); err != nil {
				return err
			}
		}
		res, err := env.Liercd.Do(ctx, http.MethodPost, c.ID+"/create", []byte(c.Config))
		switch {
		case err != nil:
			fmt.Fprintf(env.Stdout, "%s: %v\n", c.ID, err)
			failed++
		case res.Status != http.StatusOK:
			fmt.Fprintf(env.Stdout, "%s: %s\n", c.ID, statusLine(res.Status))
			failed++
		default:
			fmt.Fprintf(env.Stdout, "started %s\n", c.ID)
			started++
		}
	}
	fmt.Fprintf(env.Stdout, "started %d, failed %d\n", started, failed)
	if failed > 0 {
		return fmt.Errorf("%d connection(s) failed to start", failed)
	}
	return nil
}

// waitForInstance waits up to wait for instance's command API to answer
// at all (any HTTP status), trying every reviveProbe.
func waitForInstance(ctx context.Context, env *Env, instance string, wait time.Duration) error {
	lc, err := env.Liercd.For(instance)
	if err != nil {
		return err
	}
	for waited := time.Duration(0); ; waited += reviveProbe {
		_, err := lc.Do(ctx, http.MethodGet, "portmap", nil)
		if err == nil {
			return nil
		}
		if waited >= wait {
			return fmt.Errorf("liercd %s didn't answer within %s: %v", instance, wait, err)
		}
		if err := env.Sleep(ctx, reviveProbe); err != nil {
			return err
		}
	}
}
