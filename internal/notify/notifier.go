package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc-api/internal/mail"
	"golang.org/x/time/rate"
)

type Notifier struct {
	Client        *http.Client
	Notifications map[string]*Notification
	Last          map[string]time.Time
	Mu            *sync.Mutex
	Limiter       *rate.Limiter
	Config        *NotifierConfig
	Queue         chan *logger.LoggedMessage

	// PushHTTP sends web pushes; nil means a client that refuses private
	// addresses (newPushClient). Tests set it to reach httptest servers.
	PushHTTP *http.Client
	pushOnce sync.Once

	// Streams says whether a user is online; nil means asking API_STATS
	// over HTTP (StatsStreams, with Client).
	Streams StreamCounter

	// store is the logger's database, shared; nil in some tests
	store Store

	// accumulating is closed when Accumulate returns
	accumulating chan struct{}
}

// StreamCounter says how many event streams a user has open; a user with
// one gets no notification.
type StreamCounter interface {
	StreamCount(ctx context.Context, user string) (int, error)
}

// Store is the notifier's queries; *db.DB has them.
type Store interface {
	NotifyTarget(ctx context.Context, conn string) (db.NotifyTarget, bool, error)
	DeleteWebPush(ctx context.Context, user, endpoint string) error
}

const DefaultUserRateLimit = 10 * time.Minute

type NotifierConfig struct {
	APIStats string
	APIURL   string
	// UnsubscribeSecret signs the emails' unsubscribe links
	// (UNSUBSCRIBE_SECRET); lierc-api checks them with the same one.
	UnsubscribeSecret string
	VAPIDPrivateKey   string
	VAPIDPublicKey    string
	// SMTP is where emails go (SMTP_OPTS, as lierc-api reads it); the
	// zero Opts is mail's defaults
	SMTP          mail.Opts
	Delay         time.Duration
	UserRateLimit time.Duration
}

// SetUserRateLimit sets how long to wait before notifying the same user
// again, from a duration string as found in the USER_RATE_LIMIT env var. For
// example: 30s, 10m, 1h. An empty value falls back to 10m.
func (c *NotifierConfig) SetUserRateLimit(limit string) error {
	limit = strings.TrimSpace(limit)

	if limit == "" {
		c.UserRateLimit = DefaultUserRateLimit
		return nil
	}

	d, err := time.ParseDuration(limit)

	if err != nil {
		return fmt.Errorf("invalid user rate limit %s: %s", limit, err)
	}

	if d < 0 {
		return fmt.Errorf("user rate limit cannot be negative: %s", limit)
	}

	c.UserRateLimit = d

	return nil
}

// statsTimeout bounds one stream count, so a hung stream server can't
// stall the queue.
var statsTimeout = 5 * time.Second

// StatsStreams asks the stream server's admin listener (API_STATS, on
// loopback, no key) for a user's open streams.
type StatsStreams struct {
	Client *http.Client
	URL    string
}

func (s StatsStreams) StreamCount(ctx context.Context, user string) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", s.URL, nil)
	if err != nil {
		return 0, err
	}

	res, err := s.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer res.Body.Close()

	body, err := io.ReadAll(res.Body)
	if err != nil {
		return 0, err
	}
	if res.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(body)))
	}

	var counts map[string]int
	if err := json.Unmarshal(body, &counts); err != nil {
		return 0, err
	}

	return counts[user], nil
}

// StreamCount is how many streams user has open, from Streams.
func (n *Notifier) StreamCount(user string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), statsTimeout)
	defer cancel()
	streams := n.Streams
	if streams == nil {
		streams = StatsStreams{Client: n.Client, URL: n.Config.APIStats}
	}
	return streams.StreamCount(ctx, user)
}

// openStreams is how many streams the user has open. If stats can't be
// fetched it logs and answers 0: notifying someone who may be online
// (still subject to the rate limits) beats dropping the notification.
func (n *Notifier) openStreams(user string) int {
	count, err := n.StreamCount(user)
	if err != nil {
		log.Printf("stream stats unavailable, notifying %s anyway: %v", user, err)
		return 0
	}
	return count
}

