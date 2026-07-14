package gmc

import (
	"testing"
)

func TestScanAggregatesFirstPTSAndFrames(t *testing.T) {
	// buildUnfinalized writes 20 video frames at pts 0,3000,...,57000 (no Finalize).
	data, video, _, _ := buildUnfinalized(t, 200)
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.frames[video] != 20 {
		t.Fatalf("frames[video] = %d, want 20", r.frames[video])
	}
	if r.firstPTS[video] != 0 {
		t.Fatalf("firstPTS[video] = %d, want 0", r.firstPTS[video])
	}
	if r.maxPTS[video] != 57000 {
		t.Fatalf("maxPTS[video] = %d, want 57000", r.maxPTS[video])
	}
}
