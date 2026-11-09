package lctl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// streamStats fetches /stats from the stream server's admin listener (API_STATS): stream counts
// per user id, or with user, that user's streams.
func streamStats(ctx context.Context, env *Env, user string, into any) error {
	u := env.StatsURL
	if user != "" {
		u += "?user=" + url.QueryEscape(user)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("stream server said %s: %s", statusLine(res.StatusCode), strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, into)
}

func usernames(ctx context.Context, env *Env, ids []string) (map[string]string, error) {
	rows, err := env.DB.Pool.Query(ctx, `SELECT id, username FROM "user" WHERE id = ANY($1)`, ids)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	names := map[string]string{}
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, rows.Err()
}

func stats(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "stats", "stats\n\nOpen event streams per user, most first: count username (id).")
	if err := Parse(fs, args); err != nil {
		return err
	}
	var counts map[string]int
	if err := streamStats(ctx, env, "", &counts); err != nil {
		return err
	}
	ids := sorted(counts)
	names, err := usernames(ctx, env, ids)
	if err != nil {
		return err
	}
	name := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		return "?"
	}
	sort.SliceStable(ids, func(i, j int) bool {
		if counts[ids[i]] != counts[ids[j]] {
			return counts[ids[i]] > counts[ids[j]]
		}
		return name(ids[i]) < name(ids[j])
	})
	for _, id := range ids {
		fmt.Fprintf(env.Stdout, "%d %s (%s)\n", counts[id], name(id), id)
	}
	return nil
}

const usersUsage = `users [USERNAME...]

Each open event stream for these users (default: every user with one), as
username - [age] "user agent". Usernames match case-insensitively.`

func users(ctx context.Context, env *Env, args []string) error {
	fs := NewFlags(env, "users", usersUsage)
	if err := Parse(fs, args); err != nil {
		return err
	}
	type who struct{ id, name string }
	var list []who
	if fs.NArg() == 0 {
		var counts map[string]int
		if err := streamStats(ctx, env, "", &counts); err != nil {
			return err
		}
		ids := sorted(counts)
		names, err := usernames(ctx, env, ids)
		if err != nil {
			return err
		}
		for _, id := range ids {
			n, ok := names[id]
			if !ok {
				n = id
			}
			list = append(list, who{id, n})
		}
		sort.SliceStable(list, func(i, j int) bool { return list[i].name < list[j].name })
	}
	for _, arg := range fs.Args() {
		var w who
		err := env.DB.Pool.QueryRow(ctx, `SELECT id, username FROM "user" WHERE lower(username) = lower($1)`, arg).Scan(&w.id, &w.name)
		if isNoRows(err) {
			return fmt.Errorf("unknown user %s", arg)
		}
		if err != nil {
			return err
		}
		list = append(list, w)
	}

	for _, w := range list {
		var streams []struct {
			Agent   *string `json:"agent"`
			Created int64   `json:"created"`
		}
		if err := streamStats(ctx, env, w.id, &streams); err != nil {
			return err
		}
		for _, s := range streams {
			age := env.now().Sub(time.Unix(s.Created, 0))
			when := fmt.Sprintf("%d minutes", int(age.Minutes()))
			if age >= time.Hour {
				when = fmt.Sprintf("%d hours", int(age.Hours()))
			}
			agent := "-"
			if s.Agent != nil {
				agent = *s.Agent
			}
			fmt.Fprintf(env.Stdout, "%s - [%s] %q\n", w.name, when, agent)
		}
	}
	return nil
}
