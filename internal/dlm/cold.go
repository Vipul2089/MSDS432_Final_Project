package dlm

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"time"

	duckdb "github.com/duckdb/duckdb-go/v2"

	"obsfabric/internal/catalog"
	"obsfabric/internal/cold"
	"obsfabric/internal/event"
)

const (
	pageSize     = 10_000
	pitKeepAlive = 5 * time.Minute
)

// warmToCold copies a WARM index to Parquet files in object storage and
// switches the catalog only after the copy has been read back and checked
// (LLD-001 §6.3.2).
func (s *Service) warmToCold(ctx context.Context, job catalog.Job) error {
	switch job.Status {
	case "PENDING":
		prefix := cold.Prefix(job.Start, job.ID, job.Attempt+1)
		return s.cat.StartAttempt(ctx, job.ID, "COPYING", s.store.URI(prefix))

	case "COPYING":
		if err := s.copyToCold(ctx, job); err != nil {
			return err
		}
		return s.cat.MoveJob(ctx, job.ID, "VERIFYING", "", time.Now())

	case "VERIFYING":
		if s.cfg.TestHook == HookDeletePart && job.Attempt == 1 {
			if err := s.deleteOnePart(ctx, job); err != nil {
				return err
			}
		}
		if err := s.verifyCold(ctx, job); err != nil {
			return err
		}
		return s.cat.MoveJob(ctx, job.ID, "SWITCHING", "", time.Now())

	case "SWITCHING":
		m, uri, err := s.readManifest(ctx, job)
		if err != nil {
			return err
		}
		var size int64
		for _, f := range m.Files {
			size += f.Bytes
		}
		if err := s.cat.SwitchToCold(ctx, job, uri, job.SrcFingerprint, size); err != nil {
			return err
		}
		s.log.Info("partition moved to COLD", "job_id", job.ID, "partition_id", job.PartitionID,
			"manifest", uri, "rows", m.RowCount, "bytes", size)
		return nil

	case "PURGE_PENDING":
		index := catalog.IndexName(job.Start, job.End.Sub(job.Start))
		if err := s.search.DeleteIndex(ctx, index); err != nil {
			return err
		}
		if err := s.cat.FinishJob(ctx, job.ID); err != nil {
			return err
		}
		jobsTotal.WithLabelValues(job.Type, "done").Inc()
		s.log.Info("WARM index purged", "job_id", job.ID, "partition_id", job.PartitionID, "index", index)
		return nil

	case "ROLLING_BACK":
		if job.StagingURI != "" {
			prefix, err := s.store.Key(job.StagingURI)
			if err != nil {
				return err
			}
			if err := s.store.DeletePrefix(ctx, prefix); err != nil {
				return err
			}
		}
		next, delay := afterRollback(job)
		return s.cat.MoveJob(ctx, job.ID, next, "", time.Now().Add(delay))
	}
	return fmt.Errorf("unexpected status %s", job.Status)
}

// copyToCold starts from a clean prefix and spool every time, so a copy
// interrupted by a crash is simply run again.
func (s *Service) copyToCold(ctx context.Context, job catalog.Job) error {
	prefix, err := s.store.Key(job.StagingURI)
	if err != nil {
		return err
	}
	spool := filepath.Join(s.cfg.SpoolDir, fmt.Sprintf("job-%d", job.ID))
	if err := os.RemoveAll(spool); err != nil {
		return err
	}
	defer os.RemoveAll(spool)
	if err := s.store.DeletePrefix(ctx, prefix); err != nil {
		return fmt.Errorf("clean staging prefix: %w", err)
	}
	if err := os.MkdirAll(spool, 0o755); err != nil {
		return err
	}

	db, err := cold.Open(ctx, filepath.Join(spool, "export.duckdb"), nil, s.cfg.DuckDBMemory)
	if err != nil {
		return err
	}
	defer db.Close()
	if _, err := db.ExecContext(ctx, cold.CreateTable); err != nil {
		return err
	}

	count, fp, err := s.export(ctx, db, job.Location)
	if err != nil {
		return fmt.Errorf("export %s: %w", job.Location, err)
	}

	m := cold.Manifest{
		Version: cold.ManifestVersion, PartitionID: job.PartitionID,
		BucketStart: job.Start, BucketEnd: job.End, JobID: job.ID, Attempt: job.Attempt,
		RowCount: count, Fingerprint: strconv.FormatUint(fp, 10), Files: []cold.File{},
	}
	if count > 0 {
		var minT, maxT time.Time
		if err := db.QueryRowContext(ctx, `SELECT min(event_time), max(event_time) FROM events`).Scan(&minT, &maxT); err != nil {
			return err
		}
		m.MinEventTime, m.MaxEventTime = minT.UTC(), maxT.UTC()
		if m.Files, err = s.writeParquet(ctx, db, filepath.Join(spool, "parquet"), prefix); err != nil {
			return err
		}
	}

	m.CreatedAt = time.Now().UTC()
	body, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := s.store.Put(ctx, prefix+cold.ManifestName, body); err != nil {
		return fmt.Errorf("upload manifest: %w", err)
	}
	return s.cat.SetJobSource(ctx, job.ID, count, m.Fingerprint)
}

