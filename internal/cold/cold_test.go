package cold

import (
	"slices"
	"testing"
	"time"
)

func TestFileURIs(t *testing.T) {
	m := Manifest{Files: []File{{Key: "part-0.parquet"}, {Key: "part-1.parquet"}}}
	got := m.FileURIs("s3://obsfabric/cold/bucket=20261122T00Z/job=11/attempt=1/_manifest.json")
	want := []string{
		"s3://obsfabric/cold/bucket=20261122T00Z/job=11/attempt=1/part-0.parquet",
		"s3://obsfabric/cold/bucket=20261122T00Z/job=11/attempt=1/part-1.parquet",
	}
	if !slices.Equal(got, want) {
		t.Errorf("FileURIs = %v, want %v", got, want)
	}
}

func TestPrefix(t *testing.T) {
	start := time.Date(2026, 11, 22, 5, 0, 0, 0, time.UTC)
	if got, want := Prefix(start, 11, 2), "cold/bucket=20261122T05Z/job=11/attempt=2/"; got != want {
		t.Errorf("Prefix = %s, want %s", got, want)
	}
}

func TestQuoteList(t *testing.T) {
	if got, want := QuoteList([]string{"a", "it's"}), "['a', 'it''s']"; got != want {
		t.Errorf("QuoteList = %s, want %s", got, want)
	}
}
