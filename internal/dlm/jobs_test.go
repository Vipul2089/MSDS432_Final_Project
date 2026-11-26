package dlm

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"obsfabric/internal/catalog"
)

func TestAfterError(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		status    string
		err       error
		want      string
		wantDelay time.Duration
	}{
		{"COPYING", boom, "ROLLING_BACK", 0},
		{"VERIFYING", boom, "ROLLING_BACK", 0},
		{"SWITCHING", boom, "SWITCHING", 4 * time.Minute},
		{"SWITCHING", fmt.Errorf("switch: %w", catalog.ErrVersionConflict), "FAILED", 0},
		{"ROLLING_BACK", boom, "ROLLING_BACK", 4 * time.Minute},
	}
	for _, tt := range tests {
		got, delay := afterError(catalog.Job{Status: tt.status, Attempt: 2}, tt.err)
		if got != tt.want || delay != tt.wantDelay {
			t.Errorf("%s: got %s after %s, want %s after %s", tt.status, got, delay, tt.want, tt.wantDelay)
		}
	}
}

func TestAfterRollback(t *testing.T) {
	if got, delay := afterRollback(catalog.Job{Attempt: 1, MaxAttempts: 3}); got != "PENDING" || delay != 2*time.Minute {
		t.Errorf("attempt 1 of 3: got %s after %s, want PENDING after 2m", got, delay)
	}
	if got, _ := afterRollback(catalog.Job{Attempt: 3, MaxAttempts: 3}); got != "FAILED" {
		t.Errorf("attempt 3 of 3: got %s, want FAILED", got)
	}
}
