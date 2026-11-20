package main

import (
	"log"

	"github.com/lierc/lierc-api/internal/notify"
)

// newNotifier builds the notifier from the environment, sharing store. A
// setting that can't be parsed turns notifications off (nil) rather than
// stopping the logger.
func newNotifier(getenv func(string) string, store notify.Store) *notify.Notifier {
	conf, err := notify.ConfigFromEnv(getenv)
	if err != nil {
		log.Printf("notifications off: %v", err)
		return nil
	}
	return notify.New(conf, store)
}
