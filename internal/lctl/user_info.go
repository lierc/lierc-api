package lctl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/lierc/lierc-api/internal/admin"
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
		u, found, err := findUserDetail(ctx, env, who)
		if err != nil {
			return err
		}
		if !found {
			fmt.Fprintf(out, "%s: no such user\n", who)
			missing++
			continue
		}

		password := "none"
		switch u.Password {
		case "bcrypt":
			password = "bcrypt"
		case "legacy":
			password = "legacy (not yet moved to bcrypt)"
		}
		fmt.Fprintf(out, "%s (%s)\n", u.Username, u.ID)
		fmt.Fprintf(out, "  email:       %s (%s)\n", u.Email, verifiedState(u.Verified))
		fmt.Fprintf(out, "  last login:  %s\n", when(env.now(), u.LastLogin, "never"))
		fmt.Fprintf(out, "  registered:  %s\n", when(env.now(), u.Registered, "unknown"))
		fmt.Fprintf(out, "  password:    %s\n", password)

		fmt.Fprintf(out, "  connections: %d\n", len(u.ConnectionList))
		for _, c := range u.ConnectionList {
			fmt.Fprintf(out, "    %s  %s  %s\n", c.ID, describeConfig(c.Config), liercdState(ctx, env, c.ID))
			fmt.Fprintf(out, "      liercd: %s\n", c.Instance)
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d user(s) not found", missing)
	}
	return nil
}

// findUserDetail is the user named who (id, username or email), with their
// connections; found is false if there is none.
func findUserDetail(ctx context.Context, env *Env, who string) (u admin.UserDetail, found bool, err error) {
	id, found, err := lookupUserID(ctx, env, who)
	if err != nil || !found {
		return u, false, err
	}
	u, err = env.Admin.User(ctx, id)
	if errors.Is(err, ErrNotFound) {
		return u, false, nil
	}
	return u, err == nil, err
}

// lookupUserID is the id of the user named who (id, username or email,
// case-insensitively); an id match wins over a username or email match.
func lookupUserID(ctx context.Context, env *Env, who string) (string, bool, error) {
	list, err := env.Admin.LookupUsers(ctx, who)
	if err != nil || len(list) == 0 {
		return "", false, err
	}
	for _, u := range list {
		if u.ID == who {
			return u.ID, true, nil
		}
	}
	return list[0].ID, true, nil
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

// when is t as "2006-01-02 15:04 UTC (3 days ago)", or none if it's NULL.
func when(now time.Time, t *time.Time, none string) string {
	if t == nil {
		return none
	}
	return fmt.Sprintf("%s UTC (%s)", t.UTC().Format("2006-01-02 15:04"), ago(now, *t))
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
