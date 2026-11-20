package logger

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/lierc/lierc/pkg/ircwire"
)

func TestMain(m *testing.M) { testenv.Main(m) }

// testStore is a store on a fresh, migrated database (testenv's
// Postgres) with connections c1 (user u1, highlight word "leedo") and c2
// (user u2), and a pool on that database for checking what it stored.
func testStore(t *testing.T) (*pgStore, *pgxpool.Pool) {
	d := testenv.Postgres(t)
	testenv.EnsureConnection(t, d, "c1", "u1")
	testenv.EnsureConnection(t, d, "c2", "u2")
	if _, err := d.Pool.Exec(context.Background(), `UPDATE connections SET config = '{"Highlight": ["leedo"]}' WHERE id = 'c1'`); err != nil {
		t.Fatal(err)
	}
	return &pgStore{db: d}, d.Pool.Pool
}

func TestStoreInsertMessage(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	m := &ircwire.IRCMessage{Command: "privmsg", Params: []string{"#Chan", "hi"}, Time: 1700000000.5, Prefix: &ircwire.IRCPrefix{Name: "bob"}}

	id, err := s.InsertMessage(ctx, "c1", m, []string{"#Chan"}, true)
	if err != nil || id == 0 {
		t.Fatalf("InsertMessage = %d, %v", id, err)
	}

	var user, channel, command, text string
	var highlight bool
	var at time.Time
	err = db.QueryRow(context.Background(), `SELECT user_id, channel, command, highlight, time, message->'Params'->>1 FROM logs WHERE id = $1`, id).
		Scan(&user, &channel, &command, &highlight, &at, &text)
	if err != nil {
		t.Fatal(err)
	}
	if user != "u1" || channel != "#chan" || command != "PRIVMSG" || !highlight || text != "hi" || at.Unix() != 1700000000 {
		t.Errorf("stored %q %q %q %v %v %q", user, channel, command, highlight, at, text)
	}

	// a highlight gets its highlights row too, with the same id and columns
	var n int
	err = db.QueryRow(context.Background(), `SELECT count(*) FROM highlights h JOIN logs l USING (id)
		WHERE id = $1 AND h.user_id = l.user_id AND h.connection_id = l.connection_id AND h.channel = l.channel
		  AND h.time = l.time AND h.message = l.message AND h.self = l.self`, id).Scan(&n)
	if err != nil || n != 1 {
		t.Errorf("highlights row for %d: %d, %v", id, n, err)
	}
	plain, err := s.InsertMessage(ctx, "c1", m, []string{"#chan"}, false)
	if err != nil {
		t.Fatal(err)
	}
	db.QueryRow(context.Background(), `SELECT count(*) FROM highlights WHERE id = $1`, plain).Scan(&n)
	if n != 0 {
		t.Errorf("a message that isn't a highlight got a highlights row")
	}

	if _, err := s.InsertMessage(ctx, "nope", m, []string{"#chan"}, false); !errors.Is(err, ErrNoConnection) {
		t.Errorf("missing connection: err = %v, want ErrNoConnection", err)
	}
}

