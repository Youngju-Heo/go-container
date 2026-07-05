// Command mkv-to-gmc converts a Matroska file into a gmc file, optionally restricted to a time range.
package main

import (
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/Youngju-Heo/go-container/mkv"
)

func main() {
	from := flag.String("from", "", "range start, e.g. 10s (default: beginning)")
	to := flag.String("to", "", "range end, e.g. 20s (default: end of file)")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: mkv-to-gmc [-from 10s] [-to 20s] <in.mkv> <out.gmc>")
	}
	mkvPath, gmcPath := flag.Arg(0), flag.Arg(1)

	var rng mkv.Range
	if *from != "" {
		d, err := time.ParseDuration(*from)
		if err != nil {
			log.Fatalf("parse -from: %v", err)
		}
		rng.From = d
	}
	if *to != "" {
		d, err := time.ParseDuration(*to)
		if err != nil {
			log.Fatalf("parse -to: %v", err)
		}
		rng.To = d
	}

	res, err := mkv.Import(mkvPath, gmcPath, mkv.ImportOptions{Range: rng})
	if err != nil {
		log.Fatalf("import: %v", err)
	}

	fmt.Printf("tracks: %d\n", res.Tracks)
	fmt.Printf("frames: %d\n", res.Frames)
	for _, s := range res.SkippedTracks {
		fmt.Printf("skipped track #%d codec=%s reason=%s\n", s.Number, s.CodecID, s.Reason)
	}
}
