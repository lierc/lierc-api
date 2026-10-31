// Package stream is the Server-Sent Events server, ported from bin/stream.psgi
// and lib/API/Stream/*.pm.
package stream

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/liercd"
	"github.com/lierc/lierc-api/internal/session"
)

// Server serves /stats and, on every other path, the event stream of the
// user logged in by Perl's "chats" cookie.
type Server struct {
	// PingInterval is the time between pings to every stream (Events.pm start_pings).
	PingInterval time.Duration
	// WriteTimeout is how long one write may block before the stream is closed (Writer.pm wtimeout).
	WriteTimeout time.Duration
	// NSQReconnect is the wait between attempts to reach nsqd, at start and
	// after a lost connection.
	NSQReconnect time.Duration

	cfg config.Config
	db  *db.DB
	lc  *liercd.Client

	mu      sync.Mutex
	streams map[string]map[string]*stream // user -> stream id -> stream
	latest  map[string]int64              // user -> newest message id, while the user has streams
	closed  bool                          // set by CloseStreams

	replayed func() // test hook: runs when a stream's replay goroutine ends
}

func New(cfg config.Config, d *db.DB, lc *liercd.Client) *Server {
	return &Server{
		PingInterval: 5 * time.Second,
		WriteTimeout: 3 * time.Minute,
		NSQReconnect: 5 * time.Second,
		cfg:          cfg,
		db:           d,
		lc:           lc,
		streams:      map[string]map[string]*stream{},
		latest:       map[string]int64{},
	}
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/stats" {
		s.serveStats(w, r)
		return
	}

	ctx := r.Context()
	user := session.User(r, s.cfg.Secret)
	ok := false
	if user != "" {
		var err error
		if ok, err = s.db.UserExists(ctx, user); err != nil {
			log.Printf("stream: lookup user: %v", err)
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	conns, err := s.db.Connections(ctx, user)
	if err != nil {
		log.Printf("stream: connections: %v", err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
	s.serveStream(w, r, user, conns)
}

// Run pings every stream each PingInterval until ctx is done.
func (s *Server) Run(ctx context.Context) {
	t := time.NewTicker(s.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pingAll()
		}
	}
}

func (s *Server) pingAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, user := range s.streams {
		for _, st := range user {
			s.pingLocked(st)
		}
	}
}

// pingLocked queues a ping carrying st's user's newest message id. s.mu must
// be held.
func (s *Server) pingLocked(st *stream) {
	s.offerLocked(st, pingEvent(s.latest[st.user]))
}

// raiseLatestLocked records id as user's newest message id unless a newer one
// is known. s.mu must be held.
func (s *Server) raiseLatestLocked(user string, id int64) {
	if id > s.latest[user] {
		s.latest[user] = id
	}
}

// offerLocked queues b on st, deregistering st at once if that drops it as a
// slow client. s.mu must be held. The stream's handler still does the cleanup.
func (s *Server) offerLocked(st *stream, b []byte) {
	if !st.offer(b) {
		s.deregisterLocked(st)
	}
}

// CloseStreams ends every open stream, and any opened after it as soon as it
// starts. Give it to http.Server.RegisterOnShutdown: streams never end on their
// own, so Shutdown would otherwise hold them open, silent, until its deadline.
// Each stream still runs its close work, and its client reconnects.
func (s *Server) CloseStreams() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	for _, user := range s.streams {
		for _, st := range user {
			st.stop()
		}
	}
}

func (s *Server) register(st *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		st.stop()
	}
	if s.streams[st.user] == nil {
		s.streams[st.user] = map[string]*stream{}
	}
	s.streams[st.user][st.id] = st
}

func (s *Server) deregister(st *stream) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deregisterLocked(st)
}

func (s *Server) deregisterLocked(st *stream) {
	delete(s.streams[st.user], st.id)
	if len(s.streams[st.user]) == 0 {
		delete(s.streams, st.user)
		delete(s.latest, st.user)
	}
}

// Deliver sends one NSQ "logged" message to every stream of the owner of its
// ConnectionId. The body is passed through untouched.
func (s *Server) Deliver(body []byte) {
	var m struct{ ConnectionId, MessageId json.RawMessage }
	if err := json.Unmarshal(body, &m); err != nil {
		log.Printf("stream: bad message: %v", err)
		return
	}
	s.mu.Lock()
	idle := len(s.streams) == 0
	s.mu.Unlock()
	if idle {
		return // nobody to deliver to; skip the owner lookup
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	owner, ok, err := s.db.LookupOwner(ctx, perlString(m.ConnectionId))
	if err != nil {
		log.Printf("stream: lookup owner: %v", err)
		return
	}
	if !ok {
		return
	}

	var id string
	if perlTrue(m.MessageId) {
		id = perlString(m.MessageId)
	}
	ev := sseEvent("irc", body, id)

	s.mu.Lock()
	defer s.mu.Unlock()
	if n, err := strconv.ParseInt(id, 10, 64); err == nil && len(s.streams[owner]) > 0 {
		s.raiseLatestLocked(owner, n)
	}
	for _, st := range s.streams[owner] {
		s.offerLocked(st, ev)
	}
}

// sseEvent is Util::event: the id line only when id is non-empty.
func sseEvent(typ string, data []byte, id string) []byte {
	var b bytes.Buffer
	b.WriteString("event: ")
	b.WriteString(typ)
	b.WriteString("\ndata: ")
	b.Write(data)
	if id != "" {
		b.WriteString("\nid: ")
		b.WriteString(id)
	}
	b.WriteString("\n\n")
	return b.Bytes()
}

// pingEvent carries the time and the user's newest message id, so a client
// can tell it has missed messages.
func pingEvent(lastID int64) []byte {
	b, err := marshal(struct {
		Time   json.Number `json:"time"`
		LastID int64       `json:"last_id"`
	}{json.Number(hiresTime()), lastID})
	if err != nil {
		panic(err) // hiresTime is always a valid number
	}
	return sseEvent("ping", b, "")
}

// streamEvent tells a client its stream's id, which it names in wake pings.
func streamEvent(id string) []byte {
	b, err := marshal(struct {
		ID string `json:"id"`
	}{id})
	if err != nil {
		panic(err)
	}
	return sseEvent("stream", b, "")
}

// hiresTime is Time::HiRes::time as Perl prints it (%.15g).
func hiresTime() string {
	return strconv.FormatFloat(float64(time.Now().UnixMicro())/1e6, 'g', 15, 64)
}

// marshal encodes like JSON::XS encode_json: no HTML escaping, no newline.
func marshal(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// writeJSON is API::Responses json.
func writeJSON(w http.ResponseWriter, code int, v any) {
	b, err := marshal(v)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "application/javascript;charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(code)
	w.Write(b)
}

type errorBody struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

// writeError is API::Responses error.
func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, errorBody{"error", msg})
}

// perlTrue is Perl truthiness of a decoded JSON value.
func perlTrue(v json.RawMessage) bool {
	s := string(bytes.TrimSpace(v))
	switch s {
	case "", "null", "false", `""`, `"0"`:
		return false
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return f != 0
	}
	return true
}

// perlString is how Perl interpolates a decoded JSON scalar.
func perlString(v json.RawMessage) string {
	s := string(bytes.TrimSpace(v))
	switch {
	case s == "" || s == "null":
		return ""
	case s == "true":
		return "1"
	case s == "false":
		return "0"
	case s[0] == '"':
		var str string
		if json.Unmarshal(v, &str) == nil {
			return str
		}
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return strconv.FormatFloat(f, 'g', 15, 64)
	}
	return s
}
