package catalog

import (
	"testing"
	"time"
)

func TestBucketAndIndexName(t *testing.T) {
	ts := time.Date(2026, 11, 9, 10, 15, 30, 0, time.FixedZone("IST", 5*3600+1800))

	tests := []struct {
		size      time.Duration
		wantStart time.Time
		wantIndex string
	}{
		{24 * time.Hour, time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC), "logs-2026.11.09"},
		{time.Hour, time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC), "logs-2026.11.09.04"},
	}
	for _, tt := range tests {
		start := BucketStart(ts, tt.size)
		if !start.Equal(tt.wantStart) {
			t.Errorf("BucketStart(%s, %s) = %s, want %s", ts, tt.size, start, tt.wantStart)
		}
		if got := IndexName(start, tt.size); got != tt.wantIndex {
			t.Errorf("IndexName = %s, want %s", got, tt.wantIndex)
		}
	}
}
