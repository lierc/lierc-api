package db

import (
	"context"

	"github.com/jackc/pgx/v5/pgconn"
)

// ConnectionsChannel is the Postgres channel connection changes are
// announced on: the Perl API and lctl NOTIFY it, the stream server LISTENs
// and sends connection:create, :update and :delete to the owner's streams.
const ConnectionsChannel = "connections"

// ConnectionChange is a notification's payload. Action is create, update
// or delete.
type ConnectionChange struct {
	User       string `json:"user"`
	Connection string `json:"connection"`
	Action     string `json:"action"`
}

// Execer is a pgx.Tx or a pool.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// NotifyConnectionDeletes announces a delete of each connection matching
// where (a condition on the connection table, such as `id = $1`, with arg
// as $1), with its owner. Run it in the deleting transaction, before the
// rows go: Postgres sends the notifications only if that commits.
func NotifyConnectionDeletes(ctx context.Context, q Execer, where string, arg any) error {
	_, err := q.Exec(ctx, `SELECT pg_notify('`+ConnectionsChannel+`',
		json_build_object('user', "user", 'connection', id, 'action', 'delete')::text)
		FROM connection WHERE `+where, arg)
	return err
}
