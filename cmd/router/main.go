// Command router serves the search API across all storage tiers.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"

	"obsfabric/internal/catalog"
	"obsfabric/internal/cold"
	"obsfabric/internal/config"
	"obsfabric/internal/objectstore"
	"obsfabric/internal/router"
	"obsfabric/internal/search"
	"obsfabric/internal/server"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: config.LogLevel()})).
		With("service", "router")

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
	hotTimeout, err := config.Duration("ROUTER_HOT_TIMEOUT", 5*time.Second)
	if err != nil {
		return err
	}
	coldTimeout, err := config.Duration("ROUTER_COLD_TIMEOUT", 20*time.Second)
	if err != nil {
		return err
	}
	maxColdWindow, err := config.Duration("ROUTER_MAX_COLD_WINDOW", 168*time.Hour)
	if err != nil {
		return err
	}
	coldConcurrency, err := config.Int("ROUTER_COLD_CONCURRENCY", 4)
	if err != nil {
		return err
	}
	catalogTTL, err := config.Duration("ROUTER_CATALOG_CACHE_TTL", 5*time.Second)
	if err != nil {
		return err
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
	duck, err := cold.Open(ctx, "", &s3cfg, config.String("ROUTER_DUCKDB_MEMORY", "512MB"))
	if err != nil {
		return err
	}
	defer duck.Close()
	duck.SetMaxOpenConns(coldConcurrency)

	rt := router.New(router.Config{
		HotTimeout:    hotTimeout,
		ColdTimeout:   coldTimeout,
		MaxColdWindow: maxColdWindow,
		CatalogTTL:    catalogTTL,
	}, log, catalog.New(db), sc, store, duck)
	srv := server.New(log,
		server.Check{Name: "postgres", Fn: db.Ping},
		server.Check{Name: "opensearch", Fn: sc.Ping},
		server.Check{Name: "s3", Fn: store.Ping},
	)
	srv.Handle("GET /api/v1/search", rt)

	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return srv.Run(ctx, config.String("HTTP_ADDR", ":8080")) })
	g.Go(func() error { return rt.Run(ctx) })
	return g.Wait()
}
