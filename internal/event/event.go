// Package event defines the unified event schema shared by every source.
package event

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"
	"time"
)

const SchemaVersion = "1"

// Event is one log record in the unified schema (LLD-001 §3.1). It is stored
// as a document in OpenSearch and as a row in COLD Parquet files.
type Event struct {
	ID            string    `json:"event_id"`
	EventTime     time.Time `json:"event_time"`
	ObservedTime  time.Time `json:"observed_time"`
	PartitionID   int64     `json:"partition_id"`
	Source        string    `json:"source"`
	EventType     string    `json:"event_type"`
	Category      string    `json:"category"`
	Severity      string    `json:"severity"`
	SrcIP         string    `json:"src_ip,omitempty"`
	DstIP         string    `json:"dst_ip,omitempty"`
	Protocol      string    `json:"protocol,omitempty"`
	Service       string    `json:"service,omitempty"`
	Bytes         *int64    `json:"bytes,omitempty"`
	DurationS     *float64  `json:"duration_s,omitempty"`
	Label         string    `json:"label,omitempty"`
	RawMessage    string    `json:"raw_message"`
	IsSynthetic   bool      `json:"is_synthetic"`
	BatchID       int64     `json:"ingest_batch_id"`
	SchemaVersion string    `json:"schema_version"`
}

// ID returns the event ID for the record at offset in the file whose SHA-256
// is origin. The same record always gets the same ID, so loading a file twice
// stores each event once (ADR-0011).
func ID(source, origin string, offset int64) string {
	h := sha256.New()
	h.Write([]byte(source))
	h.Write([]byte{0x1f})
	h.Write([]byte(origin))
	h.Write([]byte{0x1f})
	h.Write([]byte(strconv.FormatInt(offset, 10)))
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Hash returns the event hash used for COLD fingerprints: the first 8 bytes
// of the ID digest as a big-endian integer (LLD-001 §3.3).
func Hash(id string) (uint64, error) {
	if len(id) < 16 {
		return 0, fmt.Errorf("event ID %q is too short", id)
	}
	return strconv.ParseUint(id[:16], 16, 64)
}

// Rejection reasons stored in rejected_records.reason.
const (
	ReasonParse        = "PARSE_ERROR"
	ReasonTooLarge     = "RECORD_TOO_LARGE"
	ReasonMissingField = "MISSING_FIELD"
	ReasonBadTimestamp = "INVALID_TIMESTAMP"
	ReasonFuture       = "FUTURE_EVENT"
	ReasonExpired      = "EXPIRED_EVENT"
	ReasonLate         = "LATE_EVENT"
	ReasonMapping      = "MAPPING_ERROR"
	ReasonIndexError   = "INDEX_ERROR"
)

// RejectError explains why a record cannot be loaded.
type RejectError struct {
	Reason string
	Detail string
}

func (e *RejectError) Error() string { return e.Reason + ": " + e.Detail }
