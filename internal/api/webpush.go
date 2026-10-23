package api

// Handlers ported from lib/API/Controller/WebPush.pm.

func webpushKeys(a *App, r *Request) (Response, error) {
	// An unset VAPID_PUBLIC is undef in Perl, so JSON null.
	var key *string
	if a.Cfg.VapidPublic != "" {
		key = &a.Cfg.VapidPublic
	}
	return JSON(map[string]*string{"vapid_public_key": key}), nil
}

func webpushDelete(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(), `
		DELETE FROM web_push
		WHERE "user"=$1 AND endpoint=$2`, r.Session.User(), r.Params["endpoint"])
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}

func webpushList(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT key, auth, endpoint FROM web_push
		WHERE "user"=$1`, r.Session.User())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]string{}
	for rows.Next() {
		var key, auth, endpoint string
		if err := rows.Scan(&key, &auth, &endpoint); err != nil {
			return nil, err
		}
		out = append(out, map[string]string{"key": key, "auth": auth, "endpoint": endpoint})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

// optParam is a parameter that binds NULL when absent.
func optParam(r *Request, name string) *string {
	v, ok := r.Param(name)
	if !ok {
		return nil
	}
	return &v
}

func webpushUpsert(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(), `
		INSERT INTO web_push (endpoint, auth, key, "user")
		  VALUES ($1,$2,$3,$4)
		ON CONFLICT ("user", endpoint)
		  DO UPDATE SET auth=$2, key=$3
		  WHERE web_push."user"=$4
		    AND web_push.endpoint=$1`,
		optParam(r, "endpoint"), optParam(r, "auth"), optParam(r, "key"), r.Session.User())
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}
