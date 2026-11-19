// Package archive moves old log rows into compressed blocks in log_archives,
// and deletes old status window messages.
//
// Per connection and channel, the archivable messages are all but the Keep
// newest, however recent. Channels move in full blocks of Block messages, oldest first; status
// windows are deleted in batches of up to Block messages instead.
package archive

import (
	"context"
	"fmt"
	"strings"

	"github.com/lierc/lierc-api/internal/db"
)

// statusChannel is the channel name the logger uses for status windows.
const statusChannel = "status"

// IsStatus reports whether a channel is connection noise rather than a
// conversation: the status window, or a name starting with * (ZNC's
// *status and other modules, server notices like *.freenode.net), which no
// nick can start with. Their old messages are deleted, never archived.
func IsStatus(channel string) bool {
	return channel == statusChannel || strings.HasPrefix(channel, "*")
}

type Options struct {
	Keep, Block int
	// Connections limits Candidates to these connection ids; empty means all.
	Connections []string
}

type Channel struct {
	Connection, Channel string
	// Movable is the number of archivable messages, counted only up to a
	// block.
	Movable int
	Status  bool
}

type Block struct {
	Connection, Channel    string
	FirstID, LastID, Count int
}

func (o Options) check() error {
	if o.Keep < 1 || o.Block < 1 {
		return fmt.Errorf("archive: keep (%d) and block (%d) must be positive", o.Keep, o.Block)
	}
	return nil
}

// Candidates lists the channels with at least a block of archivable messages,
// and the status windows with any messages to delete.
func Candidates(ctx context.Context, store db.Store, o Options) ([]Channel, error) {
	if err := o.check(); err != nil {
		return nil, err
	}
	counts, err := store.ArchiveCandidates(ctx, o.Keep, o.Block, o.Connections)
	if err != nil {
		return nil, err
	}
	var cs []Channel
	for _, n := range counts {
		c := Channel{Connection: n.Connection, Channel: n.Channel, Movable: n.Movable, Status: IsStatus(n.Channel)}
		if c.Movable >= o.Block || c.Status {
			cs = append(cs, c)
		}
	}
	return cs, nil
}

// MoveBlock moves the oldest Block archivable messages of c into one
// log_archives row, in one statement. It returns false, changing nothing,
// when fewer than Block messages are archivable.
func MoveBlock(ctx context.Context, store db.Store, c Channel, o Options) (Block, bool, error) {
	if err := o.check(); err != nil {
		return Block{}, false, err
	}
	if IsStatus(c.Channel) {
		return Block{}, false, fmt.Errorf("archive: %s is a status window: dropped, not archived", c.Channel)
	}
	r, ok, err := store.ArchiveBlock(ctx, c.Connection, c.Channel, o.Keep, o.Block)
	if err != nil || !ok {
		return Block{}, false, err
	}
	return Block{Connection: c.Connection, Channel: c.Channel, FirstID: r.FirstID, LastID: r.LastID, Count: r.Count}, true, nil
}

// DropStatus deletes up to Block of the oldest messages below the Keep
// newest of the status window c, reporting what it deleted. It returns false when none
// are left.
func DropStatus(ctx context.Context, store db.Store, c Channel, o Options) (Block, bool, error) {
	if err := o.check(); err != nil {
		return Block{}, false, err
	}
	if !IsStatus(c.Channel) {
		return Block{}, false, fmt.Errorf("archive: %s is not a status window", c.Channel)
	}
	r, ok, err := store.DropStatusBlock(ctx, c.Connection, c.Channel, o.Keep, o.Block)
	if err != nil || !ok {
		return Block{}, false, err
	}
	return Block{Connection: c.Connection, Channel: c.Channel, FirstID: r.FirstID, LastID: r.LastID, Count: r.Count}, true, nil
}