func (n *Notifier) RemoveNotification(user string) {
	n.Mu.Lock()
	defer n.Mu.Unlock()

	if o, ok := n.Notifications[user]; ok {
		o.Timer.Stop()
		delete(n.Notifications, user)
	}
}

func (n *Notifier) AddNotification(u string, p *NotificationPref, m *logger.LoggedMessage) {
	n.Mu.Lock()
	defer n.Mu.Unlock()

	if o, ok := n.Notifications[u]; ok {
		o.Messages = append(o.Messages, m)
		o.Timer.Reset(n.Config.Delay)
		log.Printf("highlight %d for %s added to pending notification (%d messages)", m.MessageId, u, len(o.Messages))
		return
	}

	log.Printf("highlight %d for %s: notifying in %s", m.MessageId, u, n.Config.Delay)

	o := &Notification{
		Pref:     p,
		User:     u,
		Messages: []*logger.LoggedMessage{m},
	}

	n.Notifications[u] = o

	o.Timer = time.AfterFunc(n.Config.Delay, func() {
		n.RemoveNotification(u)
		if streams := n.openStreams(u); streams > 0 {
			log.Printf("not notifying %s: %d open streams", u, streams)
			return
		}
		n.Send(o)
	})
}

// maxMessageAge is how old a highlight may be before it is discarded rather
// than notified, e.g. after the logger was down and nsqd replays a backlog of chats.
const maxMessageAge = 5 * time.Minute

// Accumulate handles what Notify queues, until Stop.
func (n *Notifier) Accumulate() {
	defer close(n.accumulating)
	for m := range n.Queue {
		n.accumulate(m)
	}
}

// Stop ends Accumulate, once it has handled the queue, and waits for it,
// so call it only once Accumulate has been started, and after the
// logger's Stop: a Notify after Stop panics.
// Notifications waiting for their delay are not sent.
func (n *Notifier) Stop() {
	close(n.Queue)
	<-n.accumulating
}

// accumulate handles one highlight. Errors are logged and the highlight
// dropped, so one bad message or a database outage never stops the notifier.
func (n *Notifier) accumulate(m *logger.LoggedMessage) {
	received := time.Unix(int64(m.Message.Time), 0)
	if received.Add(maxMessageAge).Before(time.Now()) {
		log.Printf("discarding highlight %d on %s: too old (%s)", m.MessageId, m.ConnectionId, received.UTC().Format(time.RFC3339))
		return
	}

	user, p, err := n.lookup(m.ConnectionId)
	if err != nil {
		log.Printf("discarding highlight %d on %s: %v", m.MessageId, m.ConnectionId, err)
		return
	}

	// Skip if user has any streams open
	if streams := n.openStreams(user); streams > 0 {
		log.Printf("highlight %d for %s not notified: %d open streams", m.MessageId, user, streams)
		n.RemoveNotification(user)
		return
	}

	n.AddNotification(user, p, m)
}

// lookup finds the user who owns a connection and how to notify them.
func (n *Notifier) lookup(connection string) (string, *NotificationPref, error) {
	if n.store == nil {
		return "", nil, errors.New("no database")
	}
	t, ok, err := n.store.NotifyTarget(context.Background(), connection)
	if err != nil {
		return "", nil, fmt.Errorf("looking up connection: %w", err)
	}
	if !ok {
		return "", nil, fmt.Errorf("unknown connection")
	}

	p := &NotificationPref{EmailEnabled: t.EmailOn, EmailAddress: t.Email}
	for _, w := range t.WebPush {
		p.WebPushConfigs = append(p.WebPushConfigs, &WebPushConfig{User: t.User, Endpoint: w.Endpoint, Key: w.Key, Auth: w.Auth})
	}
	return t.User, p, nil
}

func (n *Notifier) userRateLimit() time.Duration {
	if n.Config.UserRateLimit == 0 {
		return DefaultUserRateLimit
	}
	return n.Config.UserRateLimit
}

