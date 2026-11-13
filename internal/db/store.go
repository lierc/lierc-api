package db

import (
	"context"
	"encoding/json"
	"time"
)

// Store is every query the API and the stream server make, so they can run
// on a fake in tests. *DB is the Postgres implementation. It leaves out
// Close and the pool.
type Store interface {
	// users
	UserState(ctx context.Context, id string) (exists, unverified bool, err error)
	UserExists(ctx context.Context, id string) (bool, error)
	SaveLastLogin(ctx context.Context, user string) error
	LatestMessageID(ctx context.Context, user string) (int64, error)

	// logins
	InsertLogin(ctx context.Context, tokenHash []byte, user, userAgent, ip string) error
	LoginByHash(ctx context.Context, tokenHash []byte) (Login, bool, error)
	TouchLogin(ctx context.Context, id int64) error
	DeleteLogin(ctx context.Context, user string, id int64) (bool, error)
	DeleteLoginByHash(ctx context.Context, tokenHash []byte) error
	DeleteLogins(ctx context.Context, user string) (int64, error)
	LoginAlive(ctx context.Context, id int64) (bool, error)
	Logins(ctx context.Context, user string) ([]LoginInfo, error)
	PruneLogins(ctx context.Context) (int64, error)

	// accounts
	AccountByID(ctx context.Context, id string) (Account, bool, error)
	AccountsByLogin(ctx context.Context, name string) ([]AccountAuth, error)
	AccountAuthByEmail(ctx context.Context, email string) (AccountAuth, bool, error)
	AccountAuthByID(ctx context.Context, id string) (AccountAuth, bool, error)
	InsertUser(ctx context.Context, id, username, email, hash string) error
	UserHasConnection(ctx context.Context, user string) (bool, error)
	ClaimVerifyResend(ctx context.Context, id string, minSeconds float64) (bool, error)
	ReplacePasswordHash(ctx context.Context, id, hash string, oldHash *string) (bool, error)
	MarkVerified(ctx context.Context, id string) (bool, error)
	ReplacePasswordAndVerify(ctx context.Context, id, hash string, oldHash *string) (changed, firstVerify bool, err error)

	// connections
	Connections(ctx context.Context, user string) ([]Connection, error)
	LookupConfig(ctx context.Context, id string) ([]byte, bool, error)
	LookupConnection(ctx context.Context, id string) (owner string, cfg json.RawMessage, ok bool, err error)
	LookupLiercd(ctx context.Context, id string) (string, error)
	LookupOwner(ctx context.Context, id string) (string, bool, error)
	MergeConfig(ctx context.Context, id string, fields []byte) error
	EditConfig(ctx context.Context, id string, edit func(saved []byte) ([]byte, error)) (found bool, err error)
	SetLiercd(ctx context.Context, id, instance string) error
	SaveConnectionLimited(ctx context.Context, id, user string, config []byte, max int) (bool, error)
	SaveConnection(ctx context.Context, id, user string, config []byte) error
	DeleteConnection(ctx context.Context, id string) error

	// notifications
	NotifyHighlights(ctx context.Context) error

	// history
	LogSince(ctx context.Context, user string, after int32) ([]UserMessage, error)
	Highlights(ctx context.Context, user string, before *int32, limit int) ([]UserMessage, error)
	ChannelLogs(ctx context.Context, conn, channel string, limit int) ([]Message, error)
	ChannelLogsBefore(ctx context.Context, conn, channel string, before int32, limit int) ([]Message, error)
	ChannelLast(ctx context.Context, conn, channel string, query *string, limit int) ([]Message, error)
	ChannelDate(ctx context.Context, conn, channel, from, to string, text, sender *string, each func(Message) error) error

	// preferences, private chats, seen, web push
	Prefs(ctx context.Context, user string) ([]Pref, error)
	SetPref(ctx context.Context, user, name, value string) error
	Privates(ctx context.Context, user string) ([]Private, error)
	AddPrivate(ctx context.Context, conn, nick string) error
	DeletePrivate(ctx context.Context, conn, nick string) error
	Seen(ctx context.Context, user string) ([]SeenRow, error)
	SetSeen(ctx context.Context, user, conn, channel string, id int32) error
	ChannelNames(ctx context.Context, conn string) ([]string, error)
	Missed(ctx context.Context, user string, es []MissedEntry) (map[string]map[string]MissedCounts, error)
	SaveWebPush(ctx context.Context, user, endpoint, key, auth string) error
	DeleteWebPush(ctx context.Context, user, endpoint string) error

	// archive and lctl's maintenance commands
	ArchiveCandidates(ctx context.Context, keep int, cutoff time.Time, block int, conns []string) ([]ChannelCount, error)
	ArchiveBlock(ctx context.Context, conn, channel string, keep int, cutoff time.Time, block int) (ArchivedBlock, bool, error)
	DropStatusBlock(ctx context.Context, conn, channel string, keep int, cutoff time.Time, block int) (ArchivedBlock, bool, error)
	GoneConnections(ctx context.Context) ([]string, error)
	GoneUsers(ctx context.Context) ([]string, error)
	CountOrphanArchive(ctx context.Context) (int, error)
	DeleteGoneConnectionLogs(ctx context.Context, id string, batch int) (int64, error)
	DeleteGoneUserLogs(ctx context.Context, id string, batch int) (int64, error)
	DeleteOrphanArchive(ctx context.Context, batch int) (int64, error)
	ReviveCandidates(ctx context.Context, since time.Duration, instance string) ([]ReviveConnection, error)

	// the admin API's reads
	ListUsers(ctx context.Context, f UserFilter) ([]UserRow, error)
	LookupUsers(ctx context.Context, q string) ([]UserRow, error)
	UserCounts(ctx context.Context, id string) (map[string]int64, error)
	ListConnections(ctx context.Context, f ConnectionFilter) ([]ConnectionRow, error)
	ConnectionCounts(ctx context.Context, id string) (map[string]int64, error)
	LastLogID(ctx context.Context) (int64, error)
	StaleSeenCount(ctx context.Context) (int64, error)

	// the admin API's writes
	DeleteUserData(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error)
	DeleteConnectionData(ctx context.Context, id string) ([]ConnectionChange, map[string]int64, error)
	SweepLateLogs(ctx context.Context, user string, connections []string, after int64) (rows, blocks int64, err error)
	PruneStaleSeen(ctx context.Context) (int64, error)
}

var _ Store = (*DB)(nil)
