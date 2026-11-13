package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/lierc/lierc-api/internal/connconfig"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/respond"
)

// Connection is a saved connection. Its config has the server password
// replaced by "<set>".
type Connection struct {
	ID       string          `json:"id"`
	User     string          `json:"user"`
	Username string          `json:"username"` // empty when the owner is gone
	Instance string          `json:"instance"` // "main" or "next"
	Host     string          `json:"host"`
	Port     int             `json:"port"`
	Config   json.RawMessage `json:"config"`
}

// ConnectionDetail is one connection and the rows a delete would remove.
type ConnectionDetail struct {
	Connection
	Counts map[string]int64 `json:"counts"`
}

func connectionOf(r db.ConnectionRow) Connection {
	c := Connection{ID: r.ID, User: r.User, Username: r.Username, Instance: r.Instance, Config: redact(r.Config)}
	if v, _ := connconfig.Decode(r.Config); v != nil {
		if params, ok := v.(map[string]any); ok {
			c.Host, _ = connconfig.Display(params["Host"])
			p, _ := connconfig.Display(params["Port"])
			c.Port, _ = strconv.Atoi(p)
		}
	}
	return c
}

// redact replaces a set top-level "Pass" with "<set>". It fails closed: a
// config that isn't a JSON object (which could hold a password anywhere) or
// can't be re-encoded comes back as null, never as stored.
func redact(raw json.RawMessage) json.RawMessage {
	null := json.RawMessage("null")
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil || m == nil {
		return null
	}
	switch string(m["Pass"]) {
	case "", `""`, "null":
		return raw
	}
	m["Pass"] = json.RawMessage(`"<set>"`)
	out, err := respond.Marshal(m) // no HTML escaping: "<set>" stays readable
	if err != nil {
		return null
	}
	return out
}

func (a *api) connectionRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /admin/connections", a.listConnections)
	mux.HandleFunc("GET /admin/connections/{id}", a.connectionDetail)
	mux.HandleFunc("GET /admin/connections/{id}/config", a.connectionConfig)
}

func (a *api) listConnections(w http.ResponseWriter, r *http.Request) {
	var f db.ConnectionFilter
	var ok bool
	if f.InactiveSince, ok = sinceParam(w, r); !ok {
		return
	}
	switch f.Instance = r.URL.Query().Get("instance"); f.Instance {
	case "", "main", "next":
	default:
		fail(w, http.StatusBadRequest, "instance must be main or next")
		return
	}
	rows, err := a.store.ListConnections(r.Context(), f)
	if err != nil {
		internal(w, "list connections", err)
		return
	}
	out := make([]Connection, 0, len(rows))
	for _, c := range rows {
		out = append(out, connectionOf(c))
	}
	write(w, http.StatusOK, out)
}

func (a *api) connectionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	rows, err := a.store.ListConnections(r.Context(), db.ConnectionFilter{ID: id})
	if err != nil {
		internal(w, "get connection", err)
		return
	}
	if len(rows) == 0 {
		fail(w, http.StatusNotFound, "no such connection")
		return
	}
	counts, err := a.store.ConnectionCounts(r.Context(), id)
	if err != nil {
		internal(w, "count connection rows", err)
		return
	}
	write(w, http.StatusOK, ConnectionDetail{Connection: connectionOf(rows[0]), Counts: counts})
}

// connectionConfig is the one place a config leaves whole, password and all.
func (a *api) connectionConfig(w http.ResponseWriter, r *http.Request) {
	cfg, ok, err := a.store.LookupConfig(r.Context(), r.PathValue("id"))
	if err != nil {
		internal(w, "get config", err)
		return
	}
	if !ok {
		fail(w, http.StatusNotFound, "no such connection")
		return
	}
	write(w, http.StatusOK, json.RawMessage(cfg))
}
