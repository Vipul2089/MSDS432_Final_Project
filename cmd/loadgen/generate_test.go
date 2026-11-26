package main

import (
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRandomAddrStaysInPrefix(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	for _, p := range []netip.Prefix{srcNet, dstNet} {
		for range 1000 {
			a := netip.MustParseAddr(randomAddr(rng, p))
			if !p.Contains(a) {
				t.Fatalf("%s is outside %s", a, p)
			}
		}
	}
}

func TestRunSpreadsEventsOverWindow(t *testing.T) {
	dir := t.TempDir()
	row := "0,tcp,private,S0,0,0" + strings.Repeat(",0", 36) + ",neptune,19"
	end := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	g := generator{rows: []string{row}, outDir: dir, end: end, window: time.Hour, fileBytes: 4 << 10, seed: 1}

	files, events, err := g.run(20 << 10)
	if err != nil {
		t.Fatal(err)
	}
	if files < 2 {
		t.Fatalf("got %d files, want rotation into several files", files)
	}

	paths, _ := filepath.Glob(filepath.Join(dir, "*.csv"))
	if len(paths) != files {
		t.Fatalf("found %d .csv files, want %d (no .tmp left behind)", len(paths), files)
	}

	var times []time.Time
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
			ts, err := time.Parse(time.RFC3339Nano, strings.SplitN(line, ",", 2)[0])
			if err != nil {
				t.Fatal(err)
			}
			times = append(times, ts)
		}
	}
	if len(times) != events {
		t.Fatalf("read %d events, run reported %d", len(times), events)
	}
	if first := times[0]; !first.Equal(end.Add(-time.Hour)) {
		t.Errorf("first event at %s, want window start %s", first, end.Add(-time.Hour))
	}
	if last := times[len(times)-1]; !last.Before(end) {
		t.Errorf("last event at %s, want before %s", last, end)
	}
}
