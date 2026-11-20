package notify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc-api/internal/testenv"
	"github.com/lierc/lierc/pkg/ircwire"
	"golang.org/x/time/rate"
)

// accumulateNotifier has no open streams and a delay long enough that no
// notification is sent during a test.
func accumulateNotifier(t *testing.T, store Store) *Notifier {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	t.Cleanup(srv.Close)
	n := &Notifier{
		Client:        &http.Client{},
		Notifications: map[string]*Notification{},
		Last:          map[string]time.Time{},
		Mu:            &sync.Mutex{},
		Config:        &NotifierConfig{APIStats: srv.URL + "/stats", Delay: time.Hour},
	}
	n.store = store
	t.Cleanup(func() {
		for _, o := range n.Notifications {
			o.Timer.Stop()
		}
	})
	return n
}

func highlight(connection string, at time.Time) *logger.LoggedMessage {
	return &logger.LoggedMessage{
		ConnectionId: connection,
		MessageId:    1,
		Message: &ircwire.IRCMessage{
			Prefix:  &ircwire.IRCPrefix{Name: "bob"},
			Command: "PRIVMSG",
			Params:  []string{"#c", "hi leedo"},
			Time:    float64(at.Unix()),
		},
	}
}

// With the database down, a highlight is dropped instead of panicking.
func TestAccumulateDatabaseDown(t *testing.T) {
	d, err := db.Open(context.Background(), config.Config{DBHost: "127.0.0.1:1", DBName: "lierc"})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	n := accumulateNotifier(t, d)
	n.accumulate(highlight("c1", time.Now()))
	if len(n.Notifications) != 0 {
		t.Errorf("notified: %v", n.Notifications)
	}
}

// UserRateLimit reads Last while Send writes it from timer goroutines; run
// with -race.
func TestUserRateLimitConcurrent(t *testing.T) {
	n := accumulateNotifier(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		u := fmt.Sprint(i % 5)
		go func() {
			defer wg.Done()
			n.Mu.Lock()
			n.Last[u] = time.Now()
			n.Mu.Unlock()
		}()
		go func() {
			defer wg.Done()
			n.UserRateLimit(u)
		}()
	}
	wg.Wait()
	if !n.UserRateLimit("0") {
		t.Error("user 0 not rate limited")
	}
}

func TestMain(m *testing.M) { testenv.Main(m) }

// testDB is a fresh, migrated database (testenv's Postgres) with
// connections c1 (user u1: confirmed, email on, two push subscriptions)
// and c2 (user u2: email off).
func testDB(t *testing.T) *db.DB {
	d := testenv.Postgres(t)
	testenv.EnsureConnection(t, d, "c1", "u1")
	testenv.EnsureConnection(t, d, "c2", "u2")
	for _, q := range []string{
		`UPDATE users SET email = id || '@example.com'`,
		`INSERT INTO prefs (user_id, name, value) VALUES ('u1', 'email', 'true'), ('u2', 'email', 'false')`,
		`INSERT INTO web_push_subscriptions (endpoint, key, auth, user_id) VALUES
			('https://push.example/a', 'ka', 'aa', 'u1'), ('https://push.example/b', 'kb', 'ab', 'u1')`,
	} {
		if _, err := d.Pool.Exec(context.Background(), q); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

// lookup turns the database's answer into a NotificationPref; which
// addresses get email is db.NotifyTarget's, and tested there.
func TestLookup(t *testing.T) {
	n := accumulateNotifier(t, testDB(t))

	user, p, err := n.lookup("c1")
	if err != nil {
		t.Fatal(err)
	}
	if user != "u1" || !p.EmailEnabled || p.EmailAddress != "u1@example.com" || len(p.WebPushConfigs) != 2 {
		t.Errorf("c1: %s %+v", user, p)
	}
	for _, c := range p.WebPushConfigs {
		if c.User != "u1" || c.Endpoint == "" || c.Key == "" || c.Auth == "" {
			t.Errorf("web push %+v", c)
		}
	}

	user, p, err = n.lookup("c2")
	if err != nil || user != "u2" || p.EmailEnabled || len(p.WebPushConfigs) != 0 {
		t.Errorf("c2: %s %+v %v", user, p, err)
	}

	if _, _, err := n.lookup("nope"); err == nil || err.Error() != "unknown connection" {
		t.Errorf("nope: %v", err)
	}
}

func TestAccumulate(t *testing.T) {
	db := testDB(t)
	n := accumulateNotifier(t, db)

	n.accumulate(highlight("c1", time.Now()))
	if o := n.Notifications["u1"]; o == nil || len(o.Messages) != 1 || len(o.Pref.WebPushConfigs) != 2 {
		t.Fatalf("not queued: %+v", n.Notifications)
	}

	// Too old, or on an unknown connection: dropped.
	n.accumulate(highlight("c2", time.Now().Add(-maxMessageAge-time.Minute)))
	n.accumulate(highlight("nope", time.Now()))
	if len(n.Notifications) != 1 {
		t.Errorf("notifications: %+v", n.Notifications)
	}

	// More highlights in a row than the pool has connections: each lookup
	// must release its connection, or a later one blocks.
	done := make(chan struct{})
	go func() {
		for i := 0; i < 20; i++ {
			n.accumulate(highlight("c1", time.Now()))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("accumulate blocked: connection not released")
	}
	if o := n.Notifications["u1"]; len(o.Messages) != 21 {
		t.Errorf("u1 has %d messages", len(o.Messages))
	}
}

// An SMTP failure is logged instead of panicking, and still counts toward
// the user rate limit.
func TestSendEmailFailure(t *testing.T) {
	refused, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := refused.Addr().(*net.TCPAddr).Port
	refused.Close()

	n := accumulateNotifier(t, nil)
	n.Limiter = rate.NewLimiter(rate.Inf, 1)
	n.Config.SMTPHost, n.Config.SMTPPort = "127.0.0.1", port
	n.Send(&Notification{
		User:     "u1",
		Pref:     &NotificationPref{EmailEnabled: true, EmailAddress: "u1@example.com"},
		Messages: []*logger.LoggedMessage{highlight("c1", time.Now())},
	})
	if !n.UserRateLimit("u1") {
		t.Error("send not recorded")
	}
}

// A highlight for a user with an open stream is dropped, and logged.
func TestAccumulateOpenStreamsLogged(t *testing.T) {
	db := testDB(t)
	logs := captureLog(t)
	n := accumulateNotifier(t, db)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"u1":1}`))
	}))
	defer srv.Close()
	n.Config.APIStats = srv.URL + "/stats"

	n.accumulate(highlight("c1", time.Now()))
	if len(n.Notifications) != 0 {
		t.Errorf("queued: %+v", n.Notifications)
	}
	logs.mustContain(t, "highlight 1 for u1 not notified: 1 open streams")
}
