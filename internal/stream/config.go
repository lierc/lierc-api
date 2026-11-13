package stream

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
)

// configChannel is the API's channel on the config topic. Unlike the
// stream's channels it's durable: liercd's changes queue while the API
// restarts, and are saved once it's back.
const configChannel = "lierc-api"

// configChanged saves the config fields liercd changed itself (its
// channels as it joins and parts, a nick it was asked to change to),
// merging them into the saved config. A message that can't be read, or
// has nothing to save, is dropped; a database error is returned, so NSQ
// delivers it again. Merging the same fields again is harmless. A
// connection that's gone isn't an error.
func (s *Server) configChanged(body []byte) error {
	var c struct {
		Id     string
		Config map[string]json.RawMessage
	}
	if err := json.Unmarshal(body, &c); err != nil {
		log.Printf("stream: dropping unreadable config change: %v\n%s", err, body)
		return nil
	}
	if c.Id == "" || len(c.Config) == 0 {
		log.Printf("stream: dropping empty config change\n%s", body)
		return nil
	}
	fields, err := json.Marshal(c.Config)
	if err != nil {
		log.Printf("stream: dropping config change for %s: %v", c.Id, err)
		return nil
	}
	if err := s.db.MergeConfig(context.Background(), c.Id, fields); err != nil {
		return fmt.Errorf("saving config for %s: %w", c.Id, err)
	}
	return nil
}
