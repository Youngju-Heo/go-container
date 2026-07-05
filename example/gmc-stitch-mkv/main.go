// Command gmc-stitch-mkv simulates a DVR-style segmented recorder (3 GMC
// files back to back) and stitches an absolute-time window spanning all three
// — starting and ending mid-segment — into one fresh Matroska file built
// directly with mkv.Muxer (not mkv.Export).
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
	"github.com/Youngju-Heo/go-container/mkv"
)

const (
	sampleRate      = 8000
	samplesPerFrame = 400 // 50ms
	segmentSeconds  = 2
	segmentCount    = 3

	// matroskaEpochUnixNano is 2001-01-01T00:00:00 UTC, the Matroska DateUTC
	// epoch (mkv.mkvEpochUnixNano mirrors this but is unexported).
	matroskaEpochUnixNano = 978307200 * 1_000_000_000
)

func main() {
	outDir := "./stitch-out"
	flag.Parse()
	if flag.NArg() > 0 {
		outDir = flag.Arg(0)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatalf("mkdir: %v", err)
	}

	// Fixed base time for deterministic output.
	base := time.Unix(1_700_000_000, 0).UTC()
	freqs := [segmentCount]float64{440, 554, 659} // audible tone change per segment

	segPaths := make([]string, segmentCount)
	for i := 0; i < segmentCount; i++ {
		segPaths[i] = filepath.Join(outDir, fmt.Sprintf("seg-%03d.gmc", i))
		start := base.Add(time.Duration(i) * segmentSeconds * time.Second)
		if err := writeSegment(segPaths[i], i, start, freqs[i]); err != nil {
			log.Fatalf("write segment %d: %v", i, err)
		}
	}

	// Window spans [base+1s, base+5s): starts mid-seg-000, covers all of
	// seg-001, ends mid-seg-002.
	winStart := base.Add(1 * time.Second)
	winEnd := base.Add(5 * time.Second)
	outPath := filepath.Join(outDir, "out.mkv")

	frames, err := stitch(segPaths, winStart, winEnd, outPath)
	if err != nil {
		log.Fatalf("stitch: %v", err)
	}
	fmt.Printf("stitched %d segments -> %s (window %s..%s)\n", segmentCount, outPath, winStart.Sub(base), winEnd.Sub(base))
	fmt.Printf("packets written: %d\n", frames)

	if err := verify(outPath); err != nil {
		log.Fatalf("verify: %v", err)
	}
}

// writeSegment creates one GMC segment file: a PCM audio track (a distinct
// tone per segment) and a text track with one event per second.
func writeSegment(path string, idx int, start time.Time, freq float64) error {
	// A small checkpoint threshold keeps the audio index dense (same
	// rationale as gmc-basic) even though each segment is written in well
	// under a second of wall-clock time.
	w, err := gmc.Create(path, gmc.CreateOptions{CheckpointBytes: 4096})
	if err != nil {
		return fmt.Errorf("create: %w", err)
	}

	audioID, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindAudio, Codec: codec.CodecPCM,
		TimebaseNum: 1, TimebaseDen: sampleRate,
		Private: codec.EncodeAudioPrivate(codec.AudioParams{
			SampleRate: sampleRate, Channels: 1, BitDepth: 16,
		}, nil),
	})
	if err != nil {
		return fmt.Errorf("add audio track: %w", err)
	}

	textID, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindData, Codec: codec.CodecTextUTF8,
		TimebaseNum: 1, TimebaseDen: 1000,
		Private: codec.EncodeTextPrivate(nil),
	})
	if err != nil {
		return fmt.Errorf("add text track: %w", err)
	}

	if err := w.SetStartTime(start); err != nil {
		return fmt.Errorf("set start time: %w", err)
	}

	for sec := 0; sec < segmentSeconds; sec++ {
		absSec := idx*segmentSeconds + sec
		text := fmt.Sprintf("seg %d t=%ds", idx, absSec)
		if err := w.WriteFrame(textID, gmc.Frame{PTS: uint64(sec * 1000), Keyframe: true, Data: codec.EncodeTextFrame(900, text)}); err != nil {
			return fmt.Errorf("write text frame: %w", err)
		}
		for f := 0; f < sampleRate/samplesPerFrame; f++ {
			off := sec*sampleRate + f*samplesPerFrame
			if err := w.WriteFrame(audioID, gmc.Frame{PTS: uint64(off), Keyframe: true, Data: sineFrame(freq, off)}); err != nil {
				return fmt.Errorf("write audio frame: %w", err)
			}
		}
	}

	return w.Finalize()
}

