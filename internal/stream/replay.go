package stream

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"sync"

	"github.com/lierc/lierc-api/internal/db"
)

// status is the part of liercd's GET /:id/status the stream reads. Values that
// Perl copies into events stay raw so they pass through with their JSON type.
type status struct {
	Id     json.RawMessage
	Nick   json.RawMessage
	Config struct{ Alias, Host json.RawMessage }
	Status struct{ Connected json.RawMessage }

	Registered json.RawMessage
	Isupport   []json.RawMessage
	CapsAcked  []json.RawMessage
	Channels   []channel
}

type channel struct {
	Name, Mode json.RawMessage
	Topic      struct{ Topic, User, Time json.RawMessage }
	Nicks      *[]json.RawMessage // nil when missing or null
}

var liercdPrefix = jsonString("liercd")

func jsonString(s string) json.RawMessage {
	b, _ := marshal(s)
	return b
}

type ircPrefix struct{ Name json.RawMessage }

type ircMessage struct {
	Command string
	Prefix  ircPrefix
	Params  []json.RawMessage
	Time    json.Number
}

type ircPayload struct {
	ConnectionId json.RawMessage
	Message      ircMessage
}

// ircEvent is Util::irc_event. Time is float seconds, as Time::HiRes::time.
func ircEvent(id, prefix json.RawMessage, command string, params ...json.RawMessage) []byte {
	if params == nil {
		params = []json.RawMessage{}
	}
	b, err := marshal(ircPayload{id, ircMessage{command, ircPrefix{prefix}, params, json.Number(hiresTime())}})
	if err != nil {
		panic(err) // raw values came from json.Unmarshal, so they are valid
	}
	return sseEvent("irc", b, "")
}

func joinStrings(vs []json.RawMessage) string {
	parts := make([]string, len(vs))
	for i, v := range vs {
		parts[i] = perlString(v)
	}
	return strings.Join(parts, " ")
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
		if perlTrue(stat.Registered) {
			registered = append(registered, stat)
		}
	}
	var evs [][]byte
	emit := func(id, prefix json.RawMessage, command string, params ...json.RawMessage) {
		evs = append(evs, ircEvent(id, prefix, command, params...))
	}

	// push_connect
	for _, stat := range statuses {
		alias := stat.Config.Alias
		if !perlTrue(alias) {
			alias = stat.Config.Host
		}
		emit(stat.Id, liercdPrefix, "CREATE", stat.Nick, alias)
		if perlTrue(stat.Status.Connected) {
			emit(stat.Id, liercdPrefix, "CONNECT")
		}
	}
	// push_welcome. A missing Isupport would die in Perl; it is treated as empty.
	for _, stat := range registered {
		welcome := "Welcome to the Internet Relay Network " + perlString(stat.Nick)
		emit(stat.Id, liercdPrefix, "001", stat.Nick, jsonString(welcome))
		if len(stat.Isupport) > 0 {
			emit(stat.Id, liercdPrefix, "005", stat.Nick, jsonString(joinStrings(stat.Isupport)))
		}
		if len(stat.CapsAcked) > 0 {
			emit(stat.Id, liercdPrefix, "CAP", jsonString("*"), jsonString("ACK"), jsonString(joinStrings(stat.CapsAcked)))
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
			if perlTrue(ch.Topic.Topic) {
				emit(stat.Id, liercdPrefix, "332", stat.Nick, ch.Name, ch.Topic.Topic)
				emit(stat.Id, liercdPrefix, "333", stat.Nick, ch.Name, ch.Topic.User, ch.Topic.Time)
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
				emit(stat.Id, liercdPrefix, "353", stat.Nick, jsonString("="), ch.Name, jsonString(joinStrings(nicks[:n])))
				nicks = nicks[n:]
			}
			emit(stat.Id, liercdPrefix, "366", stat.Nick, ch.Name, jsonString("End of /NAMES list."))
		}
	}
	// push_modes
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if perlTrue(ch.Mode) {
				emit(stat.Id, liercdPrefix, "324", ch.Name, ch.Mode)
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
			Id       json.RawMessage
			Channels []struct{ Name json.RawMessage }
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
		names := make([]json.RawMessage, len(stat.Channels))
		for i, ch := range stat.Channels {
			names[i] = ch.Name
		}
		if cfg["Channels"], err = marshal(names); err != nil {
			continue
		}
		b, err := marshal(cfg)
		if err != nil {
			continue
		}
		// Perl updates the row named by the status Id, not the connection id.
		if err := s.db.UpdateConfig(ctx, perlString(stat.Id), b); err != nil {
			log.Printf("stream: save channels for %s: %v", c.ID, err)
		}
	}
}
