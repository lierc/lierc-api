package lctl

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

const reviveUsage = `revive [--since DURATION]

For users who logged in within --since (default 48h), starts every saved
connection liercd isn't running, e.g. after a liercd restart. Connections
liercd is already running are left alone.`

func revive(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "revive", reviveUsage)
	since := fs.String("since", "48h", "how recently the user logged in (a Go duration: 48h, 90m)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	d, err := time.ParseDuration(*since)
	if err != nil || d <= 0 {
		return &UsageError{Msg: "--since needs a duration such as 48h"}
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "revive takes no arguments"}
	}

	rows, err := env.DB.Pool.Query(ctx, `SELECT c.id, c.config::text FROM connection c
	  JOIN "user" u ON u.id = c."user"
	  WHERE u.last_login > now() - make_interval(secs => $1)
	  ORDER BY c.id`, d.Seconds())
	if err != nil {
		return err
	}
	type conn struct{ id, config string }
	var conns []conn
	for rows.Next() {
		var c conn
		if err := rows.Scan(&c.id, &c.config); err != nil {
			rows.Close()
			return err
		}
		conns = append(conns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	started, failed := 0, 0
	for _, c := range conns {
		if err := ctx.Err(); err != nil {
			return err
		}
		status, err := env.Liercd.Do(ctx, http.MethodGet, c.id+"/status", nil)
		if err == nil && status.Status == http.StatusOK {
			continue
		}
		res, err := env.Liercd.Do(ctx, http.MethodPost, c.id+"/create", []byte(c.config))
		switch {
		case err != nil:
			fmt.Fprintf(env.Stdout, "%s: %v\n", c.id, err)
			failed++
		case res.Status != http.StatusOK:
			fmt.Fprintf(env.Stdout, "%s: %s\n", c.id, statusLine(res.Status))
			failed++
		default:
			fmt.Fprintf(env.Stdout, "started %s\n", c.id)
			started++
		}
	}
	fmt.Fprintf(env.Stdout, "started %d, failed %d\n", started, failed)
	if failed > 0 {
		return fmt.Errorf("%d connection(s) failed to start", failed)
	}
	return nil
}
