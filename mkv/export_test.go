package mkv

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
)

// buildTestGMC writes a convention-conforming GMC file:
// video (1ms timebase) kf@0,p@40,kf@80,p@120; audio @0..140 step 20; text @10 dur30.
func buildTestGMC(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "src.gmc")
	w, err := gmc.Create(path, gmc.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindVideo, Codec: codec.CodecAVC,
		TimebaseNum: 1, TimebaseDen: 1000, Reordered: true,
		Private: codec.EncodeVideoPrivate(codec.VideoParams{Width: 640, Height: 480}, []byte{1, 2, 3}),
	})
	audio, _ := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindAudio, Codec: codec.CodecFLAC,
		TimebaseNum: 1, TimebaseDen: 1000,
		Private: codec.EncodeAudioPrivate(codec.AudioParams{SampleRate: 48000, Channels: 2}, []byte{0xF1}),
	})
	text, _ := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindData, Codec: codec.CodecTextUTF8,
		TimebaseNum: 1, TimebaseDen: 1000,
		Private: codec.EncodeTextPrivate(nil),
	})
	w.SetStartTime(time.Unix(978307200, 0).UTC()) // = mkv epoch -> DateUTC 0
	w.SetTag("TITLE", []byte("demo"))
	for i, fr := range []gmc.Frame{
		{PTS: 0, Keyframe: true, Data: []byte("v-kf0")},
		{PTS: 40, Data: []byte("v-p1")},
		{PTS: 80, Keyframe: true, Data: []byte("v-kf2")},
		{PTS: 120, Data: []byte("v-p3")},
	} {
		if err := w.WriteFrame(video, fr); err != nil {
			t.Fatalf("video %d: %v", i, err)
		}
	}
	for ts := uint64(0); ts < 160; ts += 20 {
		if err := w.WriteFrame(audio, gmc.Frame{PTS: ts, Keyframe: true, Data: []byte{byte(ts)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.WriteFrame(text, gmc.Frame{PTS: 10, Keyframe: true, Data: codec.EncodeTextFrame(30, "sub0")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path
}

func demuxAll(t *testing.T, path string) (*Demuxer, []*Packet) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fi, _ := f.Stat()
	d, err := NewDemuxer(f, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	var pkts []*Packet
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
	}
	return d, pkts
}

func TestExportFull(t *testing.T) {
	src := buildTestGMC(t)
	dst := filepath.Join(t.TempDir(), "out.mkv")
	res, err := Export(src, dst, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tracks != 3 || res.Frames != 4+8+1 {
		t.Fatalf("result = %+v", res)
	}
	d, pkts := demuxAll(t, dst)
	if d.Info().TimestampScale != 1000000 || !d.Info().HasDate || d.Info().DateUTC != 0 {
		t.Fatalf("info = %+v", d.Info())
	}
	trs := d.Tracks()
	if len(trs) != 3 || trs[0].CodecID != codec.CodecAVC || trs[0].PixelWidth != 640 ||
		trs[1].SamplingFrequency != 48000 || !bytes.Equal(trs[1].CodecPrivate, []byte{0xF1}) {
		t.Fatalf("tracks = %+v", trs)
	}
	if d.Tags()["TITLE"] != "demo" {
		t.Fatalf("tags = %v", d.Tags())
	}
	// 1ms timebase == 1ms scale: timestamps preserved exactly
	var vts []int64
	var sub *Packet
	for _, p := range pkts {
		if p.Track == trs[0].Number {
			vts = append(vts, p.Timestamp)
		}
		if p.Track == trs[2].Number {
			sub = p
		}
	}
	if len(vts) != 4 || vts[0] != 0 || vts[3] != 120 {
		t.Fatalf("video ts = %v", vts)
	}
	if sub == nil || sub.Timestamp != 10 || sub.Duration != 30 || string(sub.Data) != "sub0" {
		t.Fatalf("subtitle = %+v", sub)
	}
}

func TestExportAllTracksSkipped(t *testing.T) {
	src := filepath.Join(t.TempDir(), "skip.gmc")
	w, err := gmc.Create(src, gmc.CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, _ := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindVideo, Codec: "V_VP9",
		TimebaseNum: 1, TimebaseDen: 1000,
	})
	if err := w.WriteFrame(id, gmc.Frame{PTS: 0, Keyframe: true, Data: []byte("x")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(t.TempDir(), "skip.mkv")
	res, err := Export(src, dst, ExportOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tracks != 0 || res.Frames != 0 || len(res.SkippedTracks) != 1 {
		t.Fatalf("result = %+v", res)
	}
	d, pkts := demuxAll(t, dst)
	if len(d.Tracks()) != 0 || len(pkts) != 0 {
		t.Fatalf("tracks = %d, packets = %d", len(d.Tracks()), len(pkts))
	}
}

func TestExportRange(t *testing.T) {
	src := buildTestGMC(t)
	dst := filepath.Join(t.TempDir(), "range.mkv")
	// [50ms, 100ms): video snap kf@0 (last sync <= 50), end: kf@80 >= 100? no ->
	// no video end; audio exact cut [50..100) -> 60, 80
	_, err := Export(src, dst, ExportOptions{Range: Range{From: 50 * time.Millisecond, To: 100 * time.Millisecond}})
	if err != nil {
		t.Fatal(err)
	}
	d, pkts := demuxAll(t, dst)
	trs := d.Tracks()
	var vts, ats []int64
	for _, p := range pkts {
		switch p.Track {
		case trs[0].Number:
			vts = append(vts, p.Timestamp)
		case trs[1].Number:
			ats = append(ats, p.Timestamp)
		}
	}
	if len(vts) != 4 || vts[0] != 0 { // whole GOP span (no keyframe >= 100)
		t.Fatalf("video ts = %v", vts)
	}
	if len(ats) != 2 || ats[0] != 60 || ats[1] != 80 {
		t.Fatalf("audio ts = %v", ats)
	}
}

func TestExportTrackSelection(t *testing.T) {
	src := buildTestGMC(t)

	r, err := gmc.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	var videoID, textID gmc.TrackID
	for _, info := range r.Tracks() {
		switch info.Kind {
		case gmc.KindVideo:
			videoID = info.ID
		case gmc.KindData:
			textID = info.ID
		}
	}
	r.Close()

	dst := filepath.Join(t.TempDir(), "sel.mkv")
	res, err := Export(src, dst, ExportOptions{Tracks: []gmc.TrackID{videoID, textID}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tracks != 2 || len(res.SkippedTracks) != 0 {
		t.Fatalf("result = %+v", res)
	}
	d, pkts := demuxAll(t, dst)
	trs := d.Tracks()
	if len(trs) != 2 || trs[0].CodecID != codec.CodecAVC || trs[1].CodecID != codec.CodecTextUTF8 {
		t.Fatalf("tracks = %+v", trs)
	}
	var videoCount, subCount int
	for _, p := range pkts {
		switch p.Track {
		case trs[0].Number:
			videoCount++
		case trs[1].Number:
			subCount++
		}
	}
	if videoCount != 4 || subCount != 1 || len(pkts) != 5 {
		t.Fatalf("packet counts: video=%d sub=%d total=%d", videoCount, subCount, len(pkts))
	}

	_, err = Export(src, filepath.Join(t.TempDir(), "bad.mkv"), ExportOptions{Tracks: []gmc.TrackID{99}})
	if err == nil || !strings.Contains(err.Error(), "unknown track") {
		t.Fatalf("expected unknown track error, got %v", err)
	}
}