func (n *Notifier) UserRateLimit(user string) bool {
	limit := n.userRateLimit()

	n.Mu.Lock()
	ts, ok := n.Last[user]
	n.Mu.Unlock()

	if ok {
		now := time.Now()
		if ts.Add(limit).After(now) {
			return true
		}
	}
	return false
}

func (n *Notifier) GlobalRateLimit() bool {
	rv := n.Limiter.Reserve()
	if !rv.OK() {
		return true
	}
	if delay := rv.Delay(); delay > 0 {
		log.Printf("global rate limit: waiting %s", delay)
		time.Sleep(delay)
	}
	return false
}

func (n *Notifier) RateLimit(user string) bool {
	if n.UserRateLimit(user) {
		log.Printf("not notifying %s: notified less than %s ago", user, n.userRateLimit())
		return true
	}

	if n.GlobalRateLimit() {
		log.Printf("not notifying %s: global rate limit", user)
		return true
	}

	return false
}

// Send notifies the user by email and every web push subscription they
// have. Each method runs on its own, so one that fails, panics or hangs
// doesn't stop the others being tried.
func (n *Notifier) Send(o *Notification) {
	if !o.Pref.EmailEnabled && len(o.Pref.WebPushConfigs) == 0 {
		log.Printf("not notifying %s: email off and no web push subscriptions", o.User)
		return
	}

	if n.RateLimit(o.User) {
		return
	}

	// Recorded before sending, so the user rate limit holds while a slow
	// send is still going.
	n.Mu.Lock()
	n.Last[o.User] = time.Now()
	n.Mu.Unlock()

	var wg sync.WaitGroup
	// Each send is counted once, under kind: by what f reports, or as a
	// failure if it panics.
	attempt := func(kind, what string, f func() bool) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status := "failure"
			defer func() {
				if r := recover(); r != nil {
					log.Printf("%s for %s failed: panic: %v", what, o.User, r)
				}
				notificationsTotal[kind+"/"+status].Inc()
			}()
			if f() {
				status = "success"
			}
		}()
	}

	if o.Pref.EmailEnabled {
		attempt("email", "email", func() bool { return n.sendEmail(o) })
	}
	for _, c := range o.Pref.WebPushConfigs {
		attempt("webpush", "web push to "+c.Endpoint, func() bool { return n.sendWebPush(o, c) })
	}
	wg.Wait()
}

// sendEmail emails o, logs the outcome and reports whether it was sent.
func (n *Notifier) sendEmail(o *Notification) bool {
	if err := n.SendEmail(o.Messages, o.Pref.EmailAddress, o.User); err != nil {
		log.Printf("email to %s failed: %v", o.User, err)
		return false
	}
	log.Printf("emailed %s (%d messages)", o.User, len(o.Messages))
	return true
}

// sendWebPush pushes o to c, logs the outcome and reports whether the
// push service accepted it.
func (n *Notifier) sendWebPush(o *Notification, c *WebPushConfig) bool {
	res, body, err := n.SendWebPush(o.Messages, c)
	switch {
	case err != nil:
		log.Printf("web push to %s for %s failed: %v", c.Endpoint, c.User, err)
	case WebPushGone(res.StatusCode):
		log.Printf("web push to %s for %s: %s, deleting the subscription", c.Endpoint, c.User, res.Status)
		n.DeleteWebPushConfig(c)
	case res.StatusCode >= 400:
		log.Printf("web push to %s for %s failed: %s %s", c.Endpoint, c.User, res.Status, strings.TrimSpace(string(body)))
	default:
		log.Printf("web push to %s for %s: %s (%d messages)", c.Endpoint, c.User, res.Status, len(o.Messages))
		return true
	}
	return false
}

func (n *Notifier) DeleteWebPushConfig(c *WebPushConfig) {
	err := errors.New("no database")
	if n.store != nil {
		err = n.store.DeleteWebPush(context.Background(), c.User, c.Endpoint)
	}
	if err != nil {
		log.Printf("deleting web push %s for %s: %v", c.Endpoint, c.User, err)
	}
}
