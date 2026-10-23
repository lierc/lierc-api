package api

// Handlers ported from lib/API/Controller/Ignore.pm.

// ignoreFrom is `decode utf8 => $req->parameters->{from}`; undef binds NULL.
func ignoreFrom(r *Request) *string {
	v, ok := r.Param("from")
	if !ok {
		return nil
	}
	v = decodeUTF8(v)
	return &v
}

func ignoreDelete(a *App, r *Request) (Response, error) {
	// The :from capture is unused; the query parameter is (Ignore.pm delete).
	_, err := a.DB.Pool.Exec(r.Context(), `
		DELETE FROM ignore
		WHERE connection=$1
		  AND channel=$2
		  AND "from"=$3`, r.Params["id"], channelName(r), ignoreFrom(r))
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}

func ignoreList(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT i.* FROM ignore AS i
		JOIN connection AS c
		  ON c.id=i.connection
		WHERE c."user"=$1`, r.Session.User())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var conn, channel, from string
		if err := rows.Scan(&conn, &channel, &from); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"connection": conn, "channel": channel, "from": from})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

func ignoreCreate(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(), `
		INSERT INTO ignore
		(connection, channel, "from")
		VALUES($1,$2,$3)`, r.Params["id"], channelName(r), ignoreFrom(r))
	if err != nil {
		return nil, err
	}
	return OK(), nil
}
