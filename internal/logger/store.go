package logger

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc/pkg/ircwire"
)

// ErrNoConnection is returned when a message's connection isn't in the
// database, so there's no user to log it for.
var ErrNoConnection = errors.New("no such connection")

// Store is where logged messages are kept.
type Store interface {
	// InsertMessage logs m once under each of channels, in order, and
	// returns the last id.
	InsertMessage(ctx context.Context, connection string, m *ircwire.IRCMessage, channels []string, highlight bool) (int, error)
	// InsertPrivate records that nick sent connection a private message.
	InsertPrivate(ctx context.Context, connection string, nick string, time float64) error
	// Highlights returns each connection's highlight words, as JSON.
	Highlights(ctx context.Context) (map[string]string, error)
	// Owner returns the user who owns connection; ok is false when
	// there's no such connection.
	Owner(ctx context.Context, connection string) (user string, ok bool, err error)
}

// pgStore is the Store on lierc-api's database layer.
type pgStore struct {
	db *db.DB
}

// entry is m as the database stores it: the line as JSON, the command
// upper case.
func entry(connection string, m *ircwire.IRCMessage) (db.LogEntry, error) {
	v, err := json.Marshal(m)
	if err != nil {
		return db.LogEntry{}, err
	}
	return db.LogEntry{
		Connection: connection,
		Command:    strings.ToUpper(m.Command),
		Message:    v,
		Time:       m.Time,
		Self:       m.Prefix.Self,
	}, nil
}

// noConnection turns db.ErrGone, a connection missing or deleted while
// the insert ran, into ErrNoConnection. Retrying can't help.
func noConnection(err error) error {
	if errors.Is(err, db.ErrGone) {
		return ErrNoConnection
	}
	return err
}

func (s *pgStore) InsertMessage(ctx context.Context, connection string, m *ircwire.IRCMessage, channels []string, highlight bool) (int, error) {
	e, err := entry(connection, m)
	if err != nil {
		return 0, err
	}
	lower := make([]string, len(channels))
	for i, channel := range channels {
		lower[i] = strings.ToLower(channel)
	}
	id, err := s.db.InsertLog(ctx, e, lower, highlight)
	return int(id), noConnection(err)
}

// InsertPrivate records the time of a private message with nick on
// connection; for a connection that's gone it stores nothing.
func (s *pgStore) InsertPrivate(ctx context.Context, connection string, nick string, time float64) error {
	err := s.db.SavePrivateAt(ctx, connection, nick, time)
	if errors.Is(err, db.ErrGone) {
		return nil
	}
	return err
}

func (s *pgStore) Owner(ctx context.Context, connection string) (string, bool, error) {
	return s.db.LookupOwner(ctx, connection)
}

func (s *pgStore) Highlights(ctx context.Context) (map[string]string, error) {
	return s.db.HighlightWords(ctx)
}
