// Command gmc-stitch-mkv assembles one Matroska file out of an absolute time
// window spanning three committed GMC segment samples
// (sample/test-clip-000.gmc / -001 / -002.gmc, see task 1) — built directly
// with mkv.Muxer (not mkv.Export). The -tracks flag restricts output to the
// given comma-separated gmc TrackIDs (default: all).
package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
	"github.com/Youngju-Heo/go-container/mkv"
)

// matroskaEpochUnixNano is 2001-01-01T00:00:00 UTC, the Matroska DateUTC
// epoch (mkv.mkvEpochUnixNano mirrors this but is unexported).
const matroskaEpochUnixNano = 978307200 * 1_000_000_000

func main() {
	out := flag.String("out", "", "output mkv path (default: stitched.mkv in cwd, or 2nd positional arg)")
	tracksFlag := flag.String("tracks", "", "comma-separated gmc TrackIDs to include, e.g. 1,3 (default: all)")
	flag.Parse()

	var selectedIDs []gmc.TrackID
	if *tracksFlag != "" {
		for _, s := range strings.Split(*tracksFlag, ",") {
			n, err := strconv.ParseUint(strings.TrimSpace(s), 10, 16)
			if err != nil {
				log.Fatalf("parse -tracks: %v", err)
			}
			selectedIDs = append(selectedIDs, gmc.TrackID(n))
		}
	}

	// 1. Resolve arguments: segments dir (default ./sample), output path
	// (default stitched.mkv in cwd).
	segDir := "./sample"
	if flag.NArg() > 0 {
		segDir = flag.Arg(0)
	}
	outPath := "stitched.mkv"
	if *out != "" {
		outPath = *out
	} else if flag.NArg() > 1 {
		outPath = flag.Arg(1)
	}
	segPaths := []string{
		filepath.Join(segDir, "test-clip-000.gmc"),
		filepath.Join(segDir, "test-clip-001.gmc"),
		filepath.Join(segDir, "test-clip-002.gmc"),
	}

	// 2. Open segment 0 to build the output Matroska track list (numbers
	// 1..N in gmc-ID order) and record its start time.
	seg0, err := gmc.Open(segPaths[0])
	if err != nil {
		log.Fatalf("open %s: %v", segPaths[0], err)
	}
	firstStart, ok := seg0.StartTime()
	if !ok {
		log.Fatalf("%s: missing start time", segPaths[0])
	}
	seg0Tracks := seg0.Tracks()
	if len(selectedIDs) > 0 {
		selected := make(map[gmc.TrackID]bool, len(selectedIDs))
		for _, id := range selectedIDs {
			selected[id] = true
		}
		for id := range selected {
			found := false
			for _, ti := range seg0Tracks {
				if ti.ID == id {
					found = true
					break
				}
			}
			if !found {
				available := make([]gmc.TrackID, len(seg0Tracks))
				for i, ti := range seg0Tracks {
					available[i] = ti.ID
				}
				log.Fatalf("unknown track id %d (available: %v)", id, available)
			}
		}
		var filtered []gmc.TrackInfo
		for _, ti := range seg0Tracks {
			if selected[ti.ID] {
				filtered = append(filtered, ti)
			}
		}
		seg0Tracks = filtered
	}
	entries, entryIdx, isVideo, isText := buildTrackEntries(seg0Tracks)
	var videoID gmc.TrackID
	for id, v := range isVideo {
		if v {
			videoID = id
		}
	}
	seg0.Close()

	// 3. Open the last segment only to read its start time, which fixes the
	// far end of the absolute window.
	segLast, err := gmc.Open(segPaths[len(segPaths)-1])
	if err != nil {
		log.Fatalf("open %s: %v", segPaths[len(segPaths)-1], err)
	}
	lastStart, ok := segLast.StartTime()
	if !ok {
		log.Fatalf("%s: missing start time", segPaths[len(segPaths)-1])
	}
	segLast.Close()

	// 4. The stitch window is [firstSegStart+5s, lastSegStart+5s) — starts
	// mid-segment-0 and ends mid-segment-2.
	winStart := firstStart.Add(5 * time.Second)
	winEnd := lastStart.Add(5 * time.Second)
	winStartNs := winStart.UnixNano()
	winEndNs := winEnd.UnixNano()

	// 5. Determine the output timeline origin: the window start (5s) falls
	// mid-GOP, so the video must actually start at the preceding keyframe
	// (mkvmerge-style keyframe snap). SeekTime on the video track alone
	// positions right at that sync point; its first frame's absolute time
	// is the origin. This is a dedicated pre-pass — it closes before the
	// main loop reopens segment 0.
	r0, err := gmc.Open(segPaths[0])
	if err != nil {
		log.Fatalf("open %s: %v", segPaths[0], err)
	}
	it0, err := r0.SeekTime(winStart, videoID)
	if err != nil {
		log.Fatalf("seek segment 0 for origin: %v", err)
	}
	if !it0.Next() {
		log.Fatalf("segment 0: no video frame at/after window start")
	}
	originNs := firstStart.UnixNano() + int64(it0.Frame().PTS)*1_000_000 // 1ms timebase
	r0.Close()

	// 6. Open the output and write the Matroska header. DateUTC is the
	// origin (the actual first output timestamp), not winStart.
	f, err := os.Create(outPath)
	if err != nil {
		log.Fatalf("create %s: %v", outPath, err)
	}
	defer f.Close()
	m := mkv.NewMuxer(f, 1_000_000) // 1ms scale, matching the segments' timebase
	info := mkv.FileInfo{DateUTC: originNs - matroskaEpochUnixNano, HasDate: true}
	title := fmt.Sprintf("stitched %s..%s", winStart.Sub(firstStart), winEnd.Sub(firstStart))
	if err := m.WriteHeader(info, entries, map[string]string{"TITLE": title}); err != nil {
		log.Fatalf("write header: %v", err)
	}

	// 7. Walk the 3 segments in order, seeking each into the window and
	// copying frames that fall inside it with rebased timestamps.
	// Video: no start filter needed (the iterator already begins at the
	// snap keyframe, before winStart — the "warm-up" GOP); it stops at the
	// first keyframe at/after winEnd, keeping the cut GOP-granular.
	// Audio/text: exact cut at [winStart, winEnd) — every frame is its own
	// sync point, so no snap is needed.
	var frames, maxTS int64
	videoDone := false
	counts := map[uint64]int{}
	firstTS := map[uint64]int64{}
	lastTS := map[uint64]int64{}
	for _, segPath := range segPaths {
		r, err := gmc.Open(segPath)
		if err != nil {
			log.Fatalf("open %s: %v", segPath, err)
		}
		segStart, ok := r.StartTime()
		if !ok {
			log.Fatalf("%s: missing start time", segPath)
		}
		seekAt := winStart
		if segStart.After(seekAt) {
			seekAt = segStart
		}
		it, err := r.SeekTime(seekAt, selectedIDs...)
		if err != nil {
			log.Fatalf("seek %s: %v", segPath, err)
		}

		for it.Next() {
			fr := it.Frame()
			id := it.Track()
			absNs := segStart.UnixNano() + int64(fr.PTS)*1_000_000

			if isVideo[id] {
				if videoDone || absNs < originNs {
					continue
				}
				if fr.Keyframe && absNs >= winEndNs {
					videoDone = true // GOP-granular stop: this keyframe starts the next window
					continue
				}
			} else if absNs < winStartNs || absNs >= winEndNs {
				continue
			}

			num := entries[entryIdx[id]].Number
			pkt := mkv.Packet{Track: num, Keyframe: fr.Keyframe, Data: fr.Data}
			if isText[id] {
				dur, txt, derr := codec.DecodeTextFrame(fr.Data)
				if derr != nil {
					log.Fatalf("decode text frame (track %d): %v", id, derr)
				}
				pkt.Duration = int64(dur)
				pkt.Data = []byte(txt)
			}
			pkt.Timestamp = (absNs - originNs) / 1_000_000
			if err := m.WritePacket(pkt); err != nil {
				log.Fatalf("write packet: %v", err)
			}

			counts[num]++
			if _, seen := firstTS[num]; !seen {
				firstTS[num] = pkt.Timestamp
			}
			lastTS[num] = pkt.Timestamp
			if pkt.Timestamp > maxTS {
				maxTS = pkt.Timestamp
			}
			frames++
		}
		if err := it.Err(); err != nil {
			log.Fatalf("iterate %s: %v", segPath, err)
		}
		r.Close()
	}

	// 8. Finalize the output.
	if err := m.Finalize(float64(maxTS)); err != nil {
		log.Fatalf("finalize: %v", err)
	}
	fmt.Printf("stitched 3 segments -> %s (window %s..%s, %d frames)\n", outPath, winStart.Sub(firstStart), winEnd.Sub(firstStart), frames)
	for _, te := range entries {
		fmt.Printf("  #%d %s: packets=%d first_ts=%dms last_ts=%dms\n", te.Number, trackKindName(te.Type), counts[te.Number], firstTS[te.Number], lastTS[te.Number])
	}

	// 9. Re-demux the output and print per-track stats, confirming it reads
	// back as one continuous timeline.
	if err := verify(outPath); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

// buildTrackEntries converts a segment's GMC tracks (already sorted by ID)
// into Matroska TrackEntries numbered 1..N in the same order, decoding each
// track's codec private envelope per its Kind.
func buildTrackEntries(tracks []gmc.TrackInfo) ([]mkv.TrackEntry, map[gmc.TrackID]int, map[gmc.TrackID]bool, map[gmc.TrackID]bool) {
	entries := make([]mkv.TrackEntry, 0, len(tracks))
	entryIdx := make(map[gmc.TrackID]int, len(tracks))
	isVideo := make(map[gmc.TrackID]bool, len(tracks))
	isText := make(map[gmc.TrackID]bool, len(tracks))
	for _, ti := range tracks {
		te := mkv.TrackEntry{Number: uint64(len(entries) + 1), CodecID: ti.Codec}
		switch ti.Kind {
		case gmc.KindVideo:
			vp, priv, err := codec.DecodeVideoPrivate(ti.Private)
			if err != nil {
				log.Fatalf("decode video private (track %d): %v", ti.ID, err)
			}
			te.Type = 1 // video
			te.CodecPrivate = priv
			te.PixelWidth, te.PixelHeight = uint64(vp.Width), uint64(vp.Height)
			isVideo[ti.ID] = true
		case gmc.KindAudio:
			ap, priv, err := codec.DecodeAudioPrivate(ti.Private)
			if err != nil {
				log.Fatalf("decode audio private (track %d): %v", ti.ID, err)
			}
			te.Type = 2 // audio
			te.CodecPrivate = priv
			te.SamplingFrequency = float64(ap.SampleRate)
			if ap.OutputSampleRate > 0 {
				te.OutputSamplingFrequency = float64(ap.OutputSampleRate)
			}
			te.Channels, te.BitDepth = uint64(ap.Channels), uint64(ap.BitDepth)
		case gmc.KindData:
			priv, err := codec.DecodeTextPrivate(ti.Private)
			if err != nil {
				log.Fatalf("decode text private (track %d): %v", ti.ID, err)
			}
			te.Type = 17 // subtitle
			te.CodecPrivate = priv
			isText[ti.ID] = true
		}
		entryIdx[ti.ID] = len(entries)
		entries = append(entries, te)
	}
	return entries, entryIdx, isVideo, isText
}

// verify re-opens the stitched file with mkv.Demuxer and prints per-track
// packet stats, confirming the stitched window is one continuous timeline.
func verify(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	d, err := mkv.NewDemuxer(f, fi.Size())
	if err != nil {
		return err
	}

	fmt.Printf("verify %s: tracks=%d\n", path, len(d.Tracks()))

	type stat struct {
		count       int
		first, last int64
	}
	stats := map[uint64]*stat{}
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		s, ok := stats[p.Track]
		if !ok {
			s = &stat{first: p.Timestamp}
			stats[p.Track] = s
		}
		s.count++
		s.last = p.Timestamp
	}

	for _, te := range d.Tracks() {
		s := stats[te.Number]
		if s == nil {
			continue
		}
		fmt.Printf("  #%d %s: packets=%d first_ts=%dms last_ts=%dms\n", te.Number, trackKindName(te.Type), s.count, s.first, s.last)
	}
	return nil
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
