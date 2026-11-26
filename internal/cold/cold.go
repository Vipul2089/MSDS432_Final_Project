// Package cold holds what dlm and router share about the COLD tier: the
// object layout, the manifest, and DuckDB set up to read object storage.
package cold

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"
	"time"

	_ "github.com/duckdb/duckdb-go/v2"

	"obsfabric/internal/objectstore"
)

const (
	ManifestName    = "_manifest.json"
	ManifestVersion = 1
)

// Prefix is the object prefix for one attempt of a WARM to COLD job
// (LLD-001 §4.4). Each attempt writes to its own prefix, so a failed attempt
// never mixes files with a later one.
func Prefix(bucketStart time.Time, jobID int64, attempt int) string {
	return fmt.Sprintf("%sjob=%d/attempt=%d/", BucketPrefix(bucketStart), jobID, attempt)
}

// BucketPrefix holds every COLD object of one partition, from all jobs and
// attempts.
func BucketPrefix(bucketStart time.Time) string {
	return "cold/bucket=" + bucketStart.UTC().Format("20060102T15Z") + "/"
}

type File struct {
	Key    string `json:"key"` // relative to the manifest
	Rows   int64  `json:"rows"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

// Manifest lists the Parquet files of a COLD partition. It is written after
// all the files, so a manifest that exists always describes complete data
// (ADR-0006).
type Manifest struct {
	Version      int       `json:"manifest_version"`
	PartitionID  int64     `json:"partition_id"`
	BucketStart  time.Time `json:"bucket_start"`
	BucketEnd    time.Time `json:"bucket_end"`
	JobID        int64     `json:"job_id"`
	Attempt      int       `json:"attempt"`
	RowCount     int64     `json:"row_count"`
	Fingerprint  string    `json:"fingerprint"` // decimal; uint64 does not fit a JSON number safely
	MinEventTime time.Time `json:"min_event_time"`
	MaxEventTime time.Time `json:"max_event_time"`
	Files        []File    `json:"files"`
	CreatedAt    time.Time `json:"created_at"`
}

// FileURIs returns the s3:// URI of every file, given the manifest's URI.
func (m Manifest) FileURIs(manifestURI string) []string {
	// Not path.Dir: it would turn "s3://" into "s3:/".
	dir := manifestURI[:strings.LastIndex(manifestURI, "/")+1]
	uris := make([]string, len(m.Files))
	for i, f := range m.Files {
		uris[i] = dir + f.Key
	}
	return uris
}

// Open opens a DuckDB database (in memory if file is empty). If s3 is not
// nil, the database can also read s3:// URIs from that store.
func Open(ctx context.Context, file string, s3 *objectstore.Config, memoryLimit string) (*sql.DB, error) {
	db, err := sql.Open("duckdb", file)
	if err != nil {
		return nil, err
	}
	stmts := []string{
		"SET memory_limit = " + Quote(memoryLimit),
		"SET threads = 2",
	}
	if s3 != nil {
		secret, err := s3Secret(*s3)
		if err != nil {
			db.Close()
			return nil, err
		}
		stmts = append(stmts, "INSTALL httpfs", "LOAD httpfs", secret)
	}
	for _, q := range stmts {
		if _, err := db.ExecContext(ctx, q); err != nil {
			db.Close()
			return nil, fmt.Errorf("duckdb %q: %w", strings.SplitN(q, " (", 2)[0], err)
		}
	}
	return db, nil
}

func s3Secret(cfg objectstore.Config) (string, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil {
		return "", fmt.Errorf("S3 endpoint: %w", err)
	}
	style := "vhost"
	if cfg.PathStyle {
		style = "path"
	}
	return fmt.Sprintf(
		"CREATE OR REPLACE SECRET obs_s3 (TYPE s3, KEY_ID %s, SECRET %s, REGION %s, ENDPOINT %s, URL_STYLE %s, USE_SSL %t)",
		Quote(cfg.AccessKey), Quote(cfg.SecretKey), Quote(cfg.Region), Quote(u.Host), Quote(style), u.Scheme == "https",
	), nil
}

// Quote returns s as a SQL string literal.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

// QuoteList returns a SQL list of string literals, e.g. ['a', 'b'].
func QuoteList(items []string) string {
	q := make([]string, len(items))
	for i, s := range items {
		q[i] = Quote(s)
	}
	return "[" + strings.Join(q, ", ") + "]"
}

// Fingerprint counts the rows in the Parquet files and sums their event
// hashes modulo 2^64 (LLD-001 §6.3.3).
func Fingerprint(ctx context.Context, db *sql.DB, uris []string) (count int64, fp uint64, err error) {
	if len(uris) == 0 {
		return 0, 0, nil
	}
	err = db.QueryRowContext(ctx,
		`SELECT count(*), coalesce(sum(event_hash) % 18446744073709551616, 0)::UBIGINT
		   FROM read_parquet(`+QuoteList(uris)+`)`).Scan(&count, &fp)
	return count, fp, err
}

// CreateTable is the COLD schema (LLD-001 §3.1). dlm loads events into this
// table and writes it out as Parquet.
const CreateTable = `CREATE TABLE events (
	event_id        VARCHAR     NOT NULL,
	event_hash      UBIGINT     NOT NULL,
	event_time      TIMESTAMPTZ NOT NULL,
	observed_time   TIMESTAMPTZ NOT NULL,
	partition_id    BIGINT      NOT NULL,
	source          VARCHAR     NOT NULL,
	event_type      VARCHAR     NOT NULL,
	category        VARCHAR     NOT NULL,
	severity        VARCHAR     NOT NULL,
	src_ip          VARCHAR,
	dst_ip          VARCHAR,
	protocol        VARCHAR,
	service         VARCHAR,
	bytes           BIGINT,
	duration_s      DOUBLE,
	label           VARCHAR,
	raw_message     VARCHAR     NOT NULL,
	is_synthetic    BOOLEAN     NOT NULL,
	ingest_batch_id BIGINT      NOT NULL,
	schema_version  VARCHAR     NOT NULL
)`
