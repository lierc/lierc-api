package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/logger"
	"github.com/lierc/lierc/pkg/metrics"
)

func main() {
	// Production's secrets are files, named by NAME_FILE.
	getenv, err := config.WithFiles(os.Environ(), os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	metrics.Serve(getenv("METRICS_LISTEN"))

	// the database settings as lierc-api reads them; the pool connects
	// when first used, and Start waits for postgres
	dbConf, err := config.LoadDB(getenv)
	if err != nil {
		log.Fatal(err)
	}
	d, err := db.Open(context.Background(), dbConf)
	if err != nil {
		log.Fatal(err)
	}
	defer d.Close()

	l, err := logger.New(&logger.Config{NSQDHost: getenv("NSQD_HOST")}, d)
	if err != nil {
		log.Fatal(err)
	}

	// Only a non-nil notifier is set: a nil *notify.Notifier would make a
	// non-nil logger.Notifier.
	if n := newNotifier(getenv, d); n != nil {
		go n.Accumulate()
		l.SetNotifier(n)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := l.Start(ctx); err != nil {
		if ctx.Err() != nil {
			log.Print("stopped while starting")
			return
		}
		log.Fatal(err)
	}

	fmt.Print("Ready!\n")
	<-ctx.Done()

	log.Print("stopping")
	l.Stop()
}
