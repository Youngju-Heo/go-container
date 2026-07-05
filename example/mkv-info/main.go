// Command mkv-info prints the container info, track list, tags and per-track packet statistics of a Matroska file.
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/Youngju-Heo/go-container/mkv"
)

func main() {
	flag.Parse()
	if flag.NArg() != 1 {
		log.Fatal("usage: mkv-info <file.mkv>")
	}
	path := flag.Arg(0)

	f, err := os.Open(path)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		log.Fatalf("stat: %v", err)
	}

	d, err := mkv.NewDemuxer(f, fi.Size())
	if err != nil {
		log.Fatalf("new demuxer: %v", err)
	}

	info := d.Info()
	fmt.Printf("timestamp scale: %d ns/unit\n", info.TimestampScale)
	fmt.Printf("duration: %.3f units\n", info.Duration)
	if info.HasDate {
		fmt.Printf("date utc: %d ns since 2001-01-01\n", info.DateUTC)
	}

	fmt.Println("tracks:")
	for _, te := range d.Tracks() {
		fmt.Printf("  #%d %s codec=%s", te.Number, trackKindName(te.Type), te.CodecID)
		if te.PixelWidth > 0 {
			fmt.Printf(" %dx%d", te.PixelWidth, te.PixelHeight)
		}
		if te.SamplingFrequency > 0 {
			fmt.Printf(" %.0fHz ch=%d", te.SamplingFrequency, te.Channels)
		}
		fmt.Println()
	}

	fmt.Println("tags:")
	for k, v := range d.Tags() {
		fmt.Printf("  %s=%s\n", k, v)
	}

	type stat struct {
		packets, keyframes int
		maxTS              int64
	}
	stats := map[uint64]*stat{}
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			log.Fatalf("read packet: %v", err)
		}
		s := stats[p.Track]
		if s == nil {
			s = &stat{}
			stats[p.Track] = s
		}
		s.packets++
		if p.Keyframe {
			s.keyframes++
		}
		if p.Timestamp > s.maxTS {
			s.maxTS = p.Timestamp
		}
	}

	fmt.Println("packet stats:")
	for _, te := range d.Tracks() {
		s := stats[te.Number]
		if s == nil {
			continue
		}
		fmt.Printf("  #%d packets=%d keyframes=%d max_ts=%d\n", te.Number, s.packets, s.keyframes, s.maxTS)
	}
}

func trackKindName(t uint8) string {
	switch t {
	case 1:
		return "video"
	case 2:
		return "audio"
	case 17:
		return "subtitle"
	default:
		return fmt.Sprintf("type%d", t)
	}
}
