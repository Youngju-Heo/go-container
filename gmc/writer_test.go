package gmc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scanChunks reads every chunk of the file for test verification.
type rawChunk struct {
	typ     byte
	payload []byte
	off     int64
}

func scanChunks(t *testing.T, path string, start, limit int64) []rawChunk {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []rawChunk
	off := start
	for {
		typ, payload, next, err := readChunkAt(f, off, limit)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("scan at %d: %v", off, err)
		}
		out = append(out, rawChunk{typ, payload, off})
		off = next
	}
}

func newTestWriter(t *testing.T, opts CreateOptions) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.gmc")
	w, err := Create(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	return w, path
}

func TestCreateWritesHeaderAndTagsArea(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{Private: []byte("manifest"), TagsAreaSize: 1024})
	defer w.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, hlen, err := decodeFileHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hdr.private, []byte("manifest")) || hdr.tagsAreaLen != 1024 {
		t.Fatalf("hdr = %+v", hdr)
	}
	if w.streamStart != hlen+1024 || w.committed.Load() != w.streamStart {
		t.Fatalf("streamStart=%d committed=%d", w.streamStart, w.committed.Load())
	}
}

func TestCreateRefusesExistingFile(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	defer w.Close()
	if _, err := Create(path, CreateOptions{}); err == nil {
		t.Fatal("duplicate Create must fail")
	}
}

func TestWriteFrameAppendsChunks(t *testing.T) {
	// Keep checkpoint triggers effectively disabled so chunk counts stay predictable.
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf0")}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p1")}); err != nil {
		t.Fatal(err)
	}

	chunks := scanChunks(t, path, w.streamStart, w.committed.Load())
	if len(chunks) != 3 || chunks[0].typ != chunkTrackInfo || chunks[1].typ != chunkData {
		t.Fatalf("chunks = %+v", chunks)
	}
	h, err := decodeDataHeader(chunks[1].payload)
	if err != nil || h.id != video || h.flags != flagKeyframe || h.pts != 0 {
		t.Fatalf("frame0: h=%+v err=%v", h, err)
	}
	if !bytes.Equal(chunks[2].payload[dataHeaderSize:], []byte("p1")) {
		t.Fatal("frame1 body mismatch")
	}
	// keyframe indexed at its chunk offset
	off, ok := w.idx.seek(video, 0)
	if !ok || off != chunks[1].off {
		t.Fatalf("index off=%d ok=%v want %d", off, ok, chunks[1].off)
	}
	w.Close()
}

func TestWriteFrameValidation(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})

	if err := w.WriteFrame(99, Frame{}); !errors.Is(err, ErrUnknownTrack) {
		t.Fatalf("unknown track: err = %v", err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 100}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 99}); !errors.Is(err, ErrNonMonotonicPTS) {
		t.Fatalf("pts regression: err = %v", err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 100}); err != nil {
		t.Fatalf("equal pts must be allowed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 200}); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: err = %v", err)
	}
	if err := w.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("double close: err = %v", err)
	}
}
