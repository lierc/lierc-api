package lctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/connconfig"
)

const checkConnectionsUsage = `check-connections [--delete [--delete-unresolved]] [--no-dns] [--settle SECONDS] [ID...]

Checks saved IRC connections against the rules the API applies when a
connection is created or edited (field formats, and hosts that resolve to
loopback or private addresses), and prints each invalid one with the reason.
Checks every connection, or only the ids given as arguments or one per line
on stdin. Without --delete it's a dry run and changes nothing.

A host that doesn't resolve may be a passing DNS failure, so it's reported
but not deleted unless --delete-unresolved is also given. Valid connections
are never deleted. --delete with --no-dns needs connection ids.

Deleting a connection destroys it in liercd first, so it stops running. If
liercd can't be reached or refuses (anything but 200, or 404 for a
connection that isn't running), that connection is skipped. Destroying makes
it QUIT, and that message reaches the log through NSQ a moment later, so
the command waits --settle seconds, then deletes each connection's log,
seen, private and connection rows in one transaction, and finally sweeps log
rows that arrived late.`

type checkedConn struct {
	id, user, username, where, why string
	unresolved                     bool
}

func checkConnections(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "check-connections", checkConnectionsUsage)
	del := fs.Bool("delete", false, "delete the invalid connections")
	unresolved := fs.Bool("delete-unresolved", false, "with --delete, also delete connections whose host doesn't resolve")
	noDNS := fs.Bool("no-dns", false, "check field formats only, not where hosts resolve")
	settle := fs.Int("settle", 5, "seconds to wait for QUIT messages to reach the log before deleting")
	if err := Parse(fs, args); err != nil {
		return err
	}
	ids, err := ReadIDs(env, fs.Args())
	if errors.Is(err, ErrNoIDsOnStdin) {
		// e.g. `grep … | lctl check-connections --delete` that matched nothing:
		// never fall back to checking every connection
		return &UsageError{Msg: "no ids on stdin; to check every connection, run it on a terminal (over ssh: ssh -t)"}
	}
	if err != nil {
		return err
	}
	switch {
	case *settle < 0:
		return &UsageError{Msg: "--settle needs a number of seconds"}
	case *unresolved && !*del:
		return &UsageError{Msg: "--delete-unresolved needs --delete"}
	case *del && *noDNS && len(ids) == 0:
		return &UsageError{Msg: "--delete with --no-dns would skip the address check; pass connection ids or drop --no-dns"}
	}
	out := env.Stdout

	q := `SELECT c.id, c."user", c.config::text, u.username FROM connection c
	      LEFT JOIN "user" u ON u.id = c."user"`
	var qargs []any
	if len(ids) > 0 {
		q += ` WHERE c.id = ANY($1)`
		qargs = append(qargs, ids)
	}
	rows, err := env.DB.Pool.Query(ctx, q+` ORDER BY c.id`, qargs...)
	if err != nil {
		return err
	}
	type row struct {
		id, user, config string
		username         *string
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.user, &r.config, &r.username); err != nil {
			rows.Close()
			return err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	found := map[string]bool{}
	for _, r := range all {
		found[r.id] = true
	}
	for _, id := range ids {
		if !found[id] {
			fmt.Fprintf(out, "%s: no such connection\n", id)
		}
	}

	valid := 0
	var invalid []checkedConn
	for _, r := range all {
		why := connectionProblem(ctx, env, []byte(r.config), !*noDNS)
		if why == "" {
			valid++
			continue
		}
		c := checkedConn{id: r.id, user: r.user, username: "no such user", where: hostPort(r.config), why: why,
			unresolved: strings.HasPrefix(why, "Couldn't find host")}
		if r.username != nil {
			c.username = *r.username
		}
		fmt.Fprintf(out, "%s (user %s, %s, %s): %s\n", c.id, c.user, c.username, c.where, c.why)
		invalid = append(invalid, c)
	}
	note := ""
	if *noDNS {
		note = " (field formats only, no DNS)"
	}
	fmt.Fprintf(out, "%d connection(s) checked: %d valid, %d invalid%s\n", len(all), valid, len(invalid), note)

	if !*del {
		if len(invalid) > 0 {
			fmt.Fprintln(out, "dry run: nothing deleted; run with --delete to delete the invalid ones")
		}
		return nil
	}

	var targets []string
	kept := 0
	for _, c := range invalid {
		if c.unresolved && !*unresolved {
			kept++
			continue
		}
		targets = append(targets, c.id)
	}
	if kept > 0 {
		fmt.Fprintf(out, "%d unresolved host(s) kept; add --delete-unresolved to delete them\n", kept)
	}
	if len(targets) == 0 {
		return nil
	}

	deleted, skipped, err := destroyAndDelete(ctx, env, targets, time.Duration(*settle)*time.Second,
		connectionTables, connectionWhere)
	fmt.Fprintf(out, "deleted %d connection(s), skipped %d\n", deleted, skipped)
	if err != nil {
		return err
	}
	if skipped > 0 {
		return fmt.Errorf("%d connection(s) skipped", skipped)
	}
	return nil
}

