package lctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/lierc/lierc-api/internal/connconfig"
)

const connectionInfoUsage = `connection-info ID...

Shows each connection: its owner, its saved config (passwords are never
shown), whether that config passes the connection rules and address check,
and its live state in liercd: connected or not, current nick, status
message, network, capabilities, how many channels it's in, and any setting
where the running config differs from the saved one.`

// liercdConfig is the config liercd is running a connection with.
type liercdConfig struct {
	Host      string
	Port      int
	Ssl       bool
	Nick      string
	User      string
	Pass      string
	SASL      bool
	Alias     string
	Channels  []string
	Highlight []string
}

// liercdData is liercd's /:id/status (lierc's IRCClientData).
type liercdData struct {
	Nick       string
	Registered bool
	Config     *liercdConfig
	Status     *struct {
		Connected bool
		Message   string
	}
	Isupport  []string
	CapsAcked []string
	// only counted, so their contents (topics, nicks) aren't decoded
	Channels []json.RawMessage
}

func connectionInfo(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "connection-info", connectionInfoUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		return &UsageError{Msg: "give one or more connection ids"}
	}
	out := env.Stdout
	missing := 0
	for i, id := range fs.Args() {
		if i > 0 {
			fmt.Fprintln(out)
		}
		var user, config string
		var username *string
		err := env.DB.Pool.QueryRow(ctx, `SELECT c."user", c.config::text, u.username FROM connection c
		  LEFT JOIN "user" u ON u.id = c."user" WHERE c.id = $1`, id).Scan(&user, &config, &username)
		saved := err == nil
		if err != nil && !isNoRows(err) {
			return err
		}

		live, liveState := liercdStatus(ctx, env, id)
		if !saved && live == nil {
			fmt.Fprintf(out, "%s: no such connection\n", id)
			missing++
			continue
		}

		fmt.Fprintln(out, id)
		var params map[string]any
		if saved {
			name := "no such user"
			if username != nil {
				name = *username
			}
			fmt.Fprintf(out, "  user:        %s (%s)\n", name, user)
			fmt.Fprintln(out, "saved config:")
			v, _ := connconfig.Decode([]byte(config))
			params, _ = v.(map[string]any)
			if params != nil {
				printSavedConfig(out, params)
			}
			valid := "yes"
			if why := connectionProblem(ctx, env, []byte(config), true); why != "" {
				valid = "no: " + why
			}
			fmt.Fprintf(out, "  valid:       %s\n", valid)
		} else {
			fmt.Fprintln(out, "  saved:       no row in the database")
		}

		fmt.Fprintln(out, "liercd:")
		fmt.Fprintf(out, "  state:       %s\n", liveState)
		if live != nil {
			printLive(out, live, params)
		}
	}
	if missing > 0 {
		return fmt.Errorf("%d connection(s) not found", missing)
	}
	return nil
}

// liercdStatus fetches a connection's live state; data is nil when liercd
// isn't running it (or can't be asked), and state says which.
func liercdStatus(ctx context.Context, env *Env, id string) (*liercdData, string) {
	res, err := env.Liercd.Do(ctx, http.MethodGet, id+"/status", nil)
	if err != nil {
		return nil, "liercd unreachable: " + err.Error()
	}
	if res.Status == http.StatusNotFound {
		return nil, "not running"
	}
	if res.Status != http.StatusOK {
		return nil, "liercd said " + statusLine(res.Status)
	}
	var d liercdData
	if err := json.Unmarshal(res.Body, &d); err != nil {
		return nil, "running (unreadable status: " + err.Error() + ")"
	}
	if d.Status != nil && d.Status.Connected {
		return &d, "running, connected"
	}
	return &d, "running, not connected"
}

func savedText(params map[string]any, k string) string {
	switch v := params[k].(type) {
	case bool:
		return strconv.FormatBool(v)
	default:
		s, _ := connconfig.Display(v)
		return s
	}
}

