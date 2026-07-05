package mkv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

var samplePaths = []string{"../sample/video-clip.mkv", "../sample/test-clip.mkv", "../sample/test-clip-hevc.mkv"}

// sampleRanges gives each sample a range-import window sized to its own
// duration. video-clip.mkv and test-clip.mkv are 30s+ clips and use the
// original 10s..20s window; test-clip-hevc.mkv is only 10s long, so a
// 10s..20s window would be empty/degenerate — use 3s..7s instead.
var sampleRanges = map[string]Range{
	"../sample/test-clip-hevc.mkv": {From: 3 * time.Second, To: 7 * time.Second},
}

func rangeFor(path string) Range {
	if r, ok := sampleRanges[path]; ok {
		return r
	}
	return Range{From: 10 * time.Second, To: 20 * time.Second}
}

func requireSample(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Skipf("sample not available: %v", err)
	}
}

type flatPacket struct {
	track    uint64
	ts       int64
	keyframe bool
	data     []byte
}

func flatten(t *testing.T, path string) ([]TrackEntry, []flatPacket) {
	t.Helper()
	d, pkts := demuxAll(t, path)
	var out []flatPacket
	for _, p := range pkts {
		out = append(out, flatPacket{p.Track, p.Timestamp, p.Keyframe, p.Data})
	}
	return d.Tracks(), out
}

