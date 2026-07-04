package gmc

import (
	"errors"
	"os"
	"testing"
	"time"
)

// IBBP-style GOP: decode order with non-monotonic pts (B-frames).
// pts step 3000, GOP: I(0) P(9000) B(3000) B(6000) | I(12000) ...
func writeReorderedGOP(t *testing.T, w *Writer, id TrackID, gops int) {
	t.Helper()
	for g := 0; g < gops; g++ {
		base := uint64(g * 12000)
		frames := []struct {
			pts uint64
			key bool
		}{
			{base, true}, {base + 9000, false}, {base + 3000, false}, {base + 6000, false},
		}
		for i, fr := range frames {
			if err := w.WriteFrame(id, Frame{PTS: fr.pts, Keyframe: fr.key, Data: []byte{byte(g), byte(i)}}); err != nil {
				t.Fatalf("gop %d frame %d: %v", g, i, err)
			}
		}
	}
}

func TestReorderedTrackAcceptsBFrames(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Reordered: true})
	if err != nil {
		t.Fatal(err)
	}
	writeReorderedGOP(t, w, video, 3)

	// keyframe pts regression is still rejected
	if err := w.WriteFrame(video, Frame{PTS: 11000, Keyframe: true}); !errors.Is(err, ErrNonMonotonicPTS) {
		t.Fatalf("keyframe regression: err = %v", err)
	}
	// maxPTS tracks the highest pts, not the last written one
	if got := w.tracks[video].maxPTS; got != 2*12000+9000 {
		t.Fatalf("maxPTS = %d, want %d", got, 2*12000+9000)
	}

	// seek to pts 15000 starts at GOP-1 keyframe (pts 12000) and covers the GOP
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	it, err := r.SeekPTS(video, 15000)
	if err != nil {
		t.Fatal(err)
	}
	var ptss []uint64
	for it.Next() {
		ptss = append(ptss, it.Frame().PTS)
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}
	if len(ptss) != 8 || ptss[0] != 12000 || ptss[1] != 21000 {
		t.Fatalf("ptss = %v", ptss)
	}
	w.Close()
}

func TestDefaultTrackStillStrict(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err := w.WriteFrame(video, Frame{PTS: 100, Keyframe: true}); err != nil {
		t.Fatal(err)
	}
	// non-monotonic pts without dts is still rejected on a default track
	if err := w.WriteFrame(video, Frame{PTS: 99}); !errors.Is(err, ErrNonMonotonicPTS) {
		t.Fatalf("err = %v", err)
	}
	w.Close()
}

func TestDTSMonotonicityOnDefaultTrack(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	// with explicit DTS, monotonicity is checked on DTS: pts may reorder
	if err := w.WriteFrame(video, Frame{PTS: 3000, DTS: 0, HasDTS: true, Keyframe: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 9000, DTS: 1000, HasDTS: true}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 6000, DTS: 2000, HasDTS: true}); err != nil {
		t.Fatalf("pts reorder with monotonic dts must pass: %v", err)
	}
	// dts regression is rejected
	if err := w.WriteFrame(video, Frame{PTS: 12000, DTS: 1500, HasDTS: true}); !errors.Is(err, ErrNonMonotonicPTS) {
		t.Fatalf("dts regression: err = %v", err)
	}
	w.Close()
}

func TestFinalizeSummaryUsesMaxPTS(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Reordered: true})
	writeReorderedGOP(t, w, video, 1) // last written pts 6000, max 9000
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	var tb [trailerSize]byte
	f.ReadAt(tb[:], fi.Size()-trailerSize)
	footerOff, _ := decodeTrailer(tb[:])
	_, payload, _, err := readChunkAt(f, footerOff, fi.Size()-trailerSize)
	if err != nil {
		t.Fatal(err)
	}
	_, sums, _, err := decodeFooter(payload)
	if err != nil || len(sums) != 1 {
		t.Fatalf("sums=%v err=%v", sums, err)
	}
	if sums[0].lastPTS != 9000 {
		t.Fatalf("summary lastPTS = %d, want maxPTS 9000", sums[0].lastPTS)
	}
}
