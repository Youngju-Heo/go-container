package gmc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildUnfinalized writes frames without Finalize and returns the raw bytes.
// cpBytes controls checkpoint density (1<<30 means no checkpoint — the chunk
// layout becomes deterministic as TrackInfo + Data*20, so truncation test
// expectations don't shift).
func buildUnfinalized(t *testing.T, cpBytes int64) (data []byte, video TrackID, lastCommitted int64, streamStart int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crash.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: cpBytes, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	video, _ = w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	for i := 0; i < 20; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 50)})
	}
	lastCommitted = w.committed.Load()
	streamStart = w.streamStart
	if err := w.Close(); err != nil { // Close without Finalize
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, video, lastCommitted, streamStart
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "case.gmc")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func countFrames(t *testing.T, r *Reader) int {
	t.Helper()
	n := 0
	off := r.streamStart
	for {
		typ, _, next, err := readChunkAt(r.f, off, r.committed.Load())
		if err != nil {
			return n
		}
		if typ == chunkData {
			n++
		}
		off = next
	}
}

func TestOpenUnfinalizedFull(t *testing.T) {
	data, video, lastCommitted, _ := buildUnfinalized(t, 200) // includes several checkpoints
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() != lastCommitted {
		t.Fatalf("committed = %d, want %d", r.committed.Load(), lastCommitted)
	}
	if len(r.Tracks()) != 1 || r.Tracks()[0].ID != video {
		t.Fatalf("tracks = %+v", r.Tracks())
	}
	// keyframes at pts 0,15000,30000,45000 must be seekable (checkpoint + tail)
	if _, ok := r.idx.seek(video, 57000); !ok {
		t.Fatal("sync point missing from recovered index")
	}
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames = %d, want 20", got)
	}
}

func TestOpenTornTail(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30) // no checkpoint -> last chunk is always Data
	// cut mid-chunk: drop 3 bytes from the tail
	r, err := Open(writeTemp(t, data[:len(data)-3]))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19 (last torn chunk dropped)", got)
	}
}

func TestOpenCorruptMidChunkStopsThere(t *testing.T) {
	data, _, lastCommitted, streamStart := buildUnfinalized(t, 1<<30)
	// corrupt one byte inside the last chunk's payload
	data[lastCommitted-10] ^= 0xFF
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() >= lastCommitted || r.committed.Load() <= streamStart {
		t.Fatalf("committed = %d, want < %d", r.committed.Load(), lastCommitted)
	}
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19", got)
	}
}

func TestOpenSkipsUnknownChunkType(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30)
	unknown := appendChunk(nil, 0x7F, []byte("future extension"))
	r, err := Open(writeTemp(t, append(data, unknown...)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames = %d, want 20 (unknown chunk skipped)", got)
	}
}

func TestOpenEmptyStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	streamStart := w.streamStart
	w.Close()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() != streamStart || len(r.Tracks()) != 0 {
		t.Fatalf("committed=%d tracks=%d", r.committed.Load(), len(r.Tracks()))
	}
}
