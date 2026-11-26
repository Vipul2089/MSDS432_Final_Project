package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestEndpoints(t *testing.T) {
	ok := Check{Name: "ok", Fn: func(context.Context) error { return nil }}
	down := Check{Name: "postgres", Fn: func(context.Context) error { return errors.New("connection refused") }}

	tests := []struct {
		name   string
		checks []Check
		path   string
		want   int
	}{
		{"healthz ignores failing dependencies", []Check{down}, "/healthz", http.StatusOK},
		{"readyz with all checks passing", []Check{ok}, "/readyz", http.StatusOK},
		{"readyz with a failing check", []Check{ok, down}, "/readyz", http.StatusServiceUnavailable},
	}

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(log, tt.checks...)
			rec := httptest.NewRecorder()
			s.mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			if rec.Code != tt.want {
				t.Fatalf("GET %s = %d, want %d (body %q)", tt.path, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}