// connectionTables hold a connection's data, in delete order.
var connectionTables = []string{"log", "seen", "private", "connection"}

func connectionWhere(table string) string {
	if table == "connection" {
		return "id = $1"
	}
	return "connection = $1"
}

// destroyAndDelete destroys each connection in liercd (200 or 404 is fine,
// anything else skips it), waits settle for QUIT messages to reach the log,
// then deletes each connection's rows from tables in one transaction per
// connection and sweeps log rows that arrived late.
func destroyAndDelete(ctx context.Context, env *Env, ids []string, settle time.Duration,
	tables []string, where func(table string) string) (deleted, skipped int, err error) {
	out := env.Stdout
	var ready []string
	for _, id := range ids {
		if problem := destroy(ctx, env, id); problem != "" {
			fmt.Fprintf(out, "%s: liercd said %s, skipped\n", id, problem)
			skipped++
			continue
		}
		ready = append(ready, id)
	}
	if len(ready) == 0 {
		return 0, skipped, nil
	}
	if err := env.Sleep(ctx, settle); err != nil {
		return 0, skipped, err
	}

	var done []string
	for _, id := range ready {
		if err := ctx.Err(); err != nil {
			return len(done), skipped, err
		}
		counts, err := deleteRows(ctx, env, id, tables, where)
		if err != nil {
			fmt.Fprintf(out, "%s: failed, nothing deleted: %v\n", id, err)
			skipped++
			continue
		}
		parts := make([]string, len(tables))
		for i, t := range tables {
			parts[i] = fmt.Sprintf("%s=%d", t, counts[i])
		}
		fmt.Fprintf(out, "%s: deleted (%s)\n", id, strings.Join(parts, " "))
		done = append(done, id)
	}

	if len(done) > 0 {
		tag, err := env.DB.Pool.Exec(ctx, `DELETE FROM log WHERE connection = ANY($1)`, done)
		if err != nil {
			return len(done), skipped, err
		}
		if n := tag.RowsAffected(); n > 0 {
			fmt.Fprintf(out, "removed %d late log row(s)\n", n)
		}
		if _, err := env.DB.Pool.Exec(ctx, `DELETE FROM log_archive WHERE connection = ANY($1)`, done); err != nil {
			return len(done), skipped, err
		}
	}
	return len(done), skipped, nil
}

func deleteRows(ctx context.Context, env *Env, id string, tables []string, where func(string) string) ([]int64, error) {
	tx, err := env.DB.Pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	counts := make([]int64, len(tables))
	for i, t := range tables {
		tag, err := tx.Exec(ctx, `DELETE FROM `+quoteTable(t)+` WHERE `+where(t), id)
		if err != nil {
			return nil, err
		}
		counts[i] = tag.RowsAffected()
		if t == "log" {
			// after the log delete, so a block the archiver moved in between is caught
			if _, err := tx.Exec(ctx, `DELETE FROM log_archive WHERE `+where(t), id); err != nil {
				return nil, err
			}
		}
	}
	return counts, tx.Commit(ctx)
}

func quoteTable(t string) string {
	if t == "user" {
		return `"user"`
	}
	return t
}

// destroy stops a connection in liercd; it returns "" on 200 or 404, or a
// description of what went wrong.
func destroy(ctx context.Context, env *Env, id string) string {
	res, err := env.Liercd.Do(ctx, http.MethodPost, id+"/destroy", nil)
	if err != nil {
		return err.Error()
	}
	if res.Status == http.StatusOK || res.Status == http.StatusNotFound {
		return ""
	}
	return statusLine(res.Status)
}

func statusLine(code int) string { return fmt.Sprintf("%d %s", code, http.StatusText(code)) }

// connectionProblem is why a saved config is invalid, or "".
func connectionProblem(ctx context.Context, env *Env, raw []byte, dns bool) string {
	v, err := connconfig.Decode(raw)
	if _, ok := v.(map[string]any); err != nil || !ok {
		return "config isn't a JSON object"
	}
	config, err := connconfig.Clean(raw)
	if err != nil {
		return err.Error()
	}
	if !dns {
		return ""
	}
	if err := connconfig.CheckHost(ctx, env.Getenv, env.Resolve, config["Host"].(string), config["Port"].(int)); err != nil {
		return err.Error()
	}
	return ""
}

// hostPort is a saved config's "Host:Port" as stored, "?" for a missing or
// non-scalar part, or "?" if it isn't an object.
func hostPort(raw string) string {
	v, _ := connconfig.Decode([]byte(raw))
	params, ok := v.(map[string]any)
	if !ok {
		return "?"
	}
	part := func(k string) string {
		if s, ok := connconfig.Display(params[k]); ok {
			return s
		}
		return "?"
	}
	return part("Host") + ":" + part("Port")
}
