package lctl

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/lierc/lierc-api/internal/liercd"
)

const reconnectUsage = `reconnect ID

Restarts a connection in liercd from its saved config: destroys it (fine if
it isn't running), then creates it again.`

func reconnect(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "reconnect", reconnectUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return &UsageError{Msg: "give one connection id"}
	}
	id := fs.Arg(0)
	config, ok, err := env.DB.LookupConfig(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("unknown connection %s", id)
	}
	if problem := destroy(ctx, env, id); problem != "" {
		return fmt.Errorf("destroying %s: liercd said %s", id, problem)
	}
	if err := create(ctx, env, id, config); err != nil {
		return err
	}
	fmt.Fprintf(env.Stdout, "reconnected %s\n", id)
	return nil
}

// create starts a connection in liercd from its config.
func create(ctx context.Context, env *Env, id string, config []byte) error {
	return createOn(ctx, env.Liercd, id, config)
}

// createOn is create through a given client (one pinned to an instance).
func createOn(ctx context.Context, c *liercd.Client, id string, config []byte) error {
	res, err := c.Do(ctx, http.MethodPost, id+"/create", config)
	if err != nil {
		return fmt.Errorf("creating %s: %v", id, err)
	}
	if res.Status != http.StatusOK {
		return fmt.Errorf("creating %s: liercd said %s: %s", id, statusLine(res.Status), strings.TrimSpace(string(res.Body)))
	}
	return nil
}
