package gmc

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
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

func TestRepairIdempotent(t *testing.T) {
	path, _ := buildCrashFile(t, time.Unix(1700000000, 0))

	res1, err := Repair(path)
	if err != nil || !res1.Repaired {
		t.Fatalf("first repair: res=%+v err=%v", res1, err)
	}
	after1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res2, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Repaired {
		t.Fatal("second repair: Repaired = true, want false (already finalized)")
	}
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after1, after2) {
		t.Fatal("second repair modified the file")
	}
	if res2.Frames != 20 || res2.Size != int64(len(after2)) {
		t.Fatalf("no-op result: Frames=%d Size=%d fileSize=%d", res2.Frames, res2.Size, len(after2))
	}
}

func TestRepairAlreadyFinalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w.SetStartTime(time.Unix(1700000000, 0))
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repaired {
		t.Fatal("Repaired = true on already-finalized file")
	}
	if res.Frames != 2 || len(res.Summaries) != 1 ||
		res.Summaries[0].Frames != 2 || res.Summaries[0].LastPTS != 3000 {
		t.Fatalf("result = %+v", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("repair modified an already-finalized file")
	}
}

func TestRepairZeroFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // crash before any frame
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repaired || res.Frames != 0 {
		t.Fatalf("result = %+v, want Repaired=false Frames=0", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("zero-frame repair modified the file")
	}
	// File must remain a valid, unfinalized GMC (no bogus footer written).
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Finalized() {
		t.Fatal("zero-frame file should remain unfinalized")
	}
}

// writeMixed writes an identical multi-track sequence (video + audio + an
// empty data track) and either Finalizes or Closes (crash) based on finalize.
func writeMixed(t *testing.T, path string, finalize bool) {
	t.Helper()
	w, err := Create(path, CreateOptions{CheckpointBytes: 200, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w.SetStartTime(time.Unix(1700000000, 0))
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})
	w.AddTrack(TrackInfo{Kind: KindData, Codec: "meta", TimebaseNum: 1, TimebaseDen: 1000}) // no frames
	for i := 0; i < 12; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 40)})
	}
	for i := 0; i < 8; i++ {
		w.WriteFrame(audio, Frame{PTS: uint64(i * 1024), Keyframe: true, Data: make([]byte, 20)})
	}
	if finalize {
		if err := w.Finalize(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func footerSummaries(t *testing.T, path string) []TrackSummary {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("expected a finalized file")
	}
	sums, _ := r.Summaries()
	return sums
}

func TestRepairFooterMatchesFinalize(t *testing.T) {
	dir := t.TempDir()
	fin := filepath.Join(dir, "finalized.gmc")
	rep := filepath.Join(dir, "repaired.gmc")
	writeMixed(t, fin, true)
	writeMixed(t, rep, false)

	if _, err := Repair(rep); err != nil {
		t.Fatal(err)
	}

	want := footerSummaries(t, fin)
	got := footerSummaries(t, rep)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("summaries mismatch:\n finalize = %+v\n repair   = %+v", want, got)
	}
}

func TestRepairTornTail(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30) // no checkpoint -> last chunk is Data
	path := writeTemp(t, data[:len(data)-3])    // torn: drop last 3 bytes

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired || res.Frames != 19 {
		t.Fatalf("result = %+v, want Repaired=true Frames=19", res)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("not finalized after repair")
	}
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19 (torn frame dropped)", got)
	}
}
