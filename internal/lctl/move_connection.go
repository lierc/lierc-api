package lctl

import (
	"context"
	"fmt"
)

const moveConnectionUsage = `move-connection ID main|next

Moves a connection to another liercd instance: destroys it on the instance it
is on now, records the new instance, then creates it there from its saved
config. "next" needs LIERCD_NEXT_HOST.`

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
	if problem := destroy(ctx, env, id); problem != "" {
		return fmt.Errorf("destroying %s on %s: liercd said %s", id, source, problem)
	}
	stored := target
	if target == "main" {
		stored = ""
	}
	if err := env.DB.SetLiercd(ctx, id, stored); err != nil {
		return err
	}
	if err := create(ctx, env, id, config); err != nil {
		fmt.Fprintf(env.Stdout, "%s is now on %s, but creating it there failed: %v; it will be created when its user's stream next opens, or run lctl reconnect %s\n", id, target, err, id)
		return err
	}
	fmt.Fprintf(env.Stdout, "moved %s from %s to %s\n", id, source, target)
	return nil
}
