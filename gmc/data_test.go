package gmc

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

func TestDataPayloadWithDTS(t *testing.T) {
	body := []byte("bframe")
	p := encodeDataPayload(nil, 7, flagKeyframe|flagHasDTS, 6000, 3000, body)
	if len(p) != dataHeaderDTSSize+len(body) {
		t.Fatalf("len = %d, want %d", len(p), dataHeaderDTSSize+len(body))
	}
	h, err := decodeDataHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if h.id != 7 || h.flags != flagKeyframe|flagHasDTS || h.pts != 6000 || h.dts != 3000 || h.n != dataHeaderDTSSize {
		t.Fatalf("h = %+v", h)
	}
	if !bytes.Equal(p[h.n:], body) {
		t.Fatal("body mismatch")
	}
	// truncated dts -> corrupt
	if _, err := decodeDataHeader(p[:15]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated dts: err = %v", err)
	}
}

func TestDataPayloadWithoutDTSUnchanged(t *testing.T) {
	// dts argument must be ignored when flagHasDTS is not set (v1 byte layout)
	p := encodeDataPayload(nil, 5, flagKeyframe, 90000, 12345, []byte("x"))
	if len(p) != dataHeaderSize+1 {
		t.Fatalf("len = %d, want %d", len(p), dataHeaderSize+1)
	}
	h, err := decodeDataHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if h.n != dataHeaderSize || h.dts != 0 || h.pts != 90000 {
		t.Fatalf("h = %+v", h)
	}
}

func TestWriteFrameEmitsDTS(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 3000, DTS: 0, HasDTS: true, Keyframe: true, Data: []byte("kf")}); err != nil {
		t.Fatal(err)
	}
	chunks := scanChunks(t, path, w.streamStart, w.committed.Load())
	h, err := decodeDataHeader(chunks[1].payload)
	if err != nil || h.flags&flagHasDTS == 0 || h.pts != 3000 || h.dts != 0 {
		t.Fatalf("h=%+v err=%v", h, err)
	}
	// reader roundtrip restores DTS/HasDTS
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	it, err := r.SeekPTS(video, 3000)
	if err != nil {
		t.Fatal(err)
	}
	if !it.Next() {
		t.Fatal(it.Err())
	}
	fr := it.Frame()
	if !fr.HasDTS || fr.DTS != 0 || fr.PTS != 3000 || !bytes.Equal(fr.Data, []byte("kf")) {
		t.Fatalf("frame = %+v", fr)
	}
	w.Close()
}
