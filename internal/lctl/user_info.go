package lctl

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/connconfig"
)

const userInfoUsage = `user-info USER...

Shows each user (by id, username or email; names and emails match
case-insensitively): email and whether it's verified, last login, how the
password is stored, and their connections with whether liercd is running
them and which liercd instance (main or next) each is on. Passwords are never shown.`

func userInfo(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "user-info", userInfoUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return &UsageError{Msg: "give a user id, username or email"}
	}
	out := env.Stdout
	missing := 0
	for i, who := range fs.Args() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		var id, username, email string
		var verified *bool // NULL: grandfathered
		var lastLogin time.Time
		var legacy, hash *string
		err := env.DB.Pool.QueryRow(ctx, `SELECT id, username, email, verified, last_login, password, password_hash
		  FROM "user" WHERE id = $1 OR lower(username) = lower($1) OR lower(email) = lower($1)
		  ORDER BY id = $1 DESC LIMIT 1`, who).
			Scan(&id, &username, &email, &verified, &lastLogin, &legacy, &hash)
		if isNoRows(err) {
			fmt.Fprintf(out, "%s: no such user\n", who)
			missing++
			continue
		}
		if err != nil {
			return err
		}

		state := verifiedState(verified)
		password := "none"
		switch {
		case hash != nil && *hash != "":
			password = "bcrypt"
		case legacy != nil && *legacy != "":
			password = "legacy (not yet moved to bcrypt)"
		}
		fmt.Fprintf(out, "%s (%s)\n", username, id)
		fmt.Fprintf(out, "  email:       %s (%s)\n", email, state)
		fmt.Fprintf(out, "  last login:  %s UTC (%s)\n", lastLogin.UTC().Format("2006-01-02 15:04"), ago(env.now(), lastLogin))
		fmt.Fprintf(out, "  password:    %s\n", password)

		conns, err := env.DB.Connections(ctx, id)
		if err != nil {
			return err
		}
		fmt.Fprintf(out, "  connections: %d\n", len(conns))
		for _, c := range conns {
			fmt.Fprintf(out, "    %s  %s  %s\n", c.ID, describeConfig(c.Config), liercdState(ctx, env, c.ID))
			inst, err := instanceName(ctx, env, c.ID)
			if err != nil {
				return err
			}
			fmt.Fprintf(out, "      liercd: %s\n", inst)
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d user(s) not found", missing)
	}
	return nil
}

// describeConfig summarises a saved config without its passwords.
func describeConfig(raw []byte) string {
	v, _ := connconfig.Decode(raw)
	c, ok := v.(map[string]any)
	if !ok {
		return "config isn't a JSON object"
	}
	text := func(k string) string {
		s, _ := connconfig.Display(c[k])
		return s
	}
	parts := []string{hostPort(string(raw))}
	if n := text("Nick"); n != "" {
		parts = append(parts, "nick "+n)
	}
	var flags []string
	for _, k := range []string{"Ssl", "SASL"} {
		if b, _ := c[k].(bool); b {
			flags = append(flags, strings.ToLower(k))
		}
	}
	if len(flags) > 0 {
		parts = append(parts, strings.Join(flags, " "))
	}
	if a := text("Alias"); a != "" {
		parts = append(parts, "alias "+a)
	}
	channels, _ := c["Channels"].([]any)
	parts = append(parts, fmt.Sprintf("%d channels", len(channels)))
	if text("Pass") != "" {
		parts = append(parts, "server password set")
	}
	return strings.Join(parts, "  ")
}

// liercdState is whether liercd is running a connection and connected.
func liercdState(ctx context.Context, env *Env, id string) string {
	res, err := env.Liercd.Do(ctx, http.MethodGet, id+"/status", nil)
	if err != nil {
		return "liercd unreachable"
	}
	if res.Status == http.StatusNotFound {
		return "not running"
	}
	if res.Status != http.StatusOK {
		return "liercd said " + statusLine(res.Status)
	}
	var s struct {
		Nick   string
		Status struct{ Connected bool }
	}
	if json.Unmarshal(res.Body, &s) != nil {
		return "running"
	}
	if s.Status.Connected {
		return "connected as " + s.Nick
	}
	return "running, not connected"
}

// ago is how long before now t was, roughly.
func ago(now, t time.Time) string {
	d := now.Sub(t)
	switch {
	case d < 0:
		return "in the future"
	case d < time.Hour:
		return fmt.Sprintf("%d minutes ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours ago", int(d.Hours()))
	}
	return fmt.Sprintf("%d days ago", int(d.Hours()/24))
}

// verifiedState describes a user's verified column. NULL is an account from
// before signups had to verify their email, which is let in like a verified
// one.
func verifiedState(v *bool) string {
	switch {
	case v == nil:
		return "grandfathered"
	case *v:
		return "verified"
	default:
		return "unverified"
	}
}
