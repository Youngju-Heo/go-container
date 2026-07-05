package gmc

import (
	"errors"
	"testing"
	"time"
)

func TestSeekTime(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	start := time.Unix(1_700_000_000, 0).UTC()
	if err := w.SetStartTime(start); err != nil {
		t.Fatal(err)
	}
	// 90 kHz video: keyframe per 10 frames, pts step 3000 (30 frames = 1s)
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	writeGOPs(t, w, video, 30)

	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// t = start+0.5s -> pts 45000 -> starts at keyframe pts 30000
	it, err := r.SeekTime(start.Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if !it.Next() {
		t.Fatal(it.Err())
	}
	if got := it.Frame().PTS; got != 30000 {
		t.Fatalf("first pts = %d, want 30000", got)
	}

	// before start clamps to stream beginning
	it2, err := r.SeekTime(start.Add(-time.Hour), video)
	if err != nil {
		t.Fatal(err)
	}
	if !it2.Next() || it2.Frame().PTS != 0 {
		t.Fatalf("clamp: pts=%d err=%v", it2.Frame().PTS, it2.Err())
	}

	// unknown track
	if _, err := r.SeekTime(start, TrackID(99)); !errors.Is(err, ErrUnknownTrack) {
		t.Fatalf("unknown track: err = %v", err)
	}
	w.Close()
}

func TestSeekTimeRequiresStartTime(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err := w.WriteFrame(video, Frame{PTS: 0, Keyframe: true}); err != nil {
		t.Fatal(err)
	}
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if _, err := r.SeekTime(time.Now()); !errors.Is(err, ErrNoStartTime) {
		t.Fatalf("err = %v, want ErrNoStartTime", err)
	}
	w.Close()
}
