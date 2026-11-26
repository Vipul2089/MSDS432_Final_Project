// Command ingest loads log files from the inbox into the HOT tier.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"obsfabric/internal/catalog"
	"obsfabric/internal/config"
	"obsfabric/internal/ingest"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/search"
	"obsfabric/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: config.LogLevel()})).
		With("service", "ingest")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	dsn, err := config.Required("PG_DSN")
	if err != nil {
		return err
	}
	osURL, err := config.Required("OS_URL")
	if err != nil {
		return err
	}
	s3cfg, err := objectstore.ConfigFromEnv()
	if err != nil {
		return err
	}
	workers, err := config.Int("INGEST_WORKERS", runtime.GOMAXPROCS(0))
	if err != nil {
		return err
	}
	bulkWorkers, err := config.Int("INGEST_BULK_WORKERS", 2)
	if err != nil {
		return err
	}

	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	sc := search.New(osURL)
	store := objectstore.New(s3cfg)

	if err := sc.EnsureTemplate(ctx); err != nil {
		return err
	}

	in := ingest.New(ingest.Config{
		Inbox:       config.String("INGEST_INBOX", "/data/inbox"),
		Workers:     workers,
		BulkWorkers: bulkWorkers,
	}, log, catalog.New(db), sc, store)

	srv := server.New(log,
		server.Check{Name: "postgres", Fn: db.Ping},
		server.Check{Name: "opensearch", Fn: sc.Ping},
		server.Check{Name: "s3", Fn: store.Ping},
	)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(ctx, config.String("HTTP_ADDR", ":8080")) })
	g.Go(func() error { return in.Run(ctx) })
	return g.Wait()
}
