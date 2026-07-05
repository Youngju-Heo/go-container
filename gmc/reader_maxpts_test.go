package gmc

import (
	"os"
	"testing"
	"time"
)

func TestReaderMaxPTSFooterAndScan(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Reordered: true})
	writeReorderedGOP(t, w, video, 1) // pts 순서 0,9000,3000,6000 — max 9000
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}

	// footer path
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := r.maxPTS[video]; !ok || got != 9000 {
		t.Fatalf("footer maxPTS = %d ok=%v, want 9000", got, ok)
	}
	r.Close()

	// scan path: truncate the trailer to force recovery
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(path, fi.Size()-trailerSize); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Close()
	if got, ok := r2.maxPTS[video]; !ok || got != 9000 {
		t.Fatalf("scan maxPTS = %d ok=%v, want 9000", got, ok)
	}
	// empty-track semantics: unknown track has no entry
	if _, ok := r2.maxPTS[TrackID(99)]; ok {
		t.Fatal("unexpected entry for unknown track")
	}
}
