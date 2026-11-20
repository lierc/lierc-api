package db

import "time"

// MigrateLockKey is the advisory lock key Migrate holds, for tests.
const MigrateLockKey = migrateLockKey

// SetListenPing sets how long ListenHighlights waits before pinging, for
// tests; it returns a func that puts it back.
func SetListenPing(d time.Duration) func() {
	old := listenPing
	listenPing = d
	return func() { listenPing = old }
}
