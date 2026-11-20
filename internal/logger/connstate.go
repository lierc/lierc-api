package logger

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/nsqio/go-nsq"
)

// handleConfig saves the config fields liercd changed itself (the config
// topic: its channels as it joins and parts, a nick it was asked to change
// to), merging them into the saved config. A message that can't be read,
// or has nothing to save, is dropped; a database error is returned, so NSQ
// delivers it again, for as long as it takes (retryForeverConfig). Merging
// the same fields again is harmless. A connection that's gone isn't an
// error.
func (l *Logger) handleConfig(n *nsq.Message) error {
	var c struct {
		Id     string
		Config map[string]json.RawMessage
	}
	if err := json.Unmarshal(n.Body, &c); err != nil {
		log.Printf("dropping unreadable config change: %v\n%s", err, n.Body)
		return nil
	}
	if c.Id == "" || len(c.Config) == 0 {
		log.Printf("dropping empty config change\n%s", n.Body)
		return nil
	}
	fields, err := json.Marshal(c.Config)
	if err != nil {
		log.Printf("dropping config change for %s: %v", c.Id, err)
		return nil
	}
	if err := l.store.MergeConfig(context.Background(), c.Id, fields); err != nil {
		return fmt.Errorf("saving config for %s: %w", c.Id, err)
	}
	return nil
}

// handleStatus hands liercd's state for a connection (the status topic) to
// the Output, with its owner. It's of use only live, so it's never
// retried: a message that can't be read, or whose owner can't be looked
// up, is dropped and logged, and one for a connection that's gone is
// dropped silently.
func (l *Logger) handleStatus(n *nsq.Message) error {
	var m struct {
		Id        string
		Connected bool
	}
	if err := json.Unmarshal(n.Body, &m); err != nil || m.Id == "" {
		log.Printf("dropping unreadable status message: %q", n.Body)
		return nil
	}
	user, ok, err := l.owner(m.Id)
	if err != nil {
		log.Printf("dropping status of %s: looking up its owner: %v", m.Id, err)
		return nil
	}
	if !ok {
		return nil
	}
	l.out.Status(&StatusChange{ConnectionId: m.Id, Connected: m.Connected, UserId: user})
	return nil
}
