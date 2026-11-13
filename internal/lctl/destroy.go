package lctl

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// connectionTables hold a connection's data, in the order they are listed.
var connectionTables = []string{"log", "seen", "private", "connection"}

// destroyAndDelete destroys each connection in liercd (200 or 404 is fine,
// anything else skips it), waits settle for QUIT messages to reach the log,
// then has the API delete each connection's rows, which also sweeps log
// rows that arrived late. The log's last id is read first: that is the
// sweep's starting point, and it fails, before liercd is touched, if the
// admin server is down.
func destroyAndDelete(ctx context.Context, env *Env, ids []string, settle time.Duration) (deleted, skipped int, err error) {
	out := env.Stdout
	logAfter, err := env.Admin.LastLogID(ctx)
	if err != nil {
		return 0, 0, err
	}
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

	var late int64
	for _, id := range ready {
		if err := ctx.Err(); err != nil {
			return deleted, skipped, err
		}
		res, err := env.Admin.DeleteConnection(ctx, id, logAfter)
		if errors.Is(err, ErrMaybeDone) {
			return deleted, skipped, err
		}
		if errors.Is(err, ErrNotFound) {
			// deleted by someone else since the lookup: what we wanted
			fmt.Fprintf(out, "%s: already gone\n", id)
			deleted++
			continue
		}
		if err != nil {
			fmt.Fprintf(out, "%s: failed, nothing deleted: %v\n", id, err)
			skipped++
			continue
		}
		counts := res.Counts
		late += res.Swept
		warnSweep(env, id, res)
		parts := make([]string, len(connectionTables))
		for i, t := range connectionTables {
			parts[i] = fmt.Sprintf("%s=%d", t, counts[t])
		}
		fmt.Fprintf(out, "%s: deleted (%s)\n", id, strings.Join(parts, " "))
		deleted++
	}
	if late > 0 {
		fmt.Fprintf(out, "removed %d late log row(s)\n", late)
	}
	return deleted, skipped, nil
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

// warnSweep says on stderr that a delete committed but its sweep failed.
func warnSweep(env *Env, id string, res Deleted) {
	if res.SweepError != "" {
		fmt.Fprintf(env.Stderr, "%s: deleted, but late log rows weren't swept (%s)\n", id, res.SweepError)
	}
}
