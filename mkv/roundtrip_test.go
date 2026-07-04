package mkv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const samplePath = "../sample/video-clip.mkv"

func requireSample(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(samplePath); err != nil {
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
	requireSample(t)
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
}

// TestSampleRangeImport: a 10s..20s window of the sample must produce fewer
// frames than the full import, and its video must start on a keyframe at or
// before the 10s mark (keyframe snap).
func TestSampleRangeImport(t *testing.T) {
	requireSample(t)
	dir := t.TempDir()
	fullPath := filepath.Join(dir, "full.gmc")
	rangePath := filepath.Join(dir, "range.gmc")
	if _, err := Import(samplePath, fullPath, ImportOptions{}); err != nil {
		t.Fatal(err)
	}
	res, err := Import(samplePath, rangePath, ImportOptions{Range: Range{From: 10 * time.Second, To: 20 * time.Second}})
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
	if firstVideo == nil || !firstVideo.keyframe || firstVideo.ts > 10000 {
		t.Fatalf("range start not keyframe-snapped: %+v", firstVideo)
	}
}
