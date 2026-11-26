package dlm

import (
	"testing"
	"time"
)

func TestParseHourly(t *testing.T) {
	data := []byte(`{"aggregations":{"hours":{"buckets":[
	  {"key":1795658400000,"doc_count":7,"source":{"buckets":[
	    {"key":"nslkdd","doc_count":7,"category":{"buckets":[
	      {"key":"DoS","doc_count":5,"bytes":{"value":1200.0},"errors":{"doc_count":5}},
	      {"key":"normal","doc_count":2,"bytes":{"value":0.0},"errors":{"doc_count":0}}]}}]}},
	  {"key":1795662000000,"doc_count":1,"source":{"buckets":[
	    {"key":"nslkdd","doc_count":1,"category":{"buckets":[
	      {"key":"Probe","doc_count":1,"bytes":{"value":42.0},"errors":{"doc_count":0}}]}}]}}]}}}`)

	got, err := parseHourly(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d rows, want 3: %+v", len(got), got)
	}
	first, last := got[0], got[2]
	if !first.Hour.Equal(time.Date(2026, 11, 26, 2, 0, 0, 0, time.UTC)) || first.Category != "DoS" ||
		first.Events != 5 || first.Errors != 5 || first.Bytes != 1200 {
		t.Errorf("first row = %+v", first)
	}
	if !last.Hour.Equal(time.Date(2026, 11, 26, 3, 0, 0, 0, time.UTC)) || last.Category != "Probe" || last.Bytes != 42 {
		t.Errorf("last row = %+v", last)
	}
}
