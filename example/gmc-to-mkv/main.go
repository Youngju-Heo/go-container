// Command gmc-to-mkv converts a gmc file into a Matroska file, optionally restricted to a time range.
package main

import (
	"flag"
	"fmt"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/mkv"
)

func main() {
	from := flag.String("from", "", "range start, e.g. 10s (default: beginning)")
	to := flag.String("to", "", "range end, e.g. 20s (default: end of file)")
	scale := flag.Uint64("scale", 1000000, "output TimestampScale in ns/unit")
	tracksFlag := flag.String("tracks", "", "comma-separated gmc track IDs to export, e.g. 1,3 (default: all)")
	flag.Parse()
	if flag.NArg() != 2 {
		log.Fatal("usage: gmc-to-mkv [-from 10s] [-to 20s] [-scale 1000000] [-tracks 1,3] <in.gmc> <out.mkv>")
	}
	gmcPath, mkvPath := flag.Arg(0), flag.Arg(1)

	var tracks []gmc.TrackID
	if *tracksFlag != "" {
		for _, s := range strings.Split(*tracksFlag, ",") {
			id, err := strconv.ParseUint(s, 10, 16)
			if err != nil {
				log.Fatalf("parse -tracks: %v", err)
			}
			tracks = append(tracks, gmc.TrackID(id))
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

	res, err := mkv.Export(gmcPath, mkvPath, mkv.ExportOptions{Range: rng, TimestampScale: *scale, Tracks: tracks})
	if err != nil {
		log.Fatalf("export: %v", err)
	}

	fmt.Printf("tracks: %d\n", res.Tracks)
	fmt.Printf("frames: %d\n", res.Frames)
	for _, s := range res.SkippedTracks {
		fmt.Printf("skipped track #%d codec=%s reason=%s\n", s.Number, s.CodecID, s.Reason)
	}
}