// sineFrame renders samplesPerFrame int16 LE samples of a sine tone starting
// at sample offset offSamples.
func sineFrame(freq float64, offSamples int) []byte {
	data := make([]byte, samplesPerFrame*2)
	for i := 0; i < samplesPerFrame; i++ {
		t := float64(offSamples+i) / sampleRate
		v := int16(3000 * math.Sin(2*math.Pi*freq*t))
		binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
	}
	return data
}

// stitch builds a fresh Matroska file covering [winStart, winEnd) by reading
// each segment from the middle via SeekTime and rebasing timestamps to the
// window origin. It uses mkv.Muxer directly, mapping GMC track info to
// Matroska TrackEntry fields by hand — the step mkv.Export normally hides.
func stitch(segPaths []string, winStart, winEnd time.Time, outPath string) (int, error) {
	f, err := os.Create(outPath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	seg0, err := gmc.Open(segPaths[0])
	if err != nil {
		return 0, err
	}
	defer seg0.Close()

	var entries []mkv.TrackEntry
	var audioID, textID gmc.TrackID
	var audioNum, textNum uint64
	num := uint64(1)
	for _, ti := range seg0.Tracks() {
		switch ti.Kind {
		case gmc.KindAudio:
			ap, priv, derr := codec.DecodeAudioPrivate(ti.Private)
			if derr != nil {
				return 0, fmt.Errorf("decode audio private: %w", derr)
			}
			entries = append(entries, mkv.TrackEntry{
				Number: num, Type: 2, CodecID: ti.Codec, CodecPrivate: priv,
				SamplingFrequency: float64(ap.SampleRate), Channels: uint64(ap.Channels), BitDepth: uint64(ap.BitDepth),
			})
			audioID, audioNum = ti.ID, num
		case gmc.KindData:
			priv, derr := codec.DecodeTextPrivate(ti.Private)
			if derr != nil {
				return 0, fmt.Errorf("decode text private: %w", derr)
			}
			entries = append(entries, mkv.TrackEntry{Number: num, Type: 17, CodecID: ti.Codec, CodecPrivate: priv})
			textID, textNum = ti.ID, num
		}
		num++
	}

	m := mkv.NewMuxer(f, 1_000_000) // 1ms scale
	info := mkv.FileInfo{
		DateUTC: winStart.UnixNano() - matroskaEpochUnixNano,
		HasDate: true,
	}
	if err := m.WriteHeader(info, entries, map[string]string{"TITLE": "stitched window"}); err != nil {
		return 0, err
	}

	winStartNs := winStart.UnixNano()
	winEndNs := winEnd.UnixNano()
	var frames int
	var maxTS int64

	// All segments share the same track layout (audioID/textID), so the
	// mapping built from segment 0 above is reused for every segment.
	for _, path := range segPaths {
		r, err := gmc.Open(path)
		if err != nil {
			return 0, err
		}
		segStart, ok := r.StartTime()
		if !ok {
			r.Close()
			return 0, fmt.Errorf("segment %s: missing start time", path)
		}
		// SeekTime clamps a pre-start target to the beginning of the stream,
		// so segments entirely inside the window can simply seek to winStart.
		seekAt := winStart
		if segStart.After(seekAt) {
			seekAt = segStart
		}
		it, err := r.SeekTime(seekAt)
		if err != nil {
			r.Close()
			return 0, err
		}
		for it.Next() {
			fr := it.Frame()
			var absNs int64
			var p mkv.Packet
			switch it.Track() {
			case audioID:
				absNs = segStart.UnixNano() + int64(fr.PTS)*1_000_000_000/sampleRate
				p = mkv.Packet{Track: audioNum, Data: fr.Data}
			case textID:
				absNs = segStart.UnixNano() + int64(fr.PTS)*1_000_000 // ms timebase -> ns
				dur, txt, derr := codec.DecodeTextFrame(fr.Data)
				if derr != nil {
					continue // non-conforming payload: skip frame
				}
				p = mkv.Packet{Track: textNum, Duration: int64(dur), Data: []byte(txt)}
			default:
				continue
			}
			if absNs < winStartNs || absNs >= winEndNs {
				// Exact cut at the window edge — fine here since every GMC
				// frame in this example is a keyframe; a video track would
				// need to snap the start to the preceding keyframe instead.
				continue
			}
			p.Keyframe = true
			p.Timestamp = (absNs - winStartNs) / 1_000_000
			if err := m.WritePacket(p); err != nil {
				r.Close()
				return 0, err
			}
			if p.Timestamp > maxTS {
				maxTS = p.Timestamp
			}
			frames++
		}
		if err := it.Err(); err != nil {
			r.Close()
			return 0, err
		}
		r.Close()
	}

	if err := m.Finalize(float64(maxTS)); err != nil {
		return 0, err
	}
	return frames, nil
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

	fmt.Printf("tracks: %d\n", len(d.Tracks()))

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
