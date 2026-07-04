package mkv

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
)

// writeTestMKVFile builds an mkv on disk: video kf@0,40(p),80(kf),120(p),
// audio@0,20,...,140 (Xiph 미사용, 개별 블록), subtitle@10(dur 30).
func writeTestMKVFile(t *testing.T) string {
	t.Helper()
	var c1 []byte
	c1 = appendUintElement(c1, idTimestamp, 0)
	c1 = appendSimpleBlock(c1, 1, 0, 0x80, []byte("v-kf0"))
	for ts := int16(0); ts < 80; ts += 20 {
		c1 = appendSimpleBlock(c1, 2, ts, 0x80, []byte{byte(ts)})
	}
	var blk []byte
	blk = appendVintSize(blk, 3)
	blk = append(blk, 0, 10, 0x00)
	blk = append(blk, []byte("sub0")...)
	var bg []byte
	bg = appendElement(bg, idBlock, blk)
	bg = appendUintElement(bg, idBlockDur, 30)
	c1 = appendElement(c1, idBlockGroup, bg)
	c1 = appendSimpleBlock(c1, 1, 40, 0x00, []byte("v-p1"))

	var c2 []byte
	c2 = appendUintElement(c2, idTimestamp, 80)
	c2 = appendSimpleBlock(c2, 1, 0, 0x80, []byte("v-kf2"))
	c2 = appendSimpleBlock(c2, 1, 40, 0x00, []byte("v-p3"))
	for ts := int16(0); ts < 80; ts += 20 {
		c2 = appendSimpleBlock(c2, 2, ts, 0x80, []byte{byte(80 + ts)})
	}

	var clusters []byte
	clusters = appendElement(clusters, idCluster, c1)
	clusters = appendElement(clusters, idCluster, c2)
	data := buildTestMKV(t, clusters)
	path := filepath.Join(t.TempDir(), "in.mkv")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestImportFull(t *testing.T) {
	src := writeTestMKVFile(t)
	dst := filepath.Join(t.TempDir(), "out.gmc")
	res, err := Import(src, dst, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tracks != 3 || len(res.SkippedTracks) != 0 {
		t.Fatalf("result = %+v", res)
	}
	if res.Frames != 4+8+1 {
		t.Fatalf("frames = %d", res.Frames)
	}

	r, err := gmc.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("imported file must be finalized")
	}
	trs := r.Tracks()
	if len(trs) != 3 {
		t.Fatalf("tracks = %+v", trs)
	}
	// track 1: video, timebase = scale(1e6)/1e9
	if trs[0].Kind != gmc.KindVideo || trs[0].Codec != codec.CodecAVC ||
		trs[0].TimebaseNum != 1000000 || trs[0].TimebaseDen != 1000000000 {
		t.Fatalf("video track = %+v", trs[0])
	}
	vp, priv, err := codec.DecodeVideoPrivate(trs[0].Private)
	if err != nil || vp.Width != 1280 || vp.Height != 720 || !bytes.Equal(priv, []byte{1, 0x64, 0, 31}) {
		t.Fatalf("video envelope = %+v %x err=%v", vp, priv, err)
	}
	ap, _, err := codec.DecodeAudioPrivate(trs[1].Private)
	if err != nil || ap.SampleRate != 48000 || ap.Channels != 2 {
		t.Fatalf("audio envelope = %+v err=%v", ap, err)
	}
	// tags: TITLE + start time from DateUTC(0 = 2001-01-01)
	if string(r.Tags()["TITLE"]) != "demo" {
		t.Fatalf("tags = %v", r.Tags())
	}
	st, ok := r.StartTime()
	if !ok || st.UnixNano() != 978307200*1_000_000_000 {
		t.Fatalf("start = %v ok=%v", st, ok)
	}
	// frame-level check: video frames in storage order with pts = mkv ts
	it, err := r.SeekPTS(trs[0].ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	var vd [][]byte
	var vpts []uint64
	for it.Next() {
		vd = append(vd, it.Frame().Data)
		vpts = append(vpts, it.Frame().PTS)
	}
	if len(vd) != 4 || string(vd[0]) != "v-kf0" || vpts[3] != 120 {
		t.Fatalf("video frames %q pts %v", vd, vpts)
	}
	// subtitle payload carries the duration prefix
	it2, _ := r.SeekPTS(trs[2].ID, 0)
	if !it2.Next() {
		t.Fatal(it2.Err())
	}
	dur, txt, err := codec.DecodeTextFrame(it2.Frame().Data)
	if err != nil || dur != 30 || txt != "sub0" {
		t.Fatalf("subtitle dur=%d txt=%q err=%v", dur, txt, err)
	}
}

func TestImportRange(t *testing.T) {
	src := writeTestMKVFile(t)
	dst := filepath.Join(t.TempDir(), "range.gmc")
	// [50ms, 100ms): video snaps back to kf@0? No — last kf <= 50 is kf@0;
	// end: first kf >= 100 doesn't exist -> to file end.
	res, err := Import(src, dst, ImportOptions{Range: Range{From: 50 * time.Millisecond, To: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := gmc.Open(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	trs := r.Tracks()
	it, _ := r.SeekPTS(trs[0].ID, 0)
	var vpts []uint64
	for it.Next() {
		vpts = append(vpts, it.Frame().PTS)
	}
	// video: snap start kf@0 -> frames 0,40,80,120 all included (end rule: no kf >= 100)
	if len(vpts) != 4 || vpts[0] != 0 {
		t.Fatalf("video pts = %v", vpts)
	}
	// audio: exact cut [From, To) = [50, 100) -> frames 60, 80
	it2, _ := r.SeekPTS(trs[1].ID, 0)
	var apts []uint64
	for it2.Next() {
		apts = append(apts, it2.Frame().PTS)
	}
	if len(apts) != 2 || apts[0] != 60 || apts[len(apts)-1] != 80 {
		t.Fatalf("audio pts = %v", apts)
	}
	_ = res
}

func TestImportSkipsUnknownCodec(t *testing.T) {
	// build MKV with an unsupported codec track
	var te []byte
	te = appendUintElement(te, idTrackNumber, 9)
	te = appendUintElement(te, idTrackType, trackTypeVideo)
	te = appendStringElement(te, idCodecID, "V_VP9")
	var tracks []byte
	tracks = appendElement(tracks, idTrackEntry, te)
	var info []byte
	info = appendUintElement(info, idTimestampScale, 1000000)
	var seg []byte
	seg = appendElement(seg, idInfo, info)
	seg = appendElement(seg, idTracks, tracks)
	var ebml []byte
	ebml = appendStringElement(ebml, idDocType, "matroska")
	var out []byte
	out = appendElement(out, idEBML, ebml)
	out = appendElement(out, idSegment, seg)

	path := filepath.Join(t.TempDir(), "vp9.mkv")
	os.WriteFile(path, out, 0o644)
	dst := filepath.Join(t.TempDir(), "vp9.gmc")
	res, err := Import(path, dst, ImportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.SkippedTracks) != 1 || res.SkippedTracks[0].CodecID != "V_VP9" {
		t.Fatalf("skipped = %+v", res.SkippedTracks)
	}
}
