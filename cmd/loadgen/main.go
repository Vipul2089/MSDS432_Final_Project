// Command loadgen turns NSL-KDD rows into log files for the ingest inbox.
//
// NSL-KDD has no timestamps or IP addresses, so loadgen adds an event time and
// made-up source and destination addresses to each row. Rows are replayed in a
// shuffled order until the target volume is reached, and event times are spread
// evenly over the backfill window, which ends now.
//
// Output line format: event_time,src_ip,dst_ip,<43 NSL-KDD columns>
package main

import (
	"flag"
	"fmt"
	"log"
	"strings"
	"time"
)

func main() {
	var (
		input    = flag.String("input", "/data/raw/nslkdd/KDDTrain+.txt,/data/raw/nslkdd/KDDTest+.txt", "comma-separated NSL-KDD files")
		out      = flag.String("out", "/data/inbox", "directory to write files to (the ingest inbox)")
		backfill = flag.Duration("backfill", 6*time.Hour, "time window covered by the generated events, ending now")
		targetGB = flag.Float64("target-gb", 1, "approximate total size of the generated files")
		fileMB   = flag.Int("file-mb", 100, "approximate size of each file")
		seed     = flag.Uint64("seed", 1, "random seed for row order and addresses")
	)
	flag.Parse()

	rows, err := readRows(strings.Split(*input, ","))
	if err != nil {
		log.Fatal(err)
	}

	g := generator{
		rows:      rows,
		outDir:    *out,
		end:       time.Now().UTC(),
		window:    *backfill,
		fileBytes: int64(*fileMB) << 20,
		seed:      *seed,
	}
	files, events, err := g.run(int64(*targetGB * (1 << 30)))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("wrote %d events to %d files in %s\n", events, files, *out)
}
