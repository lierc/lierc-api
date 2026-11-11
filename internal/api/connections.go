package api

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"github.com/lierc/lierc-api/internal/connconfig"
	"github.com/lierc/lierc-api/internal/connections"
	"github.com/lierc/lierc-api/internal/respond"
)

// checkHost refuses private hosts for new and edited connections; tests
// replace it, since it resolves names.
var checkHost = func(ctx context.Context, host string, port int) error {
	return connconfig.CheckHost(ctx, os.Getenv, connconfig.SystemResolver, host, port)
}

// The connection routes, ported from lib/API/Controller/Connection.pm; the
// work is in internal/connections.
func (a *api) connections(mux *http.ServeMux) {
	mux.Handle("GET /connection", a.users.Require(a.listConnections))
	mux.Handle("POST /connection", a.users.Require(a.createConnection))
	mux.Handle("GET /connection/{id}", a.users.Require(a.owned(a.showConnection)))
	mux.Handle("PUT /connection/{id}", a.users.Require(a.owned(a.editConnection)))
	mux.Handle("PUT /connection/{id}/config", a.users.Require(a.owned(a.saveConnection)))
	mux.Handle("DELETE /connection/{id}", a.users.Require(a.owned(a.deleteConnection)))
	mux.Handle("POST /connection/{id}", a.users.Require(a.owned(a.send)))
}

type connectionJSON struct {
	Id     string
	Config json.RawMessage
}

// failed answers err: a user's problem as Perl's 400, anything else 500.
func failed(w http.ResponseWriter, what string, err error) {
	if msg, ok := connections.IsUserError(err); ok {
		respond.Error(w, http.StatusBadRequest, msg)
		return
	}
	respond.Internal(w, what, err)
}

func ok(w http.ResponseWriter) {
	respond.JSON(w, http.StatusOK, struct {
		Status string `json:"status"`
	}{"ok"})
}

func (a *api) listConnections(w http.ResponseWriter, r *http.Request, user string) {
	conns, err := a.db.Connections(r.Context(), user)
	if err != nil {
		respond.Internal(w, "list connections", err)
		return
	}
	out := []connectionJSON{}
	for _, c := range conns {
		if !json.Valid(c.Config) {
			log.Printf("list connections: %s has a config that isn't JSON", c.ID)
			continue
		}
		out = append(out, connectionJSON{c.ID, c.Config})
	}
	respond.JSON(w, http.StatusOK, out)
}

func (a *api) showConnection(w http.ResponseWriter, r *http.Request, user, id string) {
	raw, _, err := a.db.LookupConfig(r.Context(), id)
	if err != nil {
		respond.Internal(w, "show connection", err)
		return
	}
	if !json.Valid(raw) {
		raw = []byte("null")
	}
	respond.JSON(w, http.StatusOK, connectionJSON{id, raw})
}

func (a *api) createConnection(w http.ResponseWriter, r *http.Request, user string) {
	body, good := readBody(w, r)
	if !good {
		return
	}
	id, err := a.conns.Create(r.Context(), user, []byte(body))
	if err != nil {
		failed(w, "create connection", err)
		return
	}
	respond.JSON(w, http.StatusOK, struct {
		Success string `json:"success"`
		Id      string `json:"id"`
	}{"ok", id})
}

func (a *api) editConnection(w http.ResponseWriter, r *http.Request, user, id string) {
	body, good := readBody(w, r)
	if !good {
		return
	}
	if err := a.conns.Edit(r.Context(), user, id, []byte(body)); err != nil {
		failed(w, "edit connection", err)
		return
	}
	ok(w)
}

func (a *api) saveConnection(w http.ResponseWriter, r *http.Request, user, id string) {
	body, good := readBody(w, r)
	if !good {
		return
	}
	if err := a.conns.Save(r.Context(), user, id, []byte(body)); err != nil {
		failed(w, "save connection", err)
		return
	}
	ok(w)
}

func (a *api) deleteConnection(w http.ResponseWriter, r *http.Request, user, id string) {
	if err := a.conns.Delete(r.Context(), user, id); err != nil {
		failed(w, "delete connection", err)
		return
	}
	ok(w)
}

// send passes the body, an IRC line, to the connection in liercd
// (Connection.pm send). The bytes go as they came, as in Perl.
func (a *api) send(w http.ResponseWriter, r *http.Request, user, id string) {
	line, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		respond.Error(w, http.StatusBadRequest, "request body is too large")
		return
	}
	res, err := a.conns.Liercd.Do(r.Context(), http.MethodPost, id+"/raw", line)
	if err != nil {
		respond.Error(w, http.StatusBadRequest, err.Error())
		return
	}
	if res.Status != http.StatusOK {
		failed(w, "send", &connections.UserError{Msg: string(trimNL(res.Body))})
		return
	}
	ok(w)
}

func trimNL(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == '\r') {
		b = b[:len(b)-1]
	}
	return b
}
