package router

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"obsfabric/internal/catalog"
)

var auditDropped = promauto.NewCounter(prometheus.CounterOpts{
	Name: "router_audit_dropped_total",
	Help: "Audit rows dropped because the audit queue was full.",
})

// auditor writes query_audit rows in the background so a slow database
// never delays a search response.
type auditor struct {
	cat *catalog.Catalog
	log *slog.Logger
	ch  chan catalog.QueryAudit
}

func newAuditor(cat *catalog.Catalog, log *slog.Logger) *auditor {
	return &auditor{cat: cat, log: log, ch: make(chan catalog.QueryAudit, 1000)}
}

func (a *auditor) add(rec catalog.QueryAudit) {
	select {
	case a.ch <- rec:
	default:
		auditDropped.Inc()
	}
}

func (a *auditor) run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case rec := <-a.ch:
			if err := a.cat.AddQueryAudit(ctx, rec); err != nil {
				a.log.Error("write query audit", "query_id", uuid.UUID(rec.ID).String(), "err", err)
			}
		}
	}
}
