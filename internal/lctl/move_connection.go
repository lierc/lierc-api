package lctl

import (
	"context"
	"fmt"
	"net/http"

	"github.com/lierc/lierc-api/internal/liercd"
)

const moveConnectionUsage = `move-connection ID main|next

Moves a connection to another liercd instance: destroys it on the instance it
is on now, records the new instance, then creates it there from its saved
config. "next" needs LIERCD_NEXT_HOST. It refuses when the connection is
already running on the target instance, or that instance can't be reached.

Move every connection on next back to main before rolling lierc-api back past
the liercd-next change or stopping liercd-next, or they run on both.`

func moveConnection(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "move-connection", moveConnectionUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return &UsageError{Msg: "give a connection id and main or next"}
	}
	id, target := fs.Arg(0), fs.Arg(1)
	if target != "main" && target != "next" {
		return &UsageError{Msg: fmt.Sprintf("target must be main or next, not %q", target)}
	}
	return moveOne(ctx, env, id, target)
}

// moveOne moves connection id to liercd instance target ("main" or
// "next"), printing what it did.
func moveOne(ctx context.Context, env *Env, id, target string) error {
	config, ok, err := env.DB.LookupConfig(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown connection %s", id)
	}
	source, err := env.DB.LookupLiercd(ctx, id)
	if err != nil {
		return err
	}
	if source == "" {
		source = "main"
	}
	if source == target {
		return fmt.Errorf("%s is already on %s", id, target)
	}
	if target == "next" && env.Config.LiercdNextHost == "" {
		return fmt.Errorf("can't move %s to next: LIERCD_NEXT_HOST is not set", id)
	}
	src, err := env.Liercd.For(source)
	if err != nil {
		return err
	}
	dst, err := env.Liercd.For(target)
	if err != nil {
		return err
	}
	// Both ends are asked by host, not through the column: the stream
	// server's recreate-on-open reads the column too, so a connection could
	// otherwise be created on the old instance while it changes.
	res, err := dst.Do(ctx, http.MethodGet, id+"/status", nil)
	if err != nil {
		return fmt.Errorf("liercd instance %s isn't reachable: %v", target, err)
	}
	switch res.Status {
	case http.StatusOK:
		return fmt.Errorf("%s is already running on %s", id, target)
	case http.StatusNotFound:
	default:
		return fmt.Errorf("liercd instance %s isn't reachable: liercd said %s", target, statusLine(res.Status))
	}
	if problem, _ := destroyOn(ctx, src, id); problem != "" {
		return fmt.Errorf("destroying %s on %s: liercd said %s", id, source, problem)
	}
	stored := target
	if target == "main" {
		stored = ""
	}
	if err := env.DB.SetLiercd(ctx, id, stored); err != nil {
		return err
	}
	// A stream that opened between the destroy and the column change
	// recreated it on the old instance; destroy it there again.
	problem, wasRunning := destroyOn(ctx, src, id)
	if problem != "" {
		fmt.Fprintf(env.Stdout, "%s is now on %s, but destroying it again on %s failed: liercd said %s; check it isn't running there, then run lctl reconnect %s\n", id, target, source, problem, id)
		return fmt.Errorf("destroying %s on %s again: liercd said %s", id, source, problem)
	}
	if wasRunning {
		fmt.Fprintf(env.Stdout, "a recreate on %s slipped in during the move; destroyed it there\n", source)
	}
	if err := createOn(ctx, dst, id, config); err != nil {
		fmt.Fprintf(env.Stdout, "%s is now on %s, but creating it there failed: %v; it will be created when its user's stream next opens, or run lctl reconnect %s\n", id, target, err, id)
		return err
	}
	fmt.Fprintf(env.Stdout, "moved %s from %s to %s\n", id, source, target)
	return nil
}

// destroyOn destroys id on one instance. problem is "" for 200 or 404;
// running is whether it was running (200).
func destroyOn(ctx context.Context, c *liercd.Client, id string) (problem string, running bool) {
	res, err := c.Do(ctx, http.MethodPost, id+"/destroy", nil)
	if err != nil {
		return err.Error(), false
	}
	switch res.Status {
	case http.StatusOK:
		return "", true
	case http.StatusNotFound:
		return "", false
	}
	return statusLine(res.Status), false
}
