package router

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"obsfabric/internal/catalog"
	"obsfabric/internal/search"
)

func TestParseQuery(t *testing.T) {
	base := "from=2026-11-26T00:00:00Z&to=2026-11-27T00:00:00Z"
	tests := []struct {
		query   string
		wantErr string
	}{
		{base, ""},
		{base + "&q=ipsweep&source=nslkdd&severity=WARN&severity=ERROR&limit=1000", ""},
		{"to=2026-11-27T00:00:00Z", "from is required"},
		{"from=2026-11-26&to=2026-11-27T00:00:00Z", "RFC 3339"},
		{"from=2026-11-27T00:00:00Z&to=2026-11-26T00:00:00Z", "later than from"},
		{base + "&q=" + strings.Repeat("a", 201), "longer than"},
		{base + "&source=syslog", "source"},
		{base + "&q=%22unclosed", "q: "},
		{base + "&limit=0", "limit"},
		{base + "&limit=1001", "limit"},
	}
	for _, tt := range tests {
		v, _ := url.ParseQuery(tt.query)
		q, err := parseQuery(v)
		if tt.wantErr == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tt.query, err)
			} else if q.Limit < 1 {
				t.Errorf("%s: limit = %d", tt.query, q.Limit)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
			t.Errorf("%s: error = %v, want %q", tt.query, err, tt.wantErr)
		}
	}
}

func TestSearchHotAndWarm(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	part := func(d int, tier string) catalog.Partition {
		return catalog.Partition{Start: day(d), End: day(d + 1), Tier: tier, Location: day(d).Format("logs-2006.01.02")}
	}

	fail := false
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		io.Copy(io.Discard, r.Body)
		if fail {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		fmt.Fprint(w, `{"timed_out":false,"hits":{"hits":[
			{"_index":"logs-2026.11.25","_source":{"event_id":"a","event_time":"2026-11-25T10:00:00Z","source":"nslkdd"}},
			{"_index":"logs-2026.11.24","_source":{"event_id":"b","event_time":"2026-11-24T10:00:00Z","source":"nslkdd"}}]}}`)
	}))
	defer srv.Close()

	r := New(Config{HotTimeout: time.Second, MaxColdWindow: time.Hour, CatalogTTL: time.Hour},
		slog.New(slog.DiscardHandler), nil, search.New(srv.URL), nil, nil)
	r.plan.parts = []catalog.Partition{part(20, "COLD"), part(23, "WARM"), part(24, "WARM"), part(25, "HOT"), part(26, "HOT")}
	r.plan.loadedAt = time.Now()
	q := Query{From: day(24), To: day(26), Limit: 10}

	resp, err := r.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/logs-2026.11.24,logs-2026.11.25/_search" {
		t.Errorf("searched %s, want only the partitions inside the range", gotPath)
	}
	if resp.Status != "COMPLETE" || len(resp.Tiers) != 2 || resp.Hits[0].Tier != "HOT" || resp.Hits[1].Tier != "WARM" {
		t.Errorf("response = %+v", resp)
	}

	fail = true
	resp, err = r.Search(context.Background(), q)
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != "FAILED" || resp.Tiers[0].Status != "ERROR" {
		t.Errorf("status = %s, tiers = %+v, want FAILED", resp.Status, resp.Tiers)
	}
}

func TestSearchRejectsWideColdRange(t *testing.T) {
	day := func(d int) time.Time { return time.Date(2026, 11, d, 0, 0, 0, 0, time.UTC) }
	r := New(Config{MaxColdWindow: 48 * time.Hour, CatalogTTL: time.Hour}, slog.New(slog.DiscardHandler), nil, nil, nil, nil)
	for d := 20; d <= 23; d++ {
		r.plan.parts = append(r.plan.parts, catalog.Partition{Start: day(d), End: day(d + 1), Tier: "COLD"})
	}
	r.plan.loadedAt = time.Now()

	// 30 days requested, but only 20-23 Nov (4 days) are COLD.
	if _, err := r.Search(context.Background(), Query{From: day(1), To: day(30), Limit: 10}); !errors.Is(err, errColdWindow) {
		t.Errorf("err = %v, want errColdWindow", err)
	}
}

func TestMerge(t *testing.T) {
	at := func(h int) time.Time { return time.Date(2026, 11, 25, h, 0, 0, 0, time.UTC) }
	hot := []Hit{{EventID: "c", EventTime: at(9), Tier: "HOT"}, {EventID: "a", EventTime: at(5), Tier: "WARM"}}
	cold := []Hit{{EventID: "b", EventTime: at(7), Tier: "COLD"}, {EventID: "a", EventTime: at(5), Tier: "COLD"}, {EventID: "d", EventTime: at(1), Tier: "COLD"}}

	got := merge(3, hot, cold)
	var ids []string
	for _, h := range got {
		ids = append(ids, h.EventID+"/"+h.Tier)
	}
	if want := []string{"c/HOT", "b/COLD", "a/WARM"}; !slices.Equal(ids, want) {
		t.Errorf("merge = %v, want %v", ids, want)
	}
}

func TestColdQuery(t *testing.T) {
	q := Query{
		From: time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC), To: time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC),
		Severities: []string{"WARN", "ERROR"}, Limit: 5,
	}
	q.terms, _ = parseText("Neptune a.b")
	sql, args := coldQuery(q, []string{"s3://b/cold/x/part-0.parquet"})

	for _, want := range []string{"read_parquet(['s3://b/cold/x/part-0.parquet'])", "severity IN (?, ?)", "LIMIT ?"} {
		if !strings.Contains(sql, want) {
			t.Errorf("query is missing %q:\n%s", want, sql)
		}
	}
	if strings.Count(sql, "?") != len(args) {
		t.Fatalf("%d placeholders, %d args", strings.Count(sql, "?"), len(args))
	}
	if args[4] != `\bneptune\b` || args[5] != `\ba\W+b\b` {
		t.Errorf("patterns = %v, %v", args[4], args[5])
	}
}
