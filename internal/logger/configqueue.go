package logger

import (
	"context"
	"encoding/json"
	"log"
	"time"
)

// configQueue saves the config changes that arrive on chats lines
// (ircwire.IRCClientMessage.Config) one at a time, in the order they were
// queued, on a goroutine of its own, so a save waiting on a connection's
// row lock (an edit in lierc-api holds it while liercd recreates the
// connection) never holds up the chats handler.
//
// A failed save is retried until it succeeds, so an older channel list
// can't land after a newer one. The NSQ message is finished once the change
// is queued, so a change exists only here until it's saved: stop drains the
// queue, but a crash loses what's queued. Each change carries the whole
// channel list, so the next one for a connection repairs a lost one.
type configQueue struct {
	save func(ctx context.Context, connection string, fields []byte) error
	ch   chan configChange
	done chan struct{}
	// ctx ends retries when stop gives up waiting
	ctx    context.Context
	cancel context.CancelFunc
}

type configChange struct {
	connection string
	fields     []byte
}

// configQueueSize is how many changes can wait; queue waits for room
// beyond that, holding up the chats handler rather than losing a change.
const configQueueSize = 1000

// drainTimeout is how long stop waits for the queue to be saved.
var drainTimeout = 5 * time.Second

// saveTimeout bounds one save, which may wait on the row lock.
const saveTimeout = 2 * time.Minute

// start starts the goroutine that saves changes with save.
func (q *configQueue) start(save func(ctx context.Context, connection string, fields []byte) error) {
	q.save = save
	q.ch = make(chan configChange, configQueueSize)
	q.done = make(chan struct{})
	q.ctx, q.cancel = context.WithCancel(context.Background())
	go q.run()
}

// queue queues connection's config change, fields and their new values;
// nil queues nothing.
func (q *configQueue) queue(connection string, config map[string]any) {
	if config == nil {
		return
	}
	fields, err := json.Marshal(config)
	if err != nil {
		log.Printf("dropping config change for %s: %v", connection, err)
		return
	}
	q.ch <- configChange{connection, fields}
}

func (q *configQueue) run() {
	defer close(q.done)
	for c := range q.ch {
		q.saveOne(c)
	}
}

// saveOne saves c, retrying every retryInterval until it's saved or the
// queue is stopped.
func (q *configQueue) saveOne(c configChange) {
	for {
		ctx, cancel := context.WithTimeout(q.ctx, saveTimeout)
		err := q.save(ctx, c.connection, c.fields)
		cancel()
		if err == nil {
			return
		}
		if q.ctx.Err() != nil {
			log.Printf("config change for %s lost at stop: %v: %s", c.connection, err, c.fields)
			return
		}
		log.Printf("saving config for %s: %v (retrying in %s)", c.connection, err, retryInterval)
		select {
		case <-q.ctx.Done():
			log.Printf("config change for %s lost at stop: %s", c.connection, c.fields)
			return
		case <-time.After(retryInterval):
		}
	}
}

// stop saves what's queued, waiting up to drainTimeout; what's left after
// that is logged as lost. Nothing may be queued after stop.
func (q *configQueue) stop() {
	if q.ch == nil {
		return
	}
	close(q.ch)
	select {
	case <-q.done:
	case <-time.After(drainTimeout):
		q.cancel()
		<-q.done
	}
	q.cancel()
}