func TestStoreInsertMessageChannels(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	m := &ircwire.IRCMessage{Command: "QUIT", Params: []string{"bye"}, Time: 1700000000, Prefix: &ircwire.IRCPrefix{Name: "bob"}}

	id, err := s.InsertMessage(ctx, "c1", m, []string{"#B", "#a", "#c"}, false)
	if err != nil {
		t.Fatal(err)
	}

	rows, err := db.Query(context.Background(), `SELECT id, channel FROM logs ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	var last int
	for rows.Next() {
		var channel string
		rows.Scan(&last, &channel)
		got = append(got, channel)
	}
	rows.Close()

	if !equal(got, []string{"#b", "#a", "#c"}) {
		t.Errorf("channels in id order = %q, want #b #a #c", got)
	}
	if id != last {
		t.Errorf("returned id %d, want the last one, %d", id, last)
	}

	if id, err := s.InsertMessage(ctx, "c1", m, nil, false); id != 0 || err != nil {
		t.Errorf("no channels: %d, %v", id, err)
	}
	if _, err := s.InsertMessage(ctx, "nope", m, []string{"#a"}, false); !errors.Is(err, ErrNoConnection) {
		t.Errorf("missing connection: err = %v, want ErrNoConnection", err)
	}
}

func TestStoreInsertPrivate(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()

	if err := s.InsertPrivate(ctx, "c1", "bob", 1700000000); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPrivate(ctx, "c1", "bob", 1700000100); err != nil {
		t.Fatal(err)
	}

	var count int
	var at time.Time
	db.QueryRow(context.Background(), `SELECT count(*), max(time) FROM private_chats`).Scan(&count, &at)
	if count != 1 || at.Unix() != 1700000100 {
		t.Errorf("private: %d rows, time %v; want one row, updated", count, at)
	}
}

func TestStoreOwner(t *testing.T) {
	s, _ := testStore(t)
	ctx := context.Background()
	if u, ok, err := s.Owner(ctx, "c2"); u != "u2" || !ok || err != nil {
		t.Errorf("c2: %q %v %v", u, ok, err)
	}
	if _, ok, err := s.Owner(ctx, "nope"); ok || err != nil {
		t.Errorf("nope: %v %v", ok, err)
	}
}

func TestStoreHighlights(t *testing.T) {
	s, _ := testStore(t)

	configs, err := s.Highlights(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(configs) != 1 || configs["c1"] != `["leedo"]` {
		t.Errorf("Highlights = %v, want only c1's", configs)
	}
}

// A private message for a connection that's gone (deleted, so its row and
// its keys are gone) stores nothing and isn't an error.
func TestInsertPrivateMissingConnection(t *testing.T) {
	s, db := testStore(t)
	if err := s.InsertPrivate(context.Background(), "nope", "bob", 1700000000); err != nil {
		t.Fatalf("err = %v", err)
	}
	var n int
	db.QueryRow(context.Background(), `SELECT count(*) FROM private_chats`).Scan(&n)
	if n != 0 {
		t.Errorf("%d private rows, want 0", n)
	}
}

// A key violation (the connection deleted while the insert ran) is a
// missing connection, not an error to retry.
func TestInsertKeyViolationIsNoConnection(t *testing.T) {
	s, db := testStore(t)
	ctx := context.Background()
	for _, q := range []string{
		`CREATE FUNCTION deleted_meanwhile() RETURNS trigger AS $$
		  BEGIN RAISE foreign_key_violation USING MESSAGE = 'deleted meanwhile'; END $$ LANGUAGE plpgsql`,
		`CREATE TRIGGER deleted_meanwhile BEFORE INSERT ON logs FOR EACH ROW EXECUTE FUNCTION deleted_meanwhile()`,
		`CREATE TRIGGER deleted_meanwhile BEFORE INSERT ON private_chats FOR EACH ROW EXECUTE FUNCTION deleted_meanwhile()`,
	} {
		if _, err := db.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	m := &ircwire.IRCMessage{Command: "PRIVMSG", Params: []string{"#a", "hi"}, Prefix: &ircwire.IRCPrefix{Name: "bob"}}
	if _, err := s.InsertMessage(ctx, "c1", m, []string{"#a"}, false); !errors.Is(err, ErrNoConnection) {
		t.Errorf("InsertMessage: err = %v, want ErrNoConnection", err)
	}
	if _, err := s.InsertMessage(ctx, "c1", m, []string{"#a", "#b"}, false); !errors.Is(err, ErrNoConnection) {
		t.Errorf("InsertMessage, two channels: err = %v, want ErrNoConnection", err)
	}
	if err := s.InsertPrivate(ctx, "c1", "bob", 1700000000); err != nil {
		t.Errorf("InsertPrivate: err = %v, want nil", err)
	}
}
