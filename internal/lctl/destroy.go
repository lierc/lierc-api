package lctl

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/db"
)

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
		tag, err := env.pool().Exec(ctx, `DELETE FROM log WHERE connection = ANY($1)`, done)
		if err != nil {
			return len(done), skipped, err
		}
		if n := tag.RowsAffected(); n > 0 {
			fmt.Fprintf(out, "removed %d late log row(s)\n", n)
		}
		if _, err := env.pool().Exec(ctx, `DELETE FROM log_archive WHERE connection = ANY($1)`, done); err != nil {
			return len(done), skipped, err
		}
	}
	return len(done), skipped, nil
}

func deleteRows(ctx context.Context, env *Env, id string, tables []string, where func(string) string) ([]int64, error) {
	tx, err := env.pool().Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(context.Background())
	// tell the stream server, once this commits, so the UI drops the panels
	if err := db.NotifyConnectionDeletes(ctx, tx, "id = $1", id); err != nil {
		return nil, err
	}
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
