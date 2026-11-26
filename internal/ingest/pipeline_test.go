package ingest

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"obsfabric/internal/event"
	"obsfabric/internal/search"
)

func TestFlushRetriesOnlyThrottledDocs(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			fmt.Fprint(w, `{"items":[{"create":{"status":201}},{"create":{"status":429}},`+
				`{"create":{"status":409}},{"create":{"status":400,"error":{"type":"mapper_parsing_exception","reason":"bad"}}}]}`)
			return
		}
		fmt.Fprint(w, `{"items":[{"create":{"status":201}}]}`)
	}))
	defer srv.Close()

	b := &batch{}
	for i := range 4 {
		doc := fmt.Appendf(nil, "{\"create\":{\"_id\":\"%d\"}}\n{}\n", i)
		b.docs = append(b.docs, doc)
		b.recs = append(b.recs, record{offset: int64(i)})
		b.size += len(doc)
	}

	in := &Ingester{search: search.New(srv.URL)}
	st := &fileStats{}
	if err := in.flush(context.Background(), b, st); err != nil {
		t.Fatal(err)
	}

	if len(bodies) != 2 || bodies[1] != string(b.docs[1]) {
		t.Fatalf("retry body = %q, want only doc 1", bodies[1:])
	}
	if st.loaded.Load() != 2 || st.duplicate.Load() != 1 {
		t.Errorf("loaded=%d duplicate=%d, want 2 and 1", st.loaded.Load(), st.duplicate.Load())
	}
	if len(st.rejects) != 1 || st.rejects[0].Reason != event.ReasonMapping || st.rejects[0].Offset != 3 {
		t.Errorf("rejects = %+v, want one MAPPING_ERROR at offset 3", st.rejects)
	}
}

func TestScanRejectsLargeRecords(t *testing.T) {
	input := "a\n\n" + strings.Repeat("x", maxRecordBytes+1) + "\nb\n"
	out := make(chan record, 10)
	st := &fileStats{}
	if err := scan(context.Background(), bytes.NewBufferString(input), out, st); err != nil {
		t.Fatal(err)
	}
	close(out)

	var got []record
	for r := range out {
		got = append(got, r)
	}
	if len(got) != 2 || got[0].offset != 0 || got[1].offset != 3 {
		t.Errorf("records = %+v, want offsets 0 and 3", got)
	}
	if st.read.Load() != 3 || len(st.rejects) != 1 || st.rejects[0].Reason != event.ReasonTooLarge {
		t.Errorf("read=%d rejects=%+v", st.read.Load(), st.rejects)
	}
}
