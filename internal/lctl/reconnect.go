package lctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lierc/lierc-api/internal/liercd"
)

const reconnectUsage = `reconnect ID...

Restarts connections in liercd from their saved configs, given connection
ids as arguments or one per line on stdin: each is destroyed (fine if it
isn't running), then created again, one after another. A connection that
fails is reported and the rest carry on; the command fails if any did.`

func reconnect(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "reconnect", reconnectUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	ids, err := ReadIDs(env, fs.Args())
	if err != nil && !errors.Is(err, ErrNoIDsOnStdin) {
		return err
	}
	if len(ids) == 0 {
		return &UsageError{Msg: "give connection ids as arguments or on stdin"}
	}
	out := env.Stdout
	done, failed := 0, 0
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := reconnectOne(ctx, env, id); err != nil {
			fmt.Fprintln(out, err)
			failed++
			continue
		}
		fmt.Fprintf(out, "reconnected %s\n", id)
		done++
	}
	fmt.Fprintf(out, "reconnected %d, failed %d\n", done, failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d connection(s) failed", failed, len(ids))
	}
	return nil
}

// reconnectOne destroys connection id in liercd and creates it again from
// its saved config. A failed destroy leaves it alone.
func reconnectOne(ctx context.Context, env *Env, id string) error {
	config, ok, err := env.DB.LookupConfig(ctx, id)
	if err != nil {
		return fmt.Errorf("looking up %s: %v", id, err)
	}
	if !ok {
		return fmt.Errorf("unknown connection %s", id)
	}
	if problem := destroy(ctx, env, id); problem != "" {
		return fmt.Errorf("destroying %s: liercd said %s", id, problem)
	}
	return create(ctx, env, id, config)
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
