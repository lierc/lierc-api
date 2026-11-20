package logger

import (
	"context"
	"sync"
	"time"
)

// owners caches each connection's owner. A connection's owner never
// changes (nothing updates connections.user_id), so an entry never goes
// stale; one for a deleted connection is never used again.
type owners struct {
	mu sync.Mutex
	m  map[string]string
}

// ownerTimeout bounds one lookup, so a slow database can't hold up the
// handler for long.
const ownerTimeout = 5 * time.Second

// owner is the user who owns connection, from the cache or, the first
// time, the database; ok is false when there's no such connection. Only
// an owner found is cached.
func (l *Logger) owner(connection string) (user string, ok bool, err error) {
	l.owners.mu.Lock()
	user, ok = l.owners.m[connection]
	l.owners.mu.Unlock()
	if ok {
		return user, true, nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), ownerTimeout)
	defer cancel()
	user, ok, err = l.store.Owner(ctx, connection)
	if err != nil || !ok {
		return "", false, err
	}

	l.owners.mu.Lock()
	if l.owners.m == nil {
		l.owners.m = make(map[string]string)
	}
	l.owners.m[connection] = user
	l.owners.mu.Unlock()
	return user, true, nil
}

// forget drops connection from the cache: it was deleted after it was
// cached (an insert found no connection).
func (l *Logger) forget(connection string) {
	l.owners.mu.Lock()
	delete(l.owners.m, connection)
	l.owners.mu.Unlock()
}
