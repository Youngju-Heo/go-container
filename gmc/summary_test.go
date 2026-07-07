package gmc

import (
	"path/filepath"
	"testing"
)

func TestReaderSummariesFinalized(t *testing.T) {
	if Version != 1 {
		t.Fatalf("Version = %d, want 1", Version)
	}

	path := filepath.Join(t.TempDir(), "sum.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "V_TEST", TimebaseNum: 1, TimebaseDen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// pts 0,10,20: two keyframes (0,20), one non-keyframe (10).
	for i, pts := range []uint64{0, 10, 20} {
		if err := w.WriteFrame(id, Frame{PTS: pts, Keyframe: i%2 == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	sums, sync := r.Summaries()
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want 1", len(sums))
	}
	s := sums[0]
	if s.Track != id || s.FirstPTS != 0 || s.LastPTS != 20 || s.Frames != 3 {
		t.Fatalf("summary = %+v", s)
	}
	if sync < 1 {
		t.Fatalf("syncPoints = %d, want >=1", sync)
	}
}

func TestReaderSummariesNonFinalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "A_TEST", TimebaseNum: 1, TimebaseDen: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(id, Frame{PTS: 0, Keyframe: true, Data: []byte{0}}); err != nil {
		t.Fatal(err)
	}
	// Close without Finalize: no footer, recovered by scan.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Finalized() {
		t.Fatal("expected non-finalized")
	}
	sums, _ := r.Summaries()
	if sums != nil {
		t.Fatalf("summaries = %+v, want nil for non-finalized", sums)
	}
}
