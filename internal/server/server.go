// Package server provides the HTTP endpoints every service exposes:
// /healthz, /readyz and /metrics.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Check reports whether a dependency is reachable. /readyz runs every check.
type Check struct {
	Name string
	Fn   func(context.Context) error
}

type Server struct {
	mux    *http.ServeMux
	log    *slog.Logger
	checks []Check
}

func New(log *slog.Logger, checks ...Check) *Server {
	s := &Server{mux: http.NewServeMux(), log: log, checks: checks}
	s.mux.HandleFunc("GET /healthz", s.healthz)
	s.mux.HandleFunc("GET /readyz", s.readyz)
	s.mux.Handle("GET /metrics", promhttp.Handler())
	return s
}

// Handle registers an application route next to the standard endpoints.
func (s *Server) Handle(pattern string, h http.Handler) {
	s.mux.Handle(pattern, h)
}

// Run serves until ctx is cancelled, then shuts down gracefully.
func (s *Server) Run(ctx context.Context, addr string) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           s.mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.log.Info("http server started", "addr", addr)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// healthz is the liveness probe. It must not check dependencies (ADR-0012).
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Write([]byte("ok\n"))
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	failed := map[string]string{}
	for _, c := range s.checks {
		if err := c.Fn(ctx); err != nil {
			failed[c.Name] = err.Error()
		}
	}

	w.Header().Set("Content-Type", "application/json")
	if len(failed) > 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		json.NewEncoder(w).Encode(map[string]any{"ready": false, "failed": failed})
		return
	}
	json.NewEncoder(w).Encode(map[string]any{"ready": true})
}
