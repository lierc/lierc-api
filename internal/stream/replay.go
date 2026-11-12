package stream

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/lierc/lierc-api/internal/respond"

	"github.com/lierc/lierc-api/internal/db"
)

// The numerics the replay sends, named as irc-go names them.
const (
	rplWelcome       = "001"
	rplISupport      = "005"
	rplChannelModeIs = "324"
	rplTopic         = "332"
	rplTopicTime     = "333"
	rplNamReply      = "353"
	rplEndOfNames    = "366"
)

// status is the part of liercd's GET /:id/status the stream reads, with
// liercd's types (lierc's IRCClientData). A status with any other type
// doesn't decode, which fails the replay like any unreadable status. Go
// sends null for an empty list, topic or status; each reads as absent.
type status struct {
	Id     string
	Nick   string
	Config *struct{ Alias, Host string }
	Status *struct{ Connected bool }

	Registered bool
	Isupport   []string
	CapsAcked  []string
	Channels   []channel
}

type channel struct {
	Name, Mode string
	Topic      *struct {
		Topic, User string
		Time        int64
	}
	Nicks *[]string // nil when missing or null
}

const liercdPrefix = "liercd"

type ircPrefix struct{ Name string }

type ircMessage struct {
	Command string
	Prefix  ircPrefix
	Params  []any // strings, and a topic's time as a number
	Time    json.Number
}

type ircPayload struct {
	ConnectionId string
	Message      ircMessage
}

// ircEvent is Util::irc_event. Time is float seconds, as Time::HiRes::time.
func ircEvent(id, prefix, command string, params ...any) []byte {
	if params == nil {
		params = []any{}
	}
	b, err := respond.Marshal(ircPayload{id, ircMessage{command, ircPrefix{prefix}, params, json.Number(hiresTime())}})
	if err != nil {
		panic(err) // strings and ints always marshal
	}
	return sseEvent("irc", b, "")
}

// replay pushes the fake events that bring a new stream up to date
// (Events.pm push_fake_events). If any connection's status cannot be found or
// recreated, nothing is sent: Perl's callback dies before replaying (R4).
func (s *Server) replay(ctx context.Context, st *stream, conns []db.Connection) {
	statuses := make([]*status, len(conns))
	errs := make([]error, len(conns))
	var wg sync.WaitGroup
	for i, c := range conns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, err := s.lc.FindOrRecreate(ctx, s.db, c.ID)
			if err == nil {
				statuses[i] = &status{}
				err = json.Unmarshal(body, statuses[i])
			}
			errs[i] = err
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("stream %s: replay of %s: %v", st.id, conns[i].ID, err)
			}
			return
		}
	}

	var registered []*status
	for _, stat := range statuses {
		if stat.Registered {
			registered = append(registered, stat)
		}
	}
	var evs [][]byte
	emit := func(id, prefix, command string, params ...any) {
		evs = append(evs, ircEvent(id, prefix, command, params...))
	}

	// The connections snapshot, first: every connection the user has, so
	// the UI adds missing ones and drops ones deleted while it was away
	// (this replaces Perl's push_connect CREATE and CONNECT).
	evs = append(evs, snapshotEvent(statuses))
	// push_welcome. A missing Isupport would die in Perl; it is treated as empty.
	for _, stat := range registered {
		emit(stat.Id, liercdPrefix, rplWelcome, stat.Nick, "Welcome to the Internet Relay Network "+stat.Nick)
		if len(stat.Isupport) > 0 {
			emit(stat.Id, liercdPrefix, rplISupport, stat.Nick, strings.Join(stat.Isupport, " "))
		}
		if len(stat.CapsAcked) > 0 {
			emit(stat.Id, liercdPrefix, "CAP", "*", "ACK", strings.Join(stat.CapsAcked, " "))
		}
	}
	// push_joins
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			emit(stat.Id, stat.Nick, "JOIN", ch.Name)
		}
	}
	// push_topics
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if ch.Topic != nil && ch.Topic.Topic != "" {
				emit(stat.Id, liercdPrefix, rplTopic, stat.Nick, ch.Name, ch.Topic.Topic)
				emit(stat.Id, liercdPrefix, rplTopicTime, stat.Nick, ch.Name, ch.Topic.User, ch.Topic.Time)
			}
		}
	}
	// push_nicks: an empty Nicks array still gets its 366, as in Perl.
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if ch.Nicks == nil {
				continue
			}
			nicks := *ch.Nicks
			for len(nicks) > 0 {
				n := min(50, len(nicks))
				emit(stat.Id, liercdPrefix, rplNamReply, stat.Nick, "=", ch.Name, strings.Join(nicks[:n], " "))
				nicks = nicks[n:]
			}
			emit(stat.Id, liercdPrefix, rplEndOfNames, stat.Nick, ch.Name, "End of /NAMES list.")
		}
	}
	// push_modes
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if ch.Mode != "" {
				emit(stat.Id, liercdPrefix, rplChannelModeIs, ch.Name, ch.Mode)
			}
		}
	}

	for _, ev := range evs {
		if !st.push(ev) {
			return
		}
	}
}

// saveChannels stores each connection's current channel list from liercd in
// its config (Events.pm save_channels). Failures skip that connection.
func (s *Server) saveChannels(ctx context.Context, user string) {
	conns, err := s.db.Connections(ctx, user)
	if err != nil {
		log.Printf("stream: save channels: %v", err)
		return
	}
	for _, c := range conns {
		// Perl ignores the status code; a non-JSON body fails to decode.
		res, err := s.lc.Do(ctx, http.MethodGet, c.ID+"/status", nil)
		if err != nil {
			continue
		}
		var stat struct {
			Id       string
			Channels []struct{ Name string }
		}
		if json.Unmarshal(res.Body, &stat) != nil || len(stat.Channels) == 0 {
			continue
		}
		var cfg map[string]json.RawMessage
		if json.Unmarshal(c.Config, &cfg) != nil {
			continue
		}
		if cfg == nil {
			cfg = map[string]json.RawMessage{}
		}
		names := make([]string, len(stat.Channels))
		for i, ch := range stat.Channels {
			names[i] = ch.Name
		}
		if cfg["Channels"], err = respond.Marshal(names); err != nil {
			continue
		}
		b, err := respond.Marshal(cfg)
		if err != nil {
			continue
		}
		// Perl updates the row named by the status Id, not the connection id.
		if err := s.db.UpdateConfig(ctx, stat.Id, b); err != nil {
			log.Printf("stream: save channels for %s: %v", c.ID, err)
		}
	}
}

type snapshotEntry struct {
	Id, Name, Nick string
	Connected      bool
}

// snapshotEvent is the replay's connections event: each connection's id,
// network name (its alias, else its host), current nick and whether it's
// connected, as liercd's status says.
func snapshotEvent(statuses []*status) []byte {
	list := make([]snapshotEntry, 0, len(statuses))
	for _, stat := range statuses {
		var name string
		if stat.Config != nil {
			name = stat.Config.Alias
			if name == "" {
				name = stat.Config.Host
			}
		}
		list = append(list, snapshotEntry{stat.Id, name, stat.Nick, stat.Status != nil && stat.Status.Connected})
	}
	b, err := respond.Marshal(list)
	if err != nil {
		panic(err) // strings and bools always marshal
	}
	return sseEvent("connections", b, "")
}
