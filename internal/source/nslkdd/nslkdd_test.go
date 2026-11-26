package nslkdd

import (
	"errors"
	"strings"
	"testing"

	"obsfabric/internal/event"
)

// row builds a loadgen line with the given label and byte counts.
func row(ts, src, dst, srcBytes, dstBytes, label string) string {
	cols := make([]string, dataColumns)
	for i := range cols {
		cols[i] = "0"
	}
	cols[colDuration] = "2"
	cols[colProtocol] = "TCP"
	cols[colService] = "http"
	cols[colSrcBytes] = srcBytes
	cols[colDstBytes] = dstBytes
	cols[colLabel] = label
	cols[42] = "21"
	return ts + "," + src + "," + dst + "," + strings.Join(cols, ",")
}

func TestParse(t *testing.T) {
	const ts = "2026-11-09T10:15:00.000Z"

	e, err := Parse(row(ts, "10.4.2.7", "172.16.0.9", "100", "50", "neptune"))
	if err != nil {
		t.Fatal(err)
	}
	if e.Category != "DoS" || e.Severity != "ERROR" || e.Protocol != "tcp" || *e.Bytes != 150 || *e.DurationS != 2 {
		t.Errorf("unexpected mapping: %+v", e)
	}
	if !e.IsSynthetic || e.SrcIP != "10.4.2.7" || strings.HasPrefix(e.RawMessage, ts) {
		t.Errorf("unexpected fields: %+v", e)
	}

	e, err = Parse(row(ts, "not-an-ip", "172.16.0.9", "-5", "50", "mystery"))
	if err != nil {
		t.Fatal(err)
	}
	if e.SrcIP != "" || e.Bytes != nil || e.Category != "normal" || e.Severity != "INFO" {
		t.Errorf("bad values should be dropped and unknown labels treated as normal: %+v", e)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name string
		line string
		want string
	}{
		{"too few columns", "2026-11-09T10:15:00Z,1.1.1.1,2.2.2.2,0,tcp", event.ReasonParse},
		{"missing time", row("", "10.0.0.1", "172.16.0.1", "1", "1", "normal"), event.ReasonMissingField},
		{"bad time", row("2026-13-45T10:00:00Z", "10.0.0.1", "172.16.0.1", "1", "1", "normal"), event.ReasonBadTimestamp},
		{"missing label", row("2026-11-09T10:15:00Z", "10.0.0.1", "172.16.0.1", "1", "1", ""), event.ReasonMissingField},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.line)
			var rej *event.RejectError
			if !errors.As(err, &rej) || rej.Reason != tt.want {
				t.Fatalf("got %v, want reason %s", err, tt.want)
			}
		})
	}
}
