package stream

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/ids"
)

// bufferSize is how many pings and live events a stream may queue. They never
// wait for room: a stream whose buffer is full is dropped as a slow client.
// Replay events go through their own unbuffered channel, so a long replay
// never takes up this buffer.
const bufferSize = 1024

// closeTimeout bounds the save_channels and last_login work done on close.
const closeTimeout = time.Minute

var padding = ":" + strings.Repeat(" ", 2<<10) + "\n" // Writer.pm send_padding(2 << 10)

// stream is one open event stream (Writer.pm). Only the request goroutine
// writes to the response; everything else hands it events on out (pings and
// live events) or replay. The writer takes from both as they come, so replay
// and live events interleave, as they do in Perl, where live events are
// written on arrival while the replay waits on liercd.
type stream struct {
	id, user, remote string
	agent            *string
	created          int64

	out      chan []byte
	replay   chan []byte
	done     chan struct{}
	stopOnce sync.Once
}

func (st *stream) stop() { st.stopOnce.Do(func() { close(st.done) }) }

// offer queues b without blocking. A full buffer stops the stream and
// reports false; the caller then deregisters it.
func (st *stream) offer(b []byte) bool {
	select {
	case <-st.done:
		return true
	default:
	}
	select {
	case st.out <- b:
		return true
	default:
		log.Printf("stream %s: buffer full, closing", st.id)
		st.stop()
		return false
	}
}

// push hands a replay event to the writer, waiting until it takes it; false
// once the stream has stopped.
func (st *stream) push(b []byte) bool {
	select {
	case <-st.done:
		return false
	default:
	}
	select {
	case st.replay <- b:
		return true
	case <-st.done:
		return false
	}
}

func (s *Server) serveStream(w http.ResponseWriter, r *http.Request, user string, conns []db.Connection) {
	remote, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		remote = r.RemoteAddr
	}
	st := &stream{
		id:      ids.New(),
		user:    user,
		remote:  remote,
		created: time.Now().Unix(),
		out:     make(chan []byte, bufferSize),
		replay:  make(chan []byte),
		done:    make(chan struct{}),
	}
	if v, ok := r.Header["User-Agent"]; ok {
		st.agent = &v[0]
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "text/event-stream;charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Registered before the handshake is written: anything queued meanwhile
	// is written after it, by this goroutine.
	s.register(st)
	ctx, cancel := context.WithCancel(r.Context())
	var replay sync.WaitGroup
	defer func() {
		// Runs once per stream, on whatever ended it (Writer.pm on_close).
		// A slow-client drop has already deregistered it; that is harmless.
		st.stop()
		s.deregister(st)
		cancel()
		replay.Wait()
		bg, done := context.WithTimeout(context.Background(), closeTimeout)
		defer done()
		s.saveChannels(bg, user)
		if err := s.db.SaveLastLogin(bg, user); err != nil {
			log.Printf("stream: save last login: %v", err)
		}
	}()

	// Registered first, so a Deliver meanwhile raises the id too.
	id, err := s.db.LatestMessageID(ctx, user)
	if err != nil {
		log.Printf("stream: latest message id: %v", err)
	}
	s.mu.Lock()
	if len(s.streams[user]) > 0 { // not if the stream already ended
		s.raiseLatestLocked(user, id)
	}
	latest := s.latest[user]
	s.mu.Unlock()
	handshake := append([]byte(padding), streamEvent(st.id)...)
	if !s.write(rc, w, append(handshake, pingEvent(latest)...)) {
		return
	}
	if err := s.db.SaveLastLogin(ctx, user); err != nil {
		log.Printf("stream: save last login: %v", err)
	}
	replay.Add(1)
	go func() {
		defer replay.Done()
		s.replay(ctx, st, conns)
		if s.replayed != nil {
			s.replayed()
		}
	}()

	for {
		select {
		case b := <-st.out:
			if !s.write(rc, w, b) {
				return
			}
		case b := <-st.replay:
			if !s.write(rc, w, b) {
				return
			}
		case <-st.done:
			return
		case <-ctx.Done():
			return
		}
	}
}

// write sends b and flushes it, failing if that takes longer than WriteTimeout.
func (s *Server) write(rc *http.ResponseController, w http.ResponseWriter, b []byte) bool {
	err := rc.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
	if err == nil || errors.Is(err, http.ErrNotSupported) {
		_, err = w.Write(b)
	}
	if err == nil {
		err = rc.Flush()
	}
	if err != nil {
		if isTimeout(err) {
			log.Print("write timeout, closing") // Writer.pm
		}
		return false
	}
	return true
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