func savedList(params map[string]any, k string) string {
	items, _ := params[k].([]any)
	var out []string
	for _, item := range items {
		if s, ok := connconfig.Display(item); ok {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return "none"
	}
	return strings.Join(out, ", ")
}

func printSavedConfig(out io.Writer, params map[string]any) {
	server := hostPortOf(params)
	if b, _ := params["Ssl"].(bool); b {
		server += " (tls)"
	}
	fmt.Fprintf(out, "  server:      %s\n", server)
	fmt.Fprintf(out, "  nick:        %s\n", savedText(params, "Nick"))
	if u := savedText(params, "User"); u != "" {
		fmt.Fprintf(out, "  username:    %s\n", u)
	} else {
		fmt.Fprintln(out, "  username:    (uses nick)")
	}
	if savedText(params, "Pass") != "" {
		how := "PASS"
		if b, _ := params["SASL"].(bool); b {
			how = "SASL"
		}
		fmt.Fprintf(out, "  password:    set, sent with %s\n", how)
	} else {
		fmt.Fprintln(out, "  password:    none")
	}
	if a := savedText(params, "Alias"); a != "" {
		fmt.Fprintf(out, "  alias:       %s\n", a)
	}
	fmt.Fprintf(out, "  channels:    %s\n", savedList(params, "Channels"))
	fmt.Fprintf(out, "  highlight:   %s\n", savedList(params, "Highlight"))
}

func hostPortOf(params map[string]any) string {
	part := func(k string) string {
		if s, ok := connconfig.Display(params[k]); ok {
			return s
		}
		return "?"
	}
	return part("Host") + ":" + part("Port")
}

func printLive(out io.Writer, d *liercdData, saved map[string]any) {
	if d.Status != nil && d.Status.Message != "" {
		fmt.Fprintf(out, "  status:      %s\n", d.Status.Message)
	}
	if d.Nick != "" {
		fmt.Fprintf(out, "  nick:        %s\n", d.Nick)
	}
	registered := "no"
	if d.Registered {
		registered = "yes"
	}
	fmt.Fprintf(out, "  registered:  %s\n", registered)
	for _, s := range d.Isupport {
		if strings.HasPrefix(s, "NETWORK=") {
			fmt.Fprintf(out, "  network:     %s\n", strings.TrimPrefix(s, "NETWORK="))
		}
	}
	if len(d.CapsAcked) > 0 {
		caps := append([]string(nil), d.CapsAcked...)
		sort.Strings(caps)
		fmt.Fprintf(out, "  caps:        %s\n", strings.Join(caps, ", "))
	}
	if diffs := configDiffs(saved, d.Config); len(diffs) > 0 {
		fmt.Fprintf(out, "  differs:     %s\n", strings.Join(diffs, "; "))
	}
	fmt.Fprintf(out, "  joined:      %d channels\n", len(d.Channels))
}

// configDiffs lists the settings where liercd's running config differs
// from the saved one (channels aren't compared: liercd's change as it joins
// and parts). The password is compared but never shown.
func configDiffs(saved map[string]any, live *liercdConfig) []string {
	if saved == nil || live == nil {
		return nil
	}
	var diffs []string
	cmp := func(k, running string) {
		s := savedText(saved, k)
		if (k == "Ssl" || k == "SASL") && s == "" {
			s = "false"
		}
		if s == running {
			return
		}
		if k == "Pass" {
			diffs = append(diffs, "Pass")
			return
		}
		diffs = append(diffs, fmt.Sprintf("%s (running %s, saved %s)", k, orNone(running), orNone(s)))
	}
	cmp("Host", live.Host)
	cmp("Port", strconv.Itoa(live.Port))
	cmp("Ssl", strconv.FormatBool(live.Ssl))
	cmp("Nick", live.Nick)
	cmp("User", live.User)
	cmp("Pass", live.Pass)
	cmp("SASL", strconv.FormatBool(live.SASL))
	cmp("Alias", live.Alias)
	return diffs
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}
