package gmc

import (
	"path/filepath"
	"testing"
	"time"
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

// buildCrashFile writes 20 video frames (pts 0..57000) plus a start-time tag
// and Closes WITHOUT Finalize, simulating a crash. Returns the on-disk path.
func buildCrashFile(t *testing.T, start time.Time) (path string, video TrackID) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "crash.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: 200, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetStartTime(start); err != nil {
		t.Fatal(err)
	}
	video, _ = w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	for i := 0; i < 20; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 50)})
	}
	if err := w.Close(); err != nil { // crash: Close without Finalize
		t.Fatal(err)
	}
	return path, video
}

func TestRepairFinalizesCrashFile(t *testing.T) {
	start := time.Unix(1700000000, 0)
	path, video := buildCrashFile(t, start)

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired {
		t.Fatal("Repaired = false, want true")
	}
	if res.Frames != 20 {
		t.Fatalf("Frames = %d, want 20", res.Frames)
	}
	if len(res.Summaries) != 1 || res.Summaries[0].FirstPTS != 0 ||
		res.Summaries[0].LastPTS != 57000 || res.Summaries[0].Frames != 20 {
		t.Fatalf("Summaries = %+v", res.Summaries)
	}
	if !res.StartTime.Equal(start) {
		t.Fatalf("StartTime = %v, want %v", res.StartTime, start)
	}
	wantLast := start.Add(time.Duration(ptsToNano(57000, res.Tracks[0])))
	if !res.LastTime.Equal(wantLast) {
		t.Fatalf("LastTime = %v, want %v", res.LastTime, wantLast)
	}

	// Reopen: must be finalized and fully readable.
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("reopened file not finalized")
	}
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames after repair = %d, want 20", got)
	}
	if _, ok := r.idx.seek(video, 57000); !ok {
		t.Fatal("sync point missing after repair")
	}
}
