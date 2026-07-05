// Command mkv-to-gmc converts a Matroska file into a gmc file, optionally restricted to a time range.
package main

import (
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Youngju-Heo/go-container/mkv"
)

func main() {
	from := flag.String("from", "", "range start, e.g. 10s (default: beginning)")
	to := flag.String("to", "", "range end, e.g. 20s (default: end of file)")
	tracksFlag := flag.String("tracks", "", "comma-separated MKV track numbers to import, e.g. 1,3 (default: all)")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: mkv-to-gmc [-from 10s] [-to 20s] [-tracks 1,3] <in.mkv> <out.gmc>")
	}
	mkvPath, gmcPath := flag.Arg(0), flag.Arg(1)

	var tracks []uint64
	if *tracksFlag != "" {
		for _, s := range strings.Split(*tracksFlag, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
			if err != nil {
				log.Fatalf("parse -tracks: %v", err)
			}
			tracks = append(tracks, n)
		}
	}

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

	res, err := mkv.Import(mkvPath, gmcPath, mkv.ImportOptions{Range: rng, Tracks: tracks})
	if err != nil {
		log.Fatalf("import: %v", err)
	}

	fmt.Printf("tracks: %d\n", res.Tracks)
	fmt.Printf("frames: %d\n", res.Frames)
	for _, s := range res.SkippedTracks {
		fmt.Printf("skipped track #%d codec=%s reason=%s\n", s.Number, s.CodecID, s.Reason)
	}
}
