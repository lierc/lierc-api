package stream

import (
	"context"
	"encoding/json"
	"log"
	"time"

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
	b, err := marshal(c)
	if err != nil {
		panic(err) // plain strings
	}
	return sseEvent(typ, b, "")
}

// ListenConnections LISTENs on db.ConnectionsChannel, where the API and lctl
// announce created, edited and deleted connections, and sends each to the
// owner's streams, until ctx is done. A lost connection is retried every
// NSQReconnect; notifications sent meanwhile are lost, and the next
// connections snapshot (on a stream's open) repairs the UI.
func (s *Server) ListenConnections(ctx context.Context) {
	for ctx.Err() == nil {
		err := s.listenOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		log.Printf("stream: listening for connection changes: %v (retrying in %s)", err, s.NSQReconnect)
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.NSQReconnect):
		}
	}
}

func (s *Server) listenOnce(ctx context.Context) error {
	pc, err := s.db.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// a LISTENing connection mustn't go back to the pool
	conn := pc.Hijack()
	defer conn.Close(context.Background())
	if _, err := conn.Exec(ctx, "LISTEN "+db.ConnectionsChannel); err != nil {
		return err
	}
	if s.listening != nil {
		s.listening()
	}
	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		s.connectionChanged(ctx, n.Payload)
	}
}

// connectionChanged sends a connections-channel notification to the
// owner's streams: create and update with the row's name (and nick), delete
// with just the id, since the row is already gone.
func (s *Server) connectionChanged(ctx context.Context, payload string) {
	var c db.ConnectionChange
	if err := json.Unmarshal([]byte(payload), &c); err != nil || c.User == "" || c.Connection == "" {
		log.Printf("stream: bad connection change %q", payload)
		return
	}
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
		log.Printf("stream: unknown connection change %q", payload)
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
