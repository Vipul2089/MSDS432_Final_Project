// Command dlm moves partitions between the HOT, WARM and COLD tiers as they age.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"obsfabric/internal/cold"
	"obsfabric/internal/config"
	"obsfabric/internal/dlm"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/search"
	"obsfabric/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: config.LogLevel()})).
		With("service", "dlm")

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
	planInterval, err := config.Duration("DLM_PLAN_INTERVAL", 30*time.Second)
	if err != nil {
		return err
	}
	runInterval, err := config.Duration("DLM_RUN_INTERVAL", 5*time.Second)
	if err != nil {
		return err
	}

	hook := config.String("DLM_TEST_HOOK", "")
	if hook != "" && hook != dlm.HookDeletePart {
		return fmt.Errorf("DLM_TEST_HOOK %q is not supported", hook)
	}
	if hook != "" {
		log.Warn("test hook enabled", "hook", hook)
	}
	s3cfg, err := objectstore.ConfigFromEnv()
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
	duckMemory := config.String("DLM_DUCKDB_MEMORY", "256MB")
	duck, err := cold.Open(ctx, "", &s3cfg, duckMemory)
	if err != nil {
		return err
	}
	defer duck.Close()

	svc := dlm.New(dlm.Config{
		PlanInterval: planInterval,
		RunInterval:  runInterval,
		SpoolDir:     config.String("DLM_SPOOL_DIR", "/spool"),
		TestHook:     hook,
		DuckDBMemory: duckMemory,
	}, log, db, sc, store, duck)
	srv := server.New(log,
		server.Check{Name: "postgres", Fn: db.Ping},
		server.Check{Name: "opensearch", Fn: sc.Ping},
		server.Check{Name: "s3", Fn: store.Ping},
		server.Check{Name: "lock", Fn: svc.Ready},
	)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(ctx, config.String("HTTP_ADDR", ":8080")) })
	g.Go(func() error { return svc.Run(ctx) })
	return g.Wait()
}
