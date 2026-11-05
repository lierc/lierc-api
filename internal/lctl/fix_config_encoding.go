package lctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/lierc/lierc-api/internal/connconfig"
)

const fixConfigEncodingUsage = `fix-config-encoding [--fix]

Repairs saved connection configs that the Perl API stored encoded twice:
before the fix, every non-ASCII character was saved as its UTF-8 bytes
read as Latin-1 (🍰 as "ð°"), which the logger's highlights, lctl and
anything else in Go read as is. Each string in a config that decodes this
way is replaced with what it decodes to; strings that are already right
are left alone. Without --fix it's a dry run.

Each connection to fix gets a line, then its repaired fields. Highlights
and the alias take effect at once (the logger reloads highlights); a
connection whose other fields changed says it needs lctl reconnect, as
liercd may be running it with the mangled values.`

// noReconnect are the config fields liercd doesn't use.
var noReconnect = map[string]bool{"Highlight": true, "Alias": true}

func fixConfigEncoding(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "fix-config-encoding", fixConfigEncodingUsage)
	fix := fs.Bool("fix", false, "save the repaired configs (otherwise only list them)")
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return &UsageError{Msg: "fix-config-encoding takes no arguments"}
	}

	rows, err := env.DB.Pool.Query(ctx, `SELECT c.id, coalesce(u.username, ''), c.config::text
	  FROM connection c LEFT JOIN "user" u ON u.id = c."user" ORDER BY c.id`)
	if err != nil {
		return err
	}
	type conn struct{ id, username, config string }
	var conns []conn
	for rows.Next() {
		var c conn
		if err := rows.Scan(&c.id, &c.username, &c.config); err != nil {
			rows.Close()
			return err
		}
		conns = append(conns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	out := env.Stdout
	fixed, skipped := 0, 0
	var reconnects []string
	for _, c := range conns {
		v, err := connconfig.Decode([]byte(c.config))
		if err != nil {
			continue // check-connections reports configs that aren't JSON
		}
		obj, ok := v.(map[string]any)
		if !ok {
			continue
		}
		changed := map[string][]string{}
		for k, val := range obj {
			obj[k] = repairValue(val, func(s string) { changed[k] = append(changed[k], s) })
		}
		if len(changed) == 0 {
			continue
		}
		keys := make([]string, 0, len(changed))
		reconnect := false
		for k := range changed {
			keys = append(keys, k)
			reconnect = reconnect || !noReconnect[k]
		}
		sort.Strings(keys)

		if *fix {
			repaired, err := encodeConfig(obj)
			if err != nil {
				return err
			}
			// only if the row is as it was read: a concurrent edit or
			// logger merge wins, and the next run picks it up
			tag, err := env.DB.Pool.Exec(ctx, `UPDATE connection SET config = $2 WHERE id = $1 AND config::text = $3`,
				c.id, repaired, c.config)
			if err != nil {
				return err
			}
			if tag.RowsAffected() == 0 {
				fmt.Fprintf(out, "%s changed while it was being fixed; skipped, run again\n", c.id)
				skipped++
				continue
			}
		}
		line := fmt.Sprintf("%s (%s)", c.id, c.username)
		if reconnect {
			line += ", needs lctl reconnect " + c.id
			reconnects = append(reconnects, c.id)
		}
		fmt.Fprintln(out, line)
		for _, k := range keys {
			if k == "Pass" {
				fmt.Fprintf(out, "  %s: repaired (not shown)\n", k)
				continue
			}
			fmt.Fprintf(out, "  %s: %s\n", k, strings.Join(changed[k], ", "))
		}
		fixed++
	}

	if !*fix {
		fmt.Fprintf(out, "would fix %d connection(s)\n", fixed)
		if fixed > 0 {
			fmt.Fprintln(out, "run again with --fix to save them")
		}
		return nil
	}
	if fixed > 0 {
		if _, err := env.DB.Pool.Exec(ctx, `NOTIFY highlights`); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "fixed %d connection(s)\n", fixed)
	// liercd runs these with the mangled values, and the logger and the
	// stream save its channels and nick back into the row
	if len(reconnects) > 0 {
		fmt.Fprintf(out, "reconnect them now, before liercd's live state is saved over the repair:\n  for id in %s; do lctl reconnect $id; done\n", strings.Join(reconnects, " "))
	}
	if skipped > 0 {
		return fmt.Errorf("%d connection(s) changed while being fixed; run again", skipped)
	}
	return nil
}

// repairValue repairs every string in v, calling fixed with each repaired
// string.
func repairValue(v any, fixed func(string)) any {
	switch x := v.(type) {
	case string:
		if s, ok := undoDoubleUTF8(x); ok {
			fixed(s)
			return s
		}
	case []any:
		for i := range x {
			x[i] = repairValue(x[i], fixed)
		}
	case map[string]any:
		for k := range x {
			x[k] = repairValue(x[k], fixed)
		}
	}
	return v
}

// undoDoubleUTF8 reads s as UTF-8 bytes that were each taken as a Latin-1
// character, and returns the text they encode. ok is false for a string
// that isn't that: one that is ASCII, has a character past U+00FF (so it
// was stored right), or whose bytes aren't valid UTF-8.
func undoDoubleUTF8(s string) (string, bool) {
	b := make([]byte, 0, len(s))
	high := false
	for _, r := range s {
		if r > 0xFF {
			return "", false
		}
		if r >= 0x80 {
			high = true
		}
		b = append(b, byte(r))
	}
	if !high || !utf8.Valid(b) {
		return "", false
	}
	return string(b), true
}

// encodeConfig writes a config back as JSON, leaving <, > and & as they are.
func encodeConfig(obj map[string]any) (string, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
