package gmc

import (
	"os"
	"testing"
	"time"
)

func TestLastPTSAndLastTime(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	start := time.Unix(1_700_000_000, 0).UTC()
	if err := w.SetStartTime(start); err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Reordered: true})
	empty, _ := w.AddTrack(TrackInfo{Kind: KindData, Codec: "json", TimebaseNum: 1, TimebaseDen: 1000})
	writeReorderedGOP(t, w, video, 1) // max pts 9000 = 0.1s

	// live reader
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	if pts, ok := r.LastPTS(video); !ok || pts != 9000 {
		t.Fatalf("live LastPTS = %d ok=%v", pts, ok)
	}
	if _, ok := r.LastPTS(empty); ok {
		t.Fatal("empty track must report ok=false")
	}
	if _, ok := r.LastPTS(TrackID(99)); ok {
		t.Fatal("unknown track must report ok=false")
	}
	wantTime := start.Add(100 * time.Millisecond)
	if got, ok := r.LastTime(video); !ok || !got.Equal(wantTime) {
		t.Fatalf("live LastTime = %v ok=%v, want %v", got, ok, wantTime)
	}
	r.Close()

	// finalized file
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if pts, ok := r2.LastPTS(video); !ok || pts != 9000 {
		t.Fatalf("footer LastPTS = %d ok=%v", pts, ok)
	}
	if got, ok := r2.LastTime(video); !ok || !got.Equal(wantTime) {
		t.Fatalf("footer LastTime = %v ok=%v", got, ok)
	}
	r2.Close()

	// crashed file (scan path)
	fi, _ := os.Stat(path)
	if err := os.Truncate(path, fi.Size()-trailerSize); err != nil {
		t.Fatal(err)
	}
	r3, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Close()
	if pts, ok := r3.LastPTS(video); !ok || pts != 9000 {
		t.Fatalf("scan LastPTS = %d ok=%v", pts, ok)
	}
}

func TestLastTimeWithoutStartTime(t *testing.T) {
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
	if _, ok := r.LastTime(video); ok {
		t.Fatal("LastTime without start time must report ok=false")
	}
	w.Close()
}
