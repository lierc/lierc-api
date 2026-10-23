package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/lierc/lierc-api/internal/ids"
)

// Handlers ported from lib/API/Controller/Connection.pm.

func connectionList(a *App, r *Request) (Response, error) {
	conns, err := a.DB.Connections(r.Context(), r.Session.User())
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(conns))
	for i, c := range conns {
		out[i] = map[string]any{"Id": c.ID, "Config": c.Config}
	}
	return JSON(out), nil
}

func connectionShow(a *App, r *Request) (Response, error) {
	id := r.Params["id"]
	data, ok, err := a.DB.LookupConfig(r.Context(), id)
	if err != nil {
		return nil, err
	}
	if !ok {
		// decode_json undef dies in Connection.pm show.
		return nil, errors.New("malformed JSON string, neither tag, array, object, number, string or atom, at character offset 0 (before \"(end of string)\")")
	}
	var config json.RawMessage
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}
	return JSON(map[string]any{"Id": id, "Config": config}), nil
}

// liercdError turns a non-200 liercd response into the error Perl dies with
// ($res->decoded_content).
func liercdError(body []byte) error { return errors.New(string(body)) }

func connectionCreate(a *App, r *Request) (Response, error) {
	var params map[string]any
	if err := json.Unmarshal(r.Body, &params); err != nil {
		return nil, err
	}
	for _, f := range []string{"Host", "Port", "Nick"} {
		v := params[f]
		// defined && =~ /\S/ ; numbers, booleans and references are never blank.
		if s, isStr := v.(string); v == nil || isStr && !hasNonSpace(s) {
			return nil, fmt.Errorf("%s is required", f)
		}
	}

	id := r.Params["id"]
	if id == "" {
		id = ids.New()
	}
	res, err := a.Liercd.Do(r.Context(), http.MethodPost, id+"/create", r.Body)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, liercdError(res.Body)
	}
	if err := a.DB.SaveConnection(r.Context(), id, r.Session.User(), r.Body); err != nil {
		return nil, err
	}
	if _, err := a.DB.Pool.Exec(r.Context(), "NOTIFY highlights"); err != nil {
		return nil, err
	}
	return JSON(map[string]any{"success": "ok", "id": id}), nil
}

func connectionDelete(a *App, r *Request) (Response, error) {
	id := r.Params["id"]
	res, err := a.Liercd.Do(r.Context(), http.MethodPost, id+"/destroy", nil)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, liercdError(res.Body)
	}
	if err := a.DB.DeleteConnection(r.Context(), id); err != nil {
		return nil, err
	}
	return OK(), nil
}

func connectionEdit(a *App, r *Request) (Response, error) {
	// Results are ignored, as in Connection.pm edit ($app->run swallows dies).
	runIgnoring(connectionDelete, a, r)
	runIgnoring(connectionCreate, a, r)
	return OK(), nil
}

func runIgnoring(h Handler, a *App, r *Request) {
	defer func() { recover() }()
	h(a, r)
}

func connectionSend(a *App, r *Request) (Response, error) {
	id := r.Params["id"]
	token := r.Header.Get("lierc-token")
	user := r.Session.User()

	ok, err := a.DB.CheckToken(r.Context(), user, token)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, errors.New("Invalid token")
	}
	res, err := a.Liercd.Do(r.Context(), http.MethodPost, id+"/raw", r.Body)
	if err != nil {
		return nil, err
	}
	if res.Status != http.StatusOK {
		return nil, liercdError(res.Body)
	}
	next, err := a.DB.GetToken(r.Context(), user)
	if err != nil {
		return nil, err
	}
	return OK("token", next), nil
}
