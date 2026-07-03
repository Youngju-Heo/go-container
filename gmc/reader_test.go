package gmc

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// buildFinalizedFile creates a finalized two-track file and returns its path.
func buildFinalizedFile(t *testing.T) (string, TrackID, TrackID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "done.gmc")
	w, err := Create(path, CreateOptions{Private: []byte("manifest")})
	if err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})
	w.SetStartTime(time.Unix(1751500000, 0))
	w.SetTag(TagLocation, []byte("seoul"))
	for i := 0; i < 30; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)}})
		w.WriteFrame(audio, Frame{PTS: uint64(i * 1600), Keyframe: true, Data: []byte{0xA0, byte(i)}})
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path, video, audio
}

func TestOpenFinalized(t *testing.T) {
	path, video, audio := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if r.w != nil {
		t.Fatal("opened reader must not be live")
	}
	tracks := r.Tracks()
	if len(tracks) != 2 || tracks[0].ID != video || tracks[1].ID != audio || tracks[1].Codec != "pcm_s16le" {
		t.Fatalf("tracks = %+v", tracks)
	}
	if !bytes.Equal(r.FilePrivate(), []byte("manifest")) {
		t.Fatalf("private = %q", r.FilePrivate())
	}
	if !bytes.Equal(r.Tags()[TagLocation], []byte("seoul")) {
		t.Fatalf("tags = %v", r.Tags())
	}
	start, ok := r.StartTime()
	if !ok || start.Unix() != 1751500000 {
		t.Fatalf("start = %v ok=%v", start, ok)
	}
	// index loaded from footer: video keyframes at pts 0, 30000, 60000
	if _, ok := r.idx.seek(video, 30000); !ok {
		t.Fatal("footer index missing video keyframe")
	}
	if r.committed.Load() <= r.streamStart {
		t.Fatalf("committed = %d", r.committed.Load())
	}
}
