package stream

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/lierc/lierc-api/internal/respond"

	"github.com/lierc/lierc-api/internal/db"
)

// connEvent is a connection event's data. Name and Nick are left out where
// the event doesn't carry them.
type connEvent struct {
	Id   string
	Name string `json:",omitempty"`
	Nick string `json:",omitempty"`
}

// sendToUser offers ev to every stream user has open.
func (s *Server) sendToUser(user string, ev []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, st := range s.streams[user] {
		s.offerLocked(st, ev)
	}
}

func (s *Server) hasStreams(user string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.streams[user]) > 0
}

func connEventBytes(typ string, c connEvent) []byte {
	b, err := respond.Marshal(c)
	if err != nil {
		panic(err) // plain strings
	}
	return sseEvent(typ, b, "")
}

// ConnectionChanged sends a connection change to the owner's streams:
// create and update with the row's name (and nick), delete with just the
// id, since the row is already gone. The API and the admin API call it
// for their changes.
func (s *Server) ConnectionChanged(ctx context.Context, c db.ConnectionChange) {
	if !s.hasStreams(c.User) {
		return // nobody to tell; skip the lookup
	}
	switch c.Action {
	case "delete":
		s.sendToUser(c.User, connEventBytes("connection:delete", connEvent{Id: c.Connection}))
	case "create", "update":
		ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		owner, cfg, ok, err := s.db.LookupConnection(ctx, c.Connection)
		if err != nil {
			log.Printf("stream: look up %s: %v", c.Connection, err)
			return
		}
		if !ok {
			return // deleted since; its delete follows
		}
		var conf struct{ Alias, Host, Nick string }
		json.Unmarshal(cfg, &conf) // a field of another type is left empty
		ev := connEvent{Id: c.Connection, Name: conf.Alias}
		if ev.Name == "" {
			ev.Name = conf.Host
		}
		typ := "connection:update"
		if c.Action == "create" {
			typ, ev.Nick = "connection:create", conf.Nick
		}
		s.sendToUser(owner, connEventBytes(typ, ev))
	default:
		log.Printf("stream: unknown connection change %+v", c)
	}
}

// statusChanged sends liercd's status message (topic status) to the
// owner's streams as connection:connect or connection:disconnect.
func (s *Server) statusChanged(body []byte) {
	var m struct {
		Id        string
		Connected bool
	}
	if err := json.Unmarshal(body, &m); err != nil || m.Id == "" {
		log.Printf("stream: bad status message: %q", body)
		return
	}
	s.mu.Lock()
	idle := len(s.streams) == 0
	s.mu.Unlock()
	if idle {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, ok, err := s.db.LookupOwner(ctx, m.Id)
	if err != nil {
		log.Printf("stream: lookup owner: %v", err)
		return
	}
	if !ok {
		return
	}
	typ := "connection:disconnect"
	if m.Connected {
		typ = "connection:connect"
	}
	s.sendToUser(owner, connEventBytes(typ, connEvent{Id: m.Id}))
}
