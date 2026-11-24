package db

import (
	"time"
)

// MigrateLockKey is the advisory lock key Migrate holds, for tests.
const MigrateLockKey = migrateLockKey

// SetListenPing sets how long ListenHighlights waits before pinging, for
// tests; it returns a func that puts it back.
func SetListenPing(d time.Duration) func() {
	old := listenPing
	listenPing = d
	return func() { listenPing = old }
}

// BlockHoldingSQL is the archive block lookup's SQL, for EXPLAIN in tests.
func BlockHoldingSQL(ch, id int32) (string, []any, error) {
	return blockHoldingQuery(ch, id).ToSql()
}

// MessageIDsLookupSQL is the message_ids lookup's SQL, for EXPLAIN in tests.
func MessageIDsLookupSQL(conn, msgid string) (string, []any, error) {
	return messageIDsLookup(conn, msgid).ToSql()
}
