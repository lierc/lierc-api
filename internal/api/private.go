package api

// Handlers ported from lib/API/Controller/Private.pm (which declares
// package API::Controller::Message, a copy-paste quirk).

// nick is `decode utf8 => $nick`: invalid bytes become U+FFFD.
func nick(r *Request) string { return decodeUTF8(r.Params["nick"]) }

// decodeUTF8 is `decode utf8 => $s`: each invalid byte becomes U+FFFD.
func decodeUTF8(s string) string { return string([]rune(s)) }

func privateCreate(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(), `
		INSERT INTO private (connection, nick, time)
		VALUES($1,$2,NOW())
		ON CONFLICT (connection, nick)
		DO UPDATE SET time=NOW()
		WHERE private.connection=$1 AND private.nick=$2`, r.Params["id"], nick(r))
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}

func privateList(a *App, r *Request) (Response, error) {
	// time::text is DBD::Pg's text form of a timestamp column.
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT p.nick, p.connection, p.time::text
		FROM private AS p
		JOIN connection AS c
		  ON c.id=p.connection
		WHERE c.user=$1
		ORDER BY p.time ASC`, r.Session.User())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var n, c, t string
		if err := rows.Scan(&n, &c, &t); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"nick": n, "connection": c, "time": t})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

func privateDelete(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(),
		`DELETE FROM private AS p WHERE p.connection=$1 AND p.nick=$2`, r.Params["id"], nick(r))
	if err != nil {
		return nil, err
	}
	return OK(), nil
}
