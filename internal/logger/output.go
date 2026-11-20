package logger

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"sync/atomic"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/lierc/lierc-api/internal/db"
)

// loggedNotifier is the part of *db.DB PGOutput uses.
type loggedNotifier interface {
	NotifyLogged(ctx context.Context, payload []byte) error
	NotifyStatus(ctx context.Context, payload []byte) error
}

// Payload sizes, by kind: "stored" (a message with an id) or "passed"
// (one only passed on, which is never stored, so this is the only record
// of its size). Served on METRICS_LISTEN (pkg/metrics).
// logger_notify_payload_bytes is every payload's size, refused ones
// included (they fall in +Inf); logger_notify_payload_max_bytes the
// largest since the logger started; logger_notify_too_long_total the
// payloads NOTIFY couldn't take, so their messages never went out live.
var (
	payloadBuckets = []float64{256, 512, 1024, 2048, 4096, 6144, db.MaxNotifyPayload}
	payloadBytes   = map[string]*vm.PrometheusHistogram{}
	payloadMax     = map[string]*atomic.Int64{}
	tooLongTotal   = map[string]*vm.Counter{}
)

func init() {
	for _, kind := range []string{"stored", "passed"} {
		payloadBytes[kind] = vm.NewPrometheusHistogramExt(`logger_notify_payload_bytes{kind="`+kind+`"}`, payloadBuckets)
		max := &atomic.Int64{}
		payloadMax[kind] = max
		vm.NewGauge(`logger_notify_payload_max_bytes{kind="`+kind+`"}`, func() float64 { return float64(max.Load()) })
		tooLongTotal[kind] = vm.NewCounter(`logger_notify_too_long_total{kind="` + kind + `"}`)
	}
}

// payloadKind is e's label: "stored" when it has an id.
func payloadKind(e *LoggedMessage) string {
	if e.MessageId != 0 {
		return "stored"
	}
	return "passed"
}

// countPayload records a payload of n bytes for e.
func countPayload(e *LoggedMessage, n int) {
	kind := payloadKind(e)
	payloadBytes[kind].Update(float64(n))
	for max := payloadMax[kind]; ; {
		old := max.Load()
		if int64(n) <= old || max.CompareAndSwap(old, int64(n)) {
			break
		}
	}
}

// countTooLong counts a payload NOTIFY refused for its size.
func countTooLong(e *LoggedMessage) {
	tooLongTotal[payloadKind(e)].Inc()
}

// notifyTimeout bounds one NOTIFY, so a slow database can't hold up the
// handler for long.
const notifyTimeout = 5 * time.Second

// PGOutput sends each message, as JSON, as a Postgres NOTIFY on the
// channel "logged", and each status change on "status", which the API's
// stream server listens on (db.ListenStream). Each goes to whoever is
// listening when it's sent.
type PGOutput struct {
	DB loggedNotifier
}

// Logged sends e; failures, a message too long for NOTIFY among them, are
// logged.
func (o PGOutput) Logged(e *LoggedMessage) {
	body, err := json.Marshal(e)
	if err != nil {
		log.Printf("encoding logged message: %v", err)
		return
	}
	countPayload(e, len(body))
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := o.DB.NotifyLogged(ctx, body); err != nil {
		if errors.Is(err, db.ErrPayloadTooLong) {
			countTooLong(e)
		}
		log.Printf("notifying logged (message %d on %s): %v", e.MessageId, e.ConnectionId, err)
	}
}

// Status sends s; failures are logged.
func (o PGOutput) Status(s *StatusChange) {
	body, err := json.Marshal(s)
	if err != nil {
		log.Printf("encoding status change: %v", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), notifyTimeout)
	defer cancel()
	if err := o.DB.NotifyStatus(ctx, body); err != nil {
		log.Printf("notifying status of %s: %v", s.ConnectionId, err)
	}
}
