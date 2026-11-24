package main

import (
	"context"
	"fmt"
	"io"
	"log"

	"github.com/lierc/lierc-api/internal/config"
	"github.com/lierc/lierc-api/internal/db"
	"github.com/lierc/lierc-api/internal/logservice"
	"github.com/lierc/lierc/pkg/metrics"
)

// runIngest runs `lierc ingest` until ctx is done and returns the exit
// code; any argument is a usage error.
func runIngest(ctx context.Context, args []string, getenv func(string) string, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: lierc ingest (no arguments)")
		return 2
	}
	logger := log.New(stderr, "", log.LstdFlags)
	metrics.Serve(getenv("METRICS_LISTEN"))

	// the database settings as lierc api reads them; the pool connects
	// when first used, and Start waits for postgres
	dbConf, err := config.LoadDB(getenv)
	if err != nil {
		logger.Printf("config: %v", err)
		return 1
	}
	d, err := db.Open(context.Background(), dbConf)
	if err != nil {
		logger.Printf("database: %v", err)
		return 1
	}
	defer d.Close()

	// lierc-api migrates the schema; this image's ingest writes the
	// migrated one, so it waits for it (the playbook starts both at once)
	if err := d.WaitMigrated(ctx, logger.Printf); err != nil {
		logger.Print("stopped while waiting for the schema")
		return 0
	}

	svc, err := logservice.New(getenv, d, logservice.Options{})
	if err != nil {
		logger.Printf("ingest: %v", err)
		return 1
	}

	if err := svc.Start(ctx); err != nil {
		if ctx.Err() != nil {
			logger.Print("stopped while starting")
			return 0
		}
		logger.Printf("start: %v", err)
		return 1
	}

	fmt.Print("Ready!\n")
	<-ctx.Done()

	logger.Print("stopping")
	svc.Stop()
	return 0
}