// export pages through a point in time of the index and appends every
// document to the events table. It returns the count and fingerprint of
// what it read.
func (s *Service) export(ctx context.Context, db *sql.DB, index string) (count int64, fp uint64, err error) {
	pit, err := s.search.OpenPIT(ctx, index, pitKeepAlive)
	if err != nil {
		return 0, 0, err
	}
	defer s.search.ClosePIT(context.WithoutCancel(ctx), pit)

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Close()

	err = conn.Raw(func(dc any) error {
		app, err := duckdb.NewAppenderFromConn(dc.(driver.Conn), "", "events")
		if err != nil {
			return err
		}
		var after json.RawMessage
		for {
			hits, err := s.search.Page(ctx, pit, pitKeepAlive, after, pageSize)
			if err != nil {
				app.Close()
				return err
			}
			for _, h := range hits {
				var ev event.Event
				if err := json.Unmarshal(h.Source, &ev); err != nil {
					app.Close()
					return err
				}
				hash, err := event.Hash(ev.ID)
				if err != nil {
					app.Close()
					return err
				}
				if err := app.AppendRow(row(ev, hash)...); err != nil {
					app.Close()
					return err
				}
				count++
				fp += hash // wraps at 2^64, which is the modulo we want
			}
			if len(hits) < pageSize {
				return app.Close()
			}
			after = hits[len(hits)-1].Sort
		}
	})
	return count, fp, err
}

func row(ev event.Event, hash uint64) []driver.Value {
	return []driver.Value{
		ev.ID, hash, ev.EventTime, ev.ObservedTime, ev.PartitionID, ev.Source, ev.EventType,
		ev.Category, ev.Severity, nullable(ev.SrcIP), nullable(ev.DstIP), nullable(ev.Protocol),
		nullable(ev.Service), deref(ev.Bytes), deref(ev.DurationS), nullable(ev.Label),
		ev.RawMessage, ev.IsSynthetic, ev.BatchID, ev.SchemaVersion,
	}
}

func nullable(s string) driver.Value {
	if s == "" {
		return nil
	}
	return s
}

func deref[T any](p *T) driver.Value {
	if p == nil {
		return nil
	}
	return *p
}

// writeParquet writes the events table sorted by time into Parquet files of
// up to 256 MB and uploads them under prefix.
func (s *Service) writeParquet(ctx context.Context, db *sql.DB, dir, prefix string) ([]cold.File, error) {
	_, err := db.ExecContext(ctx, `COPY (SELECT * FROM events ORDER BY event_time, event_id) TO `+cold.Quote(dir)+
		` (FORMAT parquet, COMPRESSION zstd, FILE_SIZE_BYTES '256MB', FILENAME_PATTERN 'part-{i}')`)
	if err != nil {
		return nil, fmt.Errorf("write parquet: %w", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var files []cold.File
	for _, e := range entries {
		path := filepath.Join(dir, e.Name())
		f := cold.File{Key: e.Name()}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM read_parquet(`+cold.Quote(path)+`)`).Scan(&f.Rows); err != nil {
			return nil, err
		}
		if f.SHA256, f.Bytes, err = fileDigest(path); err != nil {
			return nil, err
		}
		if err := s.store.PutFile(ctx, prefix+f.Key, path); err != nil {
			return nil, fmt.Errorf("upload %s: %w", f.Key, err)
		}
		files = append(files, f)
	}
	return files, nil
}

func fileDigest(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func (s *Service) readManifest(ctx context.Context, job catalog.Job) (cold.Manifest, string, error) {
	var m cold.Manifest
	prefix, err := s.store.Key(job.StagingURI)
	if err != nil {
		return m, "", err
	}
	data, err := s.store.Get(ctx, prefix+cold.ManifestName)
	if err != nil {
		return m, "", fmt.Errorf("read manifest: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return m, "", fmt.Errorf("decode manifest: %w", err)
	}
	return m, s.store.URI(prefix + cold.ManifestName), nil
}

// verifyCold reads the uploaded files back from object storage and checks
// them against what was read from the WARM index (LLD-001 §6.3.3).
func (s *Service) verifyCold(ctx context.Context, job catalog.Job) error {
	m, uri, err := s.readManifest(ctx, job)
	if err != nil {
		return err
	}
	dir, err := s.store.Key(job.StagingURI)
	if err != nil {
		return err
	}
	for _, f := range m.Files {
		size, err := s.store.Size(ctx, dir+f.Key)
		if err != nil {
			return fmt.Errorf("check %s: %w", f.Key, err)
		}
		if size != f.Bytes {
			return fmt.Errorf("%s is %d bytes, manifest says %d", f.Key, size, f.Bytes)
		}
	}

	n, fp, err := cold.Fingerprint(ctx, s.duck, m.FileURIs(uri))
	if err != nil {
		return fmt.Errorf("read back parquet: %w", err)
	}
	dstFP := strconv.FormatUint(fp, 10)
	if err := s.cat.SetJobTarget(ctx, job.ID, n, dstFP); err != nil {
		return err
	}
	if job.DocCount != job.SrcCount || n != job.SrcCount || m.RowCount != n {
		return fmt.Errorf("row counts differ: sealed %d, read %d, written %d", job.DocCount, job.SrcCount, n)
	}
	if dstFP != job.SrcFingerprint || m.Fingerprint != dstFP {
		return fmt.Errorf("fingerprints differ: read %s, written %s", job.SrcFingerprint, dstFP)
	}
	return nil
}

// HookDeletePart makes the first attempt of every WARM_TO_COLD job lose one
// Parquet file before it is verified, for failure test FT-02.
const HookDeletePart = "delete_part_before_verify"

func (s *Service) deleteOnePart(ctx context.Context, job catalog.Job) error {
	m, _, err := s.readManifest(ctx, job)
	if err != nil || len(m.Files) == 0 {
		return err
	}
	dir, err := s.store.Key(job.StagingURI)
	if err != nil {
		return err
	}
	s.log.Warn("test hook: deleting a COLD file before verification", "job_id", job.ID, "file", m.Files[0].Key)
	return s.store.Delete(ctx, dir+m.Files[0].Key)
}
