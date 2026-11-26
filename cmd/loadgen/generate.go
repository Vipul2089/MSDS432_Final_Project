package main

import (
	"bufio"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const nslkddColumns = 43

func readRows(paths []string) ([]string, error) {
	var rows []string
	for _, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return nil, err
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if strings.Count(line, ",") != nslkddColumns-1 {
				f.Close()
				return nil, fmt.Errorf("%s: line %d does not have %d columns", p, len(rows)+1, nslkddColumns)
			}
			rows = append(rows, line)
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no rows in %v", paths)
	}
	return rows, nil
}

type generator struct {
	rows      []string
	outDir    string
	end       time.Time
	window    time.Duration
	fileBytes int64
	seed      uint64
}

// run writes about targetBytes of events and returns the number of files and
// events written.
func (g *generator) run(targetBytes int64) (files, events int, err error) {
	rng := rand.New(rand.NewPCG(g.seed, 0))

	// Estimate how many events fit in targetBytes so their times can be spaced
	// evenly across the window. The prefix adds about 50 bytes to each row.
	var total int
	for _, r := range g.rows {
		total += len(r) + 50
	}
	n := max(1, int(targetBytes/int64(total/len(g.rows))))
	step := g.window / time.Duration(n)
	start := g.end.Add(-g.window)

	w := &rotatingWriter{dir: g.outDir, limit: g.fileBytes, prefix: "nslkdd-" + g.end.Format("20060102T150405")}
	defer w.abort()

	order := rng.Perm(len(g.rows))
	for i := range n {
		if i > 0 && i%len(order) == 0 {
			rng.Shuffle(len(order), func(a, b int) { order[a], order[b] = order[b], order[a] })
		}
		row := g.rows[order[i%len(order)]]
		ts := start.Add(time.Duration(i) * step).Format("2006-01-02T15:04:05.000Z07:00")
		line := ts + "," + randomAddr(rng, srcNet) + "," + randomAddr(rng, dstNet) + "," + row + "\n"
		if err := w.write(line); err != nil {
			return w.files, i, err
		}
	}
	if err := w.close(); err != nil {
		return w.files, n, err
	}
	return w.files, n, nil
}

var (
	srcNet = netip.MustParsePrefix("10.0.0.0/8")
	dstNet = netip.MustParsePrefix("172.16.0.0/12")
)

// randomAddr returns a random IPv4 address inside p.
func randomAddr(rng *rand.Rand, p netip.Prefix) string {
	b := p.Addr().As4()
	v := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	v |= rng.Uint32() >> p.Bits()
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)}).String()
}

// rotatingWriter writes numbered files of about limit bytes. Each file is
// written as name.tmp and renamed when complete, so ingest never reads a
// partial file.
type rotatingWriter struct {
	dir    string
	prefix string
	limit  int64

	f     *os.File
	bw    *bufio.Writer
	size  int64
	files int
}

func (w *rotatingWriter) write(line string) error {
	if w.f == nil {
		name := filepath.Join(w.dir, fmt.Sprintf("%s-%05d.csv.tmp", w.prefix, w.files+1))
		f, err := os.Create(name)
		if err != nil {
			return err
		}
		w.f, w.bw, w.size = f, bufio.NewWriterSize(f, 1<<20), 0
	}
	n, err := w.bw.WriteString(line)
	w.size += int64(n)
	if err != nil {
		return err
	}
	if w.size >= w.limit {
		return w.close()
	}
	return nil
}

func (w *rotatingWriter) close() error {
	if w.f == nil {
		return nil
	}
	tmp := w.f.Name()
	err := w.bw.Flush()
	if cerr := w.f.Close(); err == nil {
		err = cerr
	}
	w.f = nil
	if err != nil {
		return err
	}
	w.files++
	return os.Rename(tmp, strings.TrimSuffix(tmp, ".tmp"))
}

// abort removes a partly written file after an error.
func (w *rotatingWriter) abort() {
	if w.f != nil {
		w.f.Close()
		os.Remove(w.f.Name())
	}
}
