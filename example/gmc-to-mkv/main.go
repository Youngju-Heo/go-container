// Command gmc-to-mkv converts a gmc file into a Matroska file, optionally restricted to a time range.
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
	scale := flag.Uint64("scale", 1000000, "output TimestampScale in ns/unit")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: gmc-to-mkv [-from 10s] [-to 20s] [-scale 1000000] <in.gmc> <out.mkv>")
	}
	gmcPath, mkvPath := flag.Arg(0), flag.Arg(1)

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

	res, err := mkv.Export(gmcPath, mkvPath, mkv.ExportOptions{Range: rng, TimestampScale: *scale})
	if err != nil {
		log.Fatalf("export: %v", err)
	}

	fmt.Printf("tracks: %d\n", res.Tracks)
	fmt.Printf("frames: %d\n", res.Frames)
	for _, s := range res.SkippedTracks {
		fmt.Printf("skipped track #%d codec=%s reason=%s\n", s.Number, s.CodecID, s.Reason)
	}
}
