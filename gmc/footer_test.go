package gmc

import (
	"testing"
)

func TestCheckpointRoundtrip(t *testing.T) {
	in := []cpEntry{{1, 0, 100}, {2, 4800, 220}, {1, 90000, 5000}}
	p := encodeCheckpoint(64, in)
	prev, got, err := decodeCheckpoint(p)
	if err != nil {
		t.Fatal(err)
	}
	if prev != 64 || len(got) != 3 || got[2] != (cpEntry{1, 90000, 5000}) {
		t.Fatalf("prev=%d got=%v", prev, got)
	}
	if _, _, err := decodeCheckpoint(p[:len(p)-1]); err == nil {
		t.Fatal("truncated checkpoint accepted")
	}
}

func TestFooterRoundtrip(t *testing.T) {
	tracks := []TrackInfo{
		{ID: 1, Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Private: []byte{1}},
		{ID: 2, Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000},
	}
	sums := []trackSummary{{1, 0, 180000, 61}, {2, 0, 96000, 100}}
	entries := []cpEntry{{1, 0, 100}, {1, 90000, 9000}}
	p := encodeFooter(tracks, sums, entries)
	gt, gs, ge, err := decodeFooter(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(gt) != 2 || gt[0].Codec != "h264" || gt[1].TimebaseDen != 48000 {
		t.Fatalf("tracks = %+v", gt)
	}
	if len(gs) != 2 || gs[1] != (trackSummary{2, 0, 96000, 100}) {
		t.Fatalf("sums = %+v", gs)
	}
	if len(ge) != 2 || ge[1] != (cpEntry{1, 90000, 9000}) {
		t.Fatalf("entries = %+v", ge)
	}
	if _, _, _, err := decodeFooter(p[:len(p)-3]); err == nil {
		t.Fatal("truncated footer accepted")
	}
}

func TestTrailerRoundtrip(t *testing.T) {
	b := encodeTrailer(123456)
	if len(b) != trailerSize {
		t.Fatalf("len = %d", len(b))
	}
	off, ok := decodeTrailer(b)
	if !ok || off != 123456 {
		t.Fatalf("off=%d ok=%v", off, ok)
	}
	bad := append([]byte(nil), b...)
	bad[3] ^= 0xFF
	if _, ok := decodeTrailer(bad); ok {
		t.Fatal("corrupt trailer accepted")
	}
	if _, ok := decodeTrailer(make([]byte, trailerSize)); ok {
		t.Fatal("zero trailer accepted")
	}
}
