package notify

import (
	"time"

	"github.com/lierc/lierc-api/internal/logger"
)

type NotificationPref struct {
	EmailEnabled   bool
	EmailAddress   string
	WebPushConfigs []*WebPushConfig
}

type Notification struct {
	Messages []*logger.LoggedMessage
	Timer    *time.Timer
	User     string
	Pref     *NotificationPref
}
