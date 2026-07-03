package gmc

import (
	"bytes"
	"errors"
	"testing"
)

func TestTrackInfoRoundtrip(t *testing.T) {
	info := TrackInfo{
		ID: 3, Kind: KindAudio, Codec: "pcm_s16le",
		TimebaseNum: 1, TimebaseDen: 48000,
		Private: []byte{0x01, 0x02},
	}
	p := encodeTrackInfo(info)
	// trailing bytes must not confuse the decoder (self-delimiting)
	got, n, err := decodeTrackInfo(append(p, 0xAA, 0xBB))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(p) {
		t.Fatalf("consumed = %d, want %d", n, len(p))
	}
	if got.ID != 3 || got.Kind != KindAudio || got.Codec != "pcm_s16le" ||
		got.TimebaseDen != 48000 || !bytes.Equal(got.Private, info.Private) {
		t.Fatalf("got %+v", got)
	}
}

func TestTrackInfoTruncated(t *testing.T) {
	p := encodeTrackInfo(TrackInfo{ID: 1, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if _, _, err := decodeTrackInfo(p[:len(p)-1]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}

func TestDataPayloadRoundtrip(t *testing.T) {
	body := []byte("frame-bytes")
	p := encodeDataPayload(nil, 5, flagKeyframe, 90000, body)
	if len(p) != dataHeaderSize+len(body) {
		t.Fatalf("len = %d", len(p))
	}
	id, flags, pts, err := decodeDataHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if id != 5 || flags != flagKeyframe || pts != 90000 || !bytes.Equal(p[dataHeaderSize:], body) {
		t.Fatalf("id=%d flags=%d pts=%d", id, flags, pts)
	}
	if _, _, _, err := decodeDataHeader(p[:10]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short header: err = %v", err)
	}
}
