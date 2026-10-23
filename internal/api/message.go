package api

import (
	"fmt"
	"strings"
)

// Handlers ported from lib/API/Controller/Message.pm.

func messageHighlight(a *App, r *Request) (Response, error) {
	limit, err := limitParam(r, 50, 150)
	if err != nil {
		return nil, err
	}
	args := []any{r.Session.User()}
	sql := `
		SELECT l.id, l.message, l.connection, l.self, l.highlight
		FROM log AS l
		WHERE l."user"=$1
		  AND l.highlight=True`
	if ev, ok := r.Params["event"]; ok {
		args = append(args, ev)
		sql += fmt.Sprintf(` AND l.id < $%d`, len(args))
	}
	args = append(args, limit)
	sql += fmt.Sprintf(` ORDER BY l.id DESC LIMIT $%d`, len(args))
	rows, err := a.DB.Pool.Query(r.Context(), sql, args...)
	if err != nil {
		return nil, err
	}
	data, err := scanLog(rows, true)
	if err != nil {
		return nil, err
	}
	return JSON(data), nil
}

func messageMissed(a *App, r *Request) (Response, error) {
	user := r.Session.User()
	r.Param("") // load r.params
	where := []string{"FALSE"}
	var args []any
	var connections []any
	for key := range r.params {
		val, _ := r.Param(key)
		connection, channel, _ := strings.Cut(key, "-")
		n := len(args)
		where = append(where, fmt.Sprintf("(log.connection=$%d AND log.channel=$%d AND log.id > $%d)", n+1, n+2, n+3))
		args = append(args, connection, channel, val)
		connections = append(connections, connection)
	}
	if len(connections) == 0 {
		return JSON(map[string]any{}), nil
	}

	ph := make([]string, len(connections))
	for i := range connections {
		ph[i] = fmt.Sprintf("$%d", i+2)
	}
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT id
		FROM connection
		WHERE "user" <> $1
		  AND id IN (`+strings.Join(ph, ", ")+`)`, append([]any{user}, connections...)...)
	if err != nil {
		return nil, err
	}
	var bad []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		bad = append(bad, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(bad) > 0 {
		return nil, fmt.Errorf("Invalid connection (%s)", strings.Join(bad, ", "))
	}

	rows, err = a.DB.Pool.Query(r.Context(), `
		SELECT channel, connection,
		  SUM( CASE WHEN command = 'PRIVMSG' THEN 1 ELSE 0 END ) AS messages,
		  SUM( CASE WHEN command <> 'PRIVMSG' THEN 1 ELSE 0 END ) AS events,
		  SUM( CASE WHEN highlight THEN 1 ELSE 0 END ) AS highlights
		FROM log
		WHERE
		(
		`+strings.Join(where, " OR ")+`
		)
		GROUP BY channel, connection`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// Perl's += on the sums makes them JSON numbers.
	channels := map[string]map[string]map[string]int64{}
	for rows.Next() {
		var chn, conn string
		var m, e, h *int64
		if err := rows.Scan(&chn, &conn, &m, &e, &h); err != nil {
			return nil, err
		}
		if channels[conn] == nil {
			channels[conn] = map[string]map[string]int64{}
		}
		channels[conn][chn] = map[string]int64{"messages": deref(m), "events": deref(e), "highlights": deref(h)}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(channels), nil
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func messageLog(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT l.id, l.message, l.connection, l.self, l.highlight
		FROM log AS l
		JOIN connection AS c
		  ON c.id=l.connection
		WHERE
		  c."user"=$1
		  AND l.id > $2
		ORDER BY l.id DESC`, r.Session.User(), r.Params["event"])
	if err != nil {
		return nil, err
	}
	data, err := scanLog(rows, true)
	if err != nil {
		return nil, err
	}
	if len(data) > 250 {
		return Error("Too many messages!"), nil
	}
	// Perl unshifts each row, so the list is oldest first.
	for i, j := 0, len(data)-1; i < j; i, j = i+1, j-1 {
		data[i], data[j] = data[j], data[i]
	}
	return JSON(data), nil
}

func messageSeen(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT s.connection, s.channel, s.message_id
		FROM seen AS s
		JOIN connection AS c
		  ON c.id=s.connection
		WHERE s."user"=$1`, r.Session.User())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	type seenRow struct {
		Connection string `json:"connection"`
		Channel    string `json:"channel"`
		MessageID  int32  `json:"message_id"`
	}
	out := []seenRow{}
	for rows.Next() {
		var s seenRow
		if err := rows.Scan(&s.Connection, &s.Channel, &s.MessageID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}
