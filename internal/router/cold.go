package router

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"obsfabric/internal/catalog"
	"obsfabric/internal/cold"
)

// searchCold runs one DuckDB query over the Parquet files of all COLD
// partitions in the plan.
func (r *Router) searchCold(ctx context.Context, q Query, parts []catalog.Partition) ([]Hit, error) {
	var uris []string
	for _, p := range parts {
		m, err := r.manifest(ctx, p.Location)
		if err != nil {
			return nil, fmt.Errorf("partition %d: %w", p.ID, err)
		}
		uris = append(uris, m.FileURIs(p.Location)...)
	}
	if len(uris) == 0 {
		return nil, nil
	}

	query, args := coldQuery(q, uris)
	rows, err := r.duck.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var hits []Hit
	for rows.Next() {
		h := Hit{Tier: "COLD"}
		if err := rows.Scan(&h.EventID, &h.EventTime, &h.Source, &h.Severity, &h.RawMessage); err != nil {
			return nil, err
		}
		h.EventTime = h.EventTime.UTC()
		hits = append(hits, h)
	}
	return hits, rows.Err()
}

// coldQuery builds the COLD search (LLD-001 §6.4.2). Every value from the
// request is a bound parameter. The file list cannot be one, so it is built
// from manifest entries that manifest() has already checked.
func coldQuery(q Query, uris []string) (string, []any) {
	var b strings.Builder
	b.WriteString(`SELECT event_id, event_time, source, severity, raw_message FROM read_parquet(`)
	b.WriteString(cold.QuoteList(uris))
	b.WriteString(`) WHERE event_time >= ? AND event_time < ?`)
	args := []any{q.From, q.To}

	if q.EventID != "" {
		b.WriteString(` AND event_id = ?`)
		args = append(args, q.EventID)
	}
	in := func(col string, vals []string) {
		if len(vals) == 0 {
			return
		}
		b.WriteString(` AND ` + col + ` IN (?` + strings.Repeat(`, ?`, len(vals)-1) + `)`)
		for _, v := range vals {
			args = append(args, v)
		}
	}
	in("source", q.Sources)
	in("severity", q.Severities)

	for _, c := range q.terms {
		if c.negate {
			b.WriteString(` AND NOT regexp_matches(lower(raw_message), ?)`)
		} else {
			b.WriteString(` AND regexp_matches(lower(raw_message), ?)`)
		}
		args = append(args, c.pattern())
	}

	b.WriteString(` ORDER BY event_time DESC, event_id LIMIT ?`)
	args = append(args, q.Limit)
	return b.String(), args
}

// manifest returns the manifest at uri. Only manifests under cold/ in our
// bucket are read, and their file names must be plain Parquet names, so a
// query can never read files outside the partition's prefix.
func (r *Router) manifest(ctx context.Context, uri string) (cold.Manifest, error) {
	r.mu.Lock()
	m, ok := r.manifests[uri]
	r.mu.Unlock()
	if ok {
		return m, nil
	}

	key, err := r.store.Key(uri)
	if err != nil {
		return m, err
	}
	if !strings.HasPrefix(key, "cold/") || !strings.HasSuffix(key, "/"+cold.ManifestName) {
		return m, fmt.Errorf("%s is not a COLD manifest", uri)
	}
	data, err := r.store.Get(ctx, key)
	if err != nil {
		return m, fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, fmt.Errorf("decode manifest: %w", err)
	}
	for _, f := range m.Files {
		if strings.ContainsAny(f.Key, `/\'`) || !strings.HasSuffix(f.Key, ".parquet") {
			return m, fmt.Errorf("manifest %s lists an invalid file %q", uri, f.Key)
		}
	}

	r.mu.Lock()
	r.manifests[uri] = m
	r.mu.Unlock()
	return m, nil
}