// TestSampleRoundtrip: sample.mkv -> GMC -> MKV, then compare the demuxed
// frame streams (bytes, timestamps, keyframes, track mapping) of the original
// and re-exported files. File bytes differ (our muxer layout != ffmpeg's);
// the logical streams must not.
func TestSampleRoundtrip(t *testing.T) {
	for _, samplePath := range samplePaths {
		samplePath := samplePath
		t.Run(filepath.Base(samplePath), func(t *testing.T) {
			requireSample(t, samplePath)
			dir := t.TempDir()
			gmcPath := filepath.Join(dir, "clip.gmc")
			outPath := filepath.Join(dir, "clip-out.mkv")

			res, err := Import(samplePath, gmcPath, ImportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Tracks == 0 || res.Frames == 0 {
				t.Fatalf("import result = %+v", res)
			}
			if _, err := Export(gmcPath, outPath, ExportOptions{}); err != nil {
				t.Fatal(err)
			}

			srcTracks, srcPkts := flatten(t, samplePath)
			dstTracks, dstPkts := flatten(t, outPath)
			if len(srcTracks) != len(dstTracks) {
				t.Fatalf("tracks %d != %d", len(srcTracks), len(dstTracks))
			}
			// track numbers may be renumbered; map by order of appearance in Tracks()
			renum := map[uint64]uint64{}
			for i := range srcTracks {
				renum[srcTracks[i].Number] = dstTracks[i].Number
				if srcTracks[i].CodecID != dstTracks[i].CodecID {
					t.Fatalf("codec %q != %q", srcTracks[i].CodecID, dstTracks[i].CodecID)
				}
				if !bytes.Equal(srcTracks[i].CodecPrivate, dstTracks[i].CodecPrivate) {
					t.Fatalf("codec private mismatch on track %d", srcTracks[i].Number)
				}
			}
			if len(srcPkts) != len(dstPkts) {
				t.Fatalf("packets %d != %d", len(srcPkts), len(dstPkts))
			}
			for i := range srcPkts {
				s, g := srcPkts[i], dstPkts[i]
				if renum[s.track] != g.track || s.ts != g.ts || s.keyframe != g.keyframe || !bytes.Equal(s.data, g.data) {
					t.Fatalf("packet %d mismatch: src{tr=%d ts=%d kf=%v n=%d} dst{tr=%d ts=%d kf=%v n=%d}",
						i, s.track, s.ts, s.keyframe, len(s.data), g.track, g.ts, g.keyframe, len(g.data))
				}
			}

			switch filepath.Base(samplePath) {
			case "test-clip.mkv":
				assertMultiCodecFixture(t, srcTracks, srcPkts)
			case "test-clip-hevc.mkv":
				assertHevcPcmFixture(t, srcTracks, srcPkts)
			}
		})
	}
}

// assertMultiCodecFixture proves that test-clip.mkv actually exercises what it
// was added for: 5 distinct codecs, real B-frame reordering on the video
// track (non-monotonic decode-order timestamps), and multiple audio tracks.
func assertMultiCodecFixture(t *testing.T, tracks []TrackEntry, pkts []flatPacket) {
	t.Helper()
	wantCodecs := []string{"V_MPEG4/ISO/AVC", "A_FLAC", "A_AAC", "A_OPUS", "S_TEXT/UTF8"}
	if len(tracks) != len(wantCodecs) {
		t.Fatalf("track count = %d, want %d", len(tracks), len(wantCodecs))
	}
	var videoNum uint64
	for i, te := range tracks {
		if te.CodecID != wantCodecs[i] {
			t.Fatalf("track %d codec = %q, want %q", i, te.CodecID, wantCodecs[i])
		}
		if te.CodecID == "V_MPEG4/ISO/AVC" {
			videoNum = te.Number
		}
	}

	var videoTS []int64
	audioTracks := map[uint64]bool{}
	for _, p := range pkts {
		if p.track == videoNum {
			videoTS = append(videoTS, p.ts)
		}
	}
	for _, te := range tracks {
		if te.CodecID == "A_FLAC" || te.CodecID == "A_AAC" || te.CodecID == "A_OPUS" {
			for _, p := range pkts {
				if p.track == te.Number {
					audioTracks[p.track] = true
					break
				}
			}
		}
	}

	nonMonotonic := 0
	for i := 1; i < len(videoTS); i++ {
		if videoTS[i] < videoTS[i-1] {
			nonMonotonic++
		}
	}
	if nonMonotonic < 1 {
		t.Fatalf("video packet timestamps are monotonic; expected B-frame reordering (nonMonotonic=%d)", nonMonotonic)
	}
	if len(audioTracks) < 2 {
		t.Fatalf("distinct audio tracks with packets = %d, want >= 2", len(audioTracks))
	}
}

// assertHevcPcmFixture proves that test-clip-hevc.mkv exercises what it was
// added for: an HEVC video track with real B-frame reordering (non-monotonic
// decode-order timestamps) alongside a PCM audio track.
func assertHevcPcmFixture(t *testing.T, tracks []TrackEntry, pkts []flatPacket) {
	t.Helper()
	wantCodecs := []string{"V_MPEGH/ISO/HEVC", "A_PCM/INT/LIT"}
	if len(tracks) != len(wantCodecs) {
		t.Fatalf("track count = %d, want %d", len(tracks), len(wantCodecs))
	}
	var videoNum uint64
	for i, te := range tracks {
		if te.CodecID != wantCodecs[i] {
			t.Fatalf("track %d codec = %q, want %q", i, te.CodecID, wantCodecs[i])
		}
		if te.CodecID == "V_MPEGH/ISO/HEVC" {
			videoNum = te.Number
		}
	}

	var videoTS []int64
	for _, p := range pkts {
		if p.track == videoNum {
			videoTS = append(videoTS, p.ts)
		}
	}
	nonMonotonic := 0
	for i := 1; i < len(videoTS); i++ {
		if videoTS[i] < videoTS[i-1] {
			nonMonotonic++
		}
	}
	if nonMonotonic < 1 {
		t.Fatalf("video packet timestamps are monotonic; expected B-frame reordering (nonMonotonic=%d)", nonMonotonic)
	}
}

// TestSampleRangeImport: a range window of the sample (see rangeFor) must
// produce fewer frames than the full import, and its video must start on a
// keyframe at or before the window start (keyframe snap).
func TestSampleRangeImport(t *testing.T) {
	for _, samplePath := range samplePaths {
		samplePath := samplePath
		t.Run(filepath.Base(samplePath), func(t *testing.T) {
			requireSample(t, samplePath)
			dir := t.TempDir()
			fullPath := filepath.Join(dir, "full.gmc")
			rangePath := filepath.Join(dir, "range.gmc")
			if _, err := Import(samplePath, fullPath, ImportOptions{}); err != nil {
				t.Fatal(err)
			}
			rng := rangeFor(samplePath)
			res, err := Import(samplePath, rangePath, ImportOptions{Range: rng})
			if err != nil {
				t.Fatal(err)
			}
			full, err := Export(fullPath, filepath.Join(dir, "f.mkv"), ExportOptions{})
			if err != nil {
				t.Fatal(err)
			}
			if res.Frames == 0 || res.Frames >= full.Frames {
				t.Fatalf("range frames = %d, full = %d", res.Frames, full.Frames)
			}
			// start boundary: the ranged file's first packet on the VIDEO track must
			// be a keyframe at ts <= 10s (audio packets are all keyframes — filter by
			// the video track number, identified by its "V_" codec prefix)
			out := filepath.Join(dir, "r.mkv")
			if _, err := Export(rangePath, out, ExportOptions{}); err != nil {
				t.Fatal(err)
			}
			trs, pkts := flatten(t, out)
			var videoNum uint64
			for _, te := range trs {
				if len(te.CodecID) > 2 && te.CodecID[:2] == "V_" {
					videoNum = te.Number
					break
				}
			}
			var firstVideo *flatPacket
			for i := range pkts {
				if pkts[i].track == videoNum {
					firstVideo = &pkts[i]
					break
				}
			}
			if firstVideo == nil || !firstVideo.keyframe || firstVideo.ts > rng.From.Milliseconds() {
				t.Fatalf("range start not keyframe-snapped: %+v", firstVideo)
			}
		})
	}
}
