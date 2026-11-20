package notify

import (
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc-api/internal/mail"
	"golang.org/x/time/rate"
)

// QueueSize is how many highlights can wait for Accumulate. Notify never
// waits for room, so a slow /stats or database can't hold up logging; a
// highlight that finds the queue full is dropped.
const QueueSize = 500

// New returns a notifier that uses store, the logger's database. Run
// Accumulate to process what Notify queues.
func New(conf *NotifierConfig, store Store) *Notifier {
	return &Notifier{
		Client:        &http.Client{},
		Notifications: make(map[string]*Notification),
		Last:          make(map[string]time.Time),
		Mu:            &sync.Mutex{},
		Limiter:       rate.NewLimiter(1, 5),
		Config:        conf,
		Queue:         make(chan *logger.LoggedMessage, QueueSize),
		store:         store,
		accumulating:  make(chan struct{}),
	}
}

// Notify queues a highlight or private message from the logger, without
// waiting. A message without a sender or text is dropped, since email and
// web push need both. e is queued as it is; the logger doesn't change it
// after handing it over.
func (n *Notifier) Notify(e *logger.LoggedMessage) {
	if msg := e.Message; msg == nil || msg.Prefix == nil || len(msg.Params) < 2 {
		log.Printf("not notifying message %d on %s: no sender or text", e.MessageId, e.ConnectionId)
		return
	}

	select {
	case n.Queue <- e:
	default:
		log.Printf("not notifying message %d on %s: notifier queue full", e.MessageId, e.ConnectionId)
	}
}

// ConfigFromEnv reads the notifier's settings. An error names the setting
// that couldn't be parsed; empty settings get their defaults.
func ConfigFromEnv(getenv func(string) string) (*NotifierConfig, error) {
	c := &NotifierConfig{
		APIStats:        getenv("API_STATS"),
		APIURL:          getenv("API_URL"),
		VAPIDPrivateKey: getenv("VAPID_PRIVATE"),
		VAPIDPublicKey:  getenv("VAPID_PUBLIC"),
		Delay:           15 * time.Second,
	}
	if c.UnsubscribeSecret = getenv("UNSUBSCRIBE_SECRET"); c.UnsubscribeSecret == "" {
		c.UnsubscribeSecret = "changeme"
	}
	smtp, err := mail.ParseOpts(getenv("SMTP_OPTS"))
	if err != nil {
		return nil, fmt.Errorf("SMTP_OPTS: %w", err)
	}
	c.SMTP = smtp
	if err := c.SetUserRateLimit(getenv("USER_RATE_LIMIT")); err != nil {
		return nil, fmt.Errorf("USER_RATE_LIMIT: %w", err)
	}
	return c, nil
}
