package gmc

import (
	"errors"
	"os"
	"testing"
)

func TestFinalizeWritesFooterAndTrailer(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})
	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf")})
	w.WriteFrame(audio, Frame{PTS: 0, Keyframe: true, Data: []byte("a0")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})

	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(); !errors.Is(err, ErrClosed) {
		t.Fatalf("double finalize: err = %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	size := fi.Size()

	tb := make([]byte, trailerSize)
	if _, err := f.ReadAt(tb, size-trailerSize); err != nil {
		t.Fatal(err)
	}
	footerOff, ok := decodeTrailer(tb)
	if !ok {
		t.Fatal("invalid trailer")
	}
	typ, payload, next, err := readChunkAt(f, footerOff, size-trailerSize)
	if err != nil || typ != chunkFooter || next != size-trailerSize {
		t.Fatalf("footer chunk: typ=%d next=%d err=%v", typ, next, err)
	}
	tracks, sums, entries, err := decodeFooter(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].ID != video || tracks[1].Codec != "flac" {
		t.Fatalf("tracks = %+v", tracks)
	}
	if len(sums) != 2 || sums[0].frames != 2 || sums[0].lastPTS != 3000 || sums[1].frames != 1 {
		t.Fatalf("sums = %+v", sums)
	}
	if len(entries) != 2 { // video kf + audio first sync
		t.Fatalf("entries = %+v", entries)
	}
}
