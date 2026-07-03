package gmc

import (
	"testing"
	"time"
)

// Byte trigger: with cpBytes=1, a checkpoint is written each time a sync point accumulates.
func TestCheckpointByteTrigger(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1, CheckpointInterval: time.Hour})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})

	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf0")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})
	w.WriteFrame(video, Frame{PTS: 90000, Keyframe: true, Data: []byte("kf1")})

	var cps []rawChunk
	for _, c := range scanChunks(t, path, w.streamStart, w.committed.Load()) {
		if c.typ == chunkCheckpoint {
			cps = append(cps, c)
		}
	}
	if len(cps) != 2 {
		t.Fatalf("checkpoints = %d, want 2", len(cps))
	}
	prev0, e0, err := decodeCheckpoint(cps[0].payload)
	if err != nil || prev0 != 0 || len(e0) != 1 || e0[0].pts != 0 {
		t.Fatalf("cp0: prev=%d entries=%v err=%v", prev0, e0, err)
	}
	prev1, e1, err := decodeCheckpoint(cps[1].payload)
	if err != nil || prev1 != cps[0].off || len(e1) != 1 || e1[0].pts != 90000 {
		t.Fatalf("cp1: prev=%d (want %d) entries=%v err=%v", prev1, cps[0].off, e1, err)
	}
	w.Close()
}

// Audio sampling: only the first sync point per track is indexed within one checkpoint interval.
func TestCheckpointAudioSampling(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})

	for i := 0; i < 10; i++ {
		w.WriteFrame(audio, Frame{PTS: uint64(i * 960), Keyframe: true, Data: make([]byte, 100)})
	}
	if len(w.pending) != 1 || w.pending[0].pts != 0 {
		t.Fatalf("pending = %v, want single entry at pts 0", w.pending)
	}
	w.Close()
}

// After a checkpoint, the sampling flag resets so the next interval's first audio frame is indexed again.
func TestCheckpointResetsSampling(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1, CheckpointInterval: time.Hour})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})

	w.WriteFrame(audio, Frame{PTS: 0, Keyframe: true, Data: []byte("a")})    // indexed + cp
	w.WriteFrame(audio, Frame{PTS: 4096, Keyframe: true, Data: []byte("b")}) // new interval -> indexed
	if _, ok := w.idx.seek(audio, 4096); !ok {
		t.Fatal("second interval sync point not indexed")
	}
	off, _ := w.idx.seek(audio, 4096)
	off0, _ := w.idx.seek(audio, 0)
	if off == off0 {
		t.Fatal("expected two distinct index entries")
	}
	w.Close()
}
