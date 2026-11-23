package stream

import (
	"context"
	"encoding/json"
	"errors"
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
	Config *struct {
		Alias, Host string
		Port        int
	}
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

// isupportPerLine is the most tokens in one 005, as servers send them:
// the nick, the tokens and the trailing text stay within 15 params.
const isupportPerLine = 13

// isupportLines is the params of the 005s that send tokens: the nick, a
// param per token (up to isupportPerLine), then the trailing text.
func isupportLines(nick string, tokens []string) [][]any {
	var lines [][]any
	for len(tokens) > 0 {
		n := min(len(tokens), isupportPerLine)
		params := []any{nick}
		for _, tok := range tokens[:n] {
			params = append(params, tok)
		}
		lines = append(lines, append(params, "are supported by this server"))
		tokens = tokens[n:]
	}
	return lines
}

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

// ircEvent is an "irc" event for connection id. Time is Unix seconds as a
// float (hiresTime).
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

// replay pushes the fake events that bring a new stream up to date. If any
// connection's status cannot be found or recreated, nothing is sent.
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
	// the UI adds missing ones and drops ones deleted while it was away.
	evs = append(evs, snapshotEvent(statuses))
	// Welcome, ISUPPORT and CAP ACK, shaped as a server sends them; a
	// missing Isupport is treated as empty.
	for _, stat := range registered {
		emit(stat.Id, liercdPrefix, rplWelcome, stat.Nick, "Welcome to the Internet Relay Network "+stat.Nick)
		for _, params := range isupportLines(stat.Nick, stat.Isupport) {
			emit(stat.Id, liercdPrefix, rplISupport, params...)
		}
		if len(stat.CapsAcked) > 0 {
			emit(stat.Id, liercdPrefix, "CAP", stat.Nick, "ACK", strings.Join(stat.CapsAcked, " "))
		}
	}
	// Joins
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			emit(stat.Id, stat.Nick, "JOIN", ch.Name)
		}
	}
	// Topics
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if ch.Topic != nil && ch.Topic.Topic != "" {
				emit(stat.Id, liercdPrefix, rplTopic, stat.Nick, ch.Name, ch.Topic.Topic)
				emit(stat.Id, liercdPrefix, rplTopicTime, stat.Nick, ch.Name, ch.Topic.User, ch.Topic.Time)
			}
		}
	}
	// Names: an empty Nicks array still gets its 366.
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
	// Modes, as a server's 324: the nick, the channel, then the modes
	for _, stat := range registered {
		for _, ch := range stat.Channels {
			if ch.Mode != "" {
				emit(stat.Id, liercdPrefix, rplChannelModeIs, stat.Nick, ch.Name, ch.Mode)
			}
		}
	}

	for _, ev := range evs {
		if !st.push(ev) {
			return
		}
	}
}

// errSkip leaves a connection's config as it is.
var errSkip = errors.New("skip")

// saveChannels stores each connection's current channel list from liercd in
// its config. Failures skip that connection.
func (s *Server) saveChannels(ctx context.Context, user string) {
	conns, err := s.db.Connections(ctx, user)
	if err != nil {
		log.Printf("stream: save channels: %v", err)
		return
	}
	for _, c := range conns {
		res, err := s.lc.Do(ctx, http.MethodGet, c.ID+"/status", nil)
		if err != nil || res.Status != http.StatusOK {
			continue
		}
		var stat struct {
			Id       string
			Channels []struct{ Name string }
		}
		if json.Unmarshal(res.Body, &stat) != nil || len(stat.Channels) == 0 {
			continue
		}
		if stat.Id != c.ID {
			log.Printf("stream: save channels: liercd answered %q for %s, skipped", stat.Id, c.ID)
			continue
		}
		names := make([]string, len(stat.Channels))
		for i, ch := range stat.Channels {
			names[i] = ch.Name
		}
		// The row is locked from read to write, so a channel liercd saves
		// meanwhile (MergeConfig) isn't overwritten; a row that's gone, or
		// isn't an object, is skipped.
		_, err = s.db.EditConfig(ctx, c.ID, func(stored []byte) ([]byte, error) {
			var cfg map[string]json.RawMessage
			if json.Unmarshal(stored, &cfg) != nil {
				return nil, errSkip
			}
			if cfg == nil {
				cfg = map[string]json.RawMessage{}
			}
			var err error
			if cfg["Channels"], err = respond.Marshal(names); err != nil {
				return nil, errSkip
			}
			return respond.Marshal(cfg)
		})
		if err != nil && !errors.Is(err, errSkip) {
			log.Printf("stream: save channels for %s: %v", c.ID, err)
		}
	}
}

type snapshotEntry struct {
	Id, Name, Nick string
	Connected      bool
	Host           string `json:",omitempty"`
	Port           int    `json:",omitempty"`
}

// snapshotEvent is the replay's connections event: each connection's id,
// network name (its alias, else its host), current nick, whether it's
// connected, and its server (host and port), as liercd's status says.
func snapshotEvent(statuses []*status) []byte {
	list := make([]snapshotEntry, 0, len(statuses))
	for _, stat := range statuses {
		e := snapshotEntry{Id: stat.Id, Nick: stat.Nick, Connected: stat.Status != nil && stat.Status.Connected}
		if stat.Config != nil {
			e.Name, e.Host, e.Port = stat.Config.Alias, stat.Config.Host, stat.Config.Port
			if e.Name == "" {
				e.Name = stat.Config.Host
			}
		}
		list = append(list, e)
	}
	b, err := respond.Marshal(list)
	if err != nil {
		panic(err) // strings, bools and ints always marshal
	}
	return sseEvent("connections", b, "")
}
