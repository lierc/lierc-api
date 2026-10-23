package api

// Handlers ported from lib/API/Controller/Pref.pm.

const prefUpsertSQL = `
	INSERT INTO pref ("user",name,value) VALUES($1,$2,$3)
	ON CONFLICT ("user", name)
	DO UPDATE SET value=$3 WHERE pref."user"=$1 AND pref.name=$2`

type prefRow struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

func prefUnsubscribe(a *App, r *Request) (Response, error) {
	_, err := a.DB.Pool.Exec(r.Context(), prefUpsertSQL, r.Session.User(), "email", "false")
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}

func prefList(a *App, r *Request) (Response, error) {
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT name, value FROM pref
		WHERE "user"=$1`, r.Session.User())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	// fetchall_arrayref is always truthy, so Pref.pm's not_found is dead code.
	out := []prefRow{}
	for rows.Next() {
		var p prefRow
		if err := rows.Scan(&p.Name, &p.Value); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return JSON(out), nil
}

func prefShow(a *App, r *Request) (Response, error) {
	var p prefRow
	rows, err := a.DB.Pool.Query(r.Context(), `
		SELECT name, value FROM pref
		WHERE "user"=$1 AND name=$2`, r.Session.User(), r.Params["pref"])
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, err
		}
		return NotFound(), nil
	}
	if err := rows.Scan(&p.Name, &p.Value); err != nil {
		return nil, err
	}
	return JSON(p), nil
}

func prefUpsert(a *App, r *Request) (Response, error) {
	// The raw body is the value (Pref.pm upsert).
	_, err := a.DB.Pool.Exec(r.Context(), prefUpsertSQL,
		r.Session.User(), r.Params["pref"], decodeUTF8(string(r.Body)))
	if err != nil {
		return nil, err
	}
	return NoContent(), nil
}
