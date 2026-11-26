package router

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"obsfabric/internal/catalog"
)

// planner answers "which partitions hold data for this time range" from a
// short-lived copy of the catalog, so each search does not hit PostgreSQL.
type planner struct {
	cat *catalog.Catalog
	ttl time.Duration
	log *slog.Logger

	mu       sync.Mutex
	parts    []catalog.Partition
	loadedAt time.Time
}

// plan returns the partitions that overlap [from, to). If the catalog cannot
// be read, the last copy is used so searches keep working.
func (p *planner) plan(ctx context.Context, from, to time.Time) ([]catalog.Partition, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if time.Since(p.loadedAt) > p.ttl {
		parts, err := p.cat.Partitions(ctx)
		switch {
		case err == nil:
			p.parts, p.loadedAt = parts, time.Now()
		case p.loadedAt.IsZero():
			return nil, err
		default:
			p.log.Warn("catalog unavailable, using cached copy", "age", time.Since(p.loadedAt).String(), "err", err)
		}
	}

	var out []catalog.Partition
	for _, part := range p.parts {
		if part.Start.Before(to) && part.End.After(from) {
			out = append(out, part)
		}
	}
	return out, nil
}
