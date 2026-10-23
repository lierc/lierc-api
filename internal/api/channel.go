package api

import (
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Handlers ported from lib/API/Controller/Channel.pm.

// logEntry is one row of a log listing. Self is only set by the handlers that
// select l.self (Message.pm).
type logEntry struct {
	MessageID    int64           `json:"MessageId"`
	Message      json.RawMessage `json:"Message"`
	ConnectionID string          `json:"ConnectionId"`
	Self         *bool           `json:"Self,omitempty"`
	Highlight    bool            `json:"Highlight"`
}

// channelName is `lc decode utf8 => $req->captures->{channel}`.
func channelName(r *Request) string { return strings.ToLower(decodeUTF8(r.Params["channel"])) }

// limitParam is `min($req->parameters->{limit} || def, max)`.
func limitParam(r *Request, def, max int) (int, error) {
	v, _ := r.Param("limit")
	if v == "" || v == "0" { // Perl falsiness
		return def, nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil {
		// Perl would pass the string on to LIMIT; Postgres rejects it.
		return 0, fmt.Errorf("invalid input syntax for type bigint: %q", v)
	}
	if n > float64(max) {
		return max, nil
	}
	return int(n), nil
}

// scanLog reads rows of (id, message, connection, highlight) and, with
// withSelf, (id, message, connection, self, highlight).
func scanLog(rows pgx.Rows, withSelf bool) ([]logEntry, error) {
	defer rows.Close()
	data := []logEntry{}
	for rows.Next() {
		var e logEntry
		var id int32
		var self bool
		var err error
		if withSelf {
			err = rows.Scan(&id, &e.Message, &e.ConnectionID, &self, &e.Highlight)
			e.Self = &self
		} else {
			err = rows.Scan(&id, &e.Message, &e.ConnectionID, &e.Highlight)
		}
		if err != nil {
			return nil, err
		}
		e.MessageID = int64(id)
		data = append(data, e)
	}
	return data, rows.Err()
}

func channelList(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT channel
		FROM log
		WHERE connection=$1
		GROUP BY channel
		ORDER BY MAX(id) DESC`, r.Params["id"])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

func channelDate(a *App, r *Request) (Response, error) {
	chan_ := channelName(r)
	args := []any{r.Params["id"], chan_, r.Params["from"], r.Params["to"]}
	sql := `
		SELECT id,
		  message, connection, highlight
		FROM log
		WHERE connection=$1
		  AND channel=$2
		  AND time >= date($3)
		  AND time < (date($4) + '1 day'::interval)`
	text, hasText := r.Param("text")
	sender, hasSender := r.Param("sender")
	if hasText {
		sql += ` AND to_tsvector('english', message->'Params'->1) @@ to_tsquery($5)`
		args = append(args, decodeUTF8(text))
	}
	if hasSender {
		args = append(args, decodeUTF8(sender))
		sql += fmt.Sprintf(` AND message->'Prefix'->>'Name'=$%d`, len(args))
	}
	sql += ` ORDER BY id ASC`

	return Stream(func(w io.Writer) bool {
		// The headers are already sent when Perl's streaming sub runs, so a
		// query failure just ends the stream (Channel.pm date).
		rows, err := a.DB.Pool.Query(r.Context(), sql, args...)
		if err != nil {
			return true
		}
		defer rows.Close()
		for rows.Next() {
			var e logEntry
			var id int32
			if err := rows.Scan(&id, &e.Message, &e.ConnectionID, &e.Highlight); err != nil {
				return true
			}
			e.MessageID = int64(id)
			b := JSON(e).(jsonResponse).body
			if _, err := fmt.Fprintf(w, "event: log\ndata: %s\n\n", b); err != nil {
				return false
			}
		}
		return true
	}), nil
}

func channelLast(a *App, r *Request) (Response, error) {
	limit, err := limitParam(r, 5, 20)
	if err != nil {
		return nil, err
	}
	var query *string
	if q, ok := r.Param("query"); ok {
		q = decodeUTF8(q)
		query = &q
	}
	// "\x02" is a literal control character in Perl's qq string.
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT id,
		  jsonb_set(
		    message, '{Params,1}', ts_headline(
		      'english', message->'Params'->1, to_tsquery($3),
		      'StartSel = `+"\x02"+`, StopSel = `+"\x02"+`')),
		  connection, highlight
		FROM log
		WHERE channel=$1
		  AND connection=$2
		  AND command='PRIVMSG'
		  AND time > (NOW() - '1 week'::interval)
		  AND to_tsvector(message->'Params'->>1) @@ to_tsquery($3)
		ORDER BY id DESC LIMIT $4`, channelName(r), r.Params["id"], query, limit)
	if err != nil {
		return nil, err
	}
	data, err := scanLog(rows, false)
	if err != nil {
		return nil, err
	}
	return JSON(data), nil
}

func channelLogs(a *App, r *Request) (Response, error) {
	limit, err := limitParam(r, 50, 150)
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT id, message, connection, highlight FROM log
		  WHERE channel=$1 AND connection=$2
		  ORDER BY id DESC LIMIT $3`, channelName(r), r.Params["id"], limit)
	if err != nil {
		return nil, err
	}
	data, err := scanLog(rows, false)
	if err != nil {
		return nil, err
	}
	return JSON(data), nil
}

func channelLogsID(a *App, r *Request) (Response, error) {
	limit, err := limitParam(r, 50, 150)
	if err != nil {
		return nil, err
	}
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT id, message, connection, highlight FROM log
		  WHERE channel=$1 AND connection=$2 AND id < $3
		  ORDER BY id DESC LIMIT $4`, channelName(r), r.Params["id"], r.Params["event"], limit)
	if err != nil {
		return nil, err
	}
	data, err := scanLog(rows, false)
	if err != nil {
		return nil, err
	}
	return JSON(data), nil
}

func channelSetSeen(a *App, r *Request) (Response, error) {
	// The position is the raw request body ($req->content).
	_, err := a.DB.Pool.Exec(r.Context(), `
		INSERT into seen ("user", connection, channel, message_id)
		VALUES($1,$2,$3,$4)
		ON CONFLICT ("user", connection, channel)
		DO UPDATE SET message_id=$4, updated=NOW()
		WHERE seen.user=$1 AND seen.connection=$2 AND seen.channel=$3`,
		r.Session.User(), r.Params["id"], channelName(r), string(r.Body))
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}
