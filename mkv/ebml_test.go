package mkv

import (
	"bytes"
	"io"
	"math"
	"testing"
)

func TestVintSizeRoundtrip(t *testing.T) {
	cases := []int64{0, 1, 126, 127, 128, 16382, 16383, 1 << 20, 1 << 35}
	for _, v := range cases {
		b := appendVintSize(nil, v)
		got, n := readVintAt(b)
		if int64(got) != v || n != len(b) {
			t.Fatalf("v=%d got=%d n=%d len=%d", v, got, n, len(b))
		}
	}
}

func TestSignedVint(t *testing.T) {
	// EBML lacing delta: signed vint = unsigned - (2^(7*len-1) - 1)
	b := appendVintSize(nil, 63+5) // 1-byte vint storing +5
	got, n := readSignedVintAt(b)
	if got != 5 || n != 1 {
		t.Fatalf("got=%d n=%d", got, n)
	}
	b2 := appendVintSize(nil, 63-5) // -5
	if got, _ := readSignedVintAt(b2); got != -5 {
		t.Fatalf("got=%d", got)
	}
}

func TestElementRoundtrip(t *testing.T) {
	var buf []byte
	buf = appendUintElement(buf, idTimestampScale, 1000000)
	buf = appendStringElement(buf, idDocType, "matroska")
	buf = appendFloatElement(buf, idDuration, 12345.5)
	buf = appendElement(buf, idCodecPrivate, []byte{1, 2, 3})

	er := newEBMLReader(bytes.NewReader(buf), int64(len(buf)))

	id, sz, _, err := er.readElement()
	if err != nil || id != idTimestampScale {
		t.Fatalf("id=%x err=%v", id, err)
	}
	b, _ := er.readBytes(sz)
	if parseUint(b) != 1000000 {
		t.Fatalf("scale = %d", parseUint(b))
	}

	id, sz, _, _ = er.readElement()
	b, _ = er.readBytes(sz)
	if id != idDocType || string(b) != "matroska" {
		t.Fatalf("doctype id=%x %q", id, b)
	}

	id, sz, _, _ = er.readElement()
	b, _ = er.readBytes(sz)
	f, err := parseFloat(b)
	if id != idDuration || err != nil || math.Abs(f-12345.5) > 1e-9 {
		t.Fatalf("duration id=%x f=%v err=%v", id, f, err)
	}

	id, sz, _, _ = er.readElement()
	b, _ = er.readBytes(sz)
	if id != idCodecPrivate || !bytes.Equal(b, []byte{1, 2, 3}) {
		t.Fatalf("private id=%x %x", id, b)
	}

	if _, _, _, err := er.readElement(); err != io.EOF {
		t.Fatalf("eof: err = %v", err)
	}
}

func TestUnknownSizeAndBounds(t *testing.T) {
	// Segment with unknown size: size vint 0x01FFFFFFFFFFFFFF
	buf := appendVintID(nil, idSegment)
	buf = append(buf, 0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF)
	er := newEBMLReader(bytes.NewReader(buf), int64(len(buf)))
	id, _, unknown, err := er.readElement()
	if err != nil || id != idSegment || !unknown {
		t.Fatalf("id=%x unknown=%v err=%v", id, unknown, err)
	}

	// oversize element -> errMalformed
	bad := appendVintID(nil, idCodecPrivate)
	bad = appendVintSize(bad, maxElementSize+1)
	er2 := newEBMLReader(bytes.NewReader(bad), int64(len(bad)))
	if _, _, _, err := er2.readElement(); err != errMalformed {
		t.Fatalf("oversize: err = %v", err)
	}
}

func TestTruncatedSourcePeek(t *testing.T) {
	// Valid element whose size vint spans 2 bytes (payload of 256 bytes).
	buf := appendElement(nil, idCodecPrivate, make([]byte, 256))
	// Underlying data truncated mid-header (id + first size byte only),
	// while the declared size claims the full element is present.
	// peek must not zero-pad the short read into a "valid" header.
	er := newEBMLReader(bytes.NewReader(buf[:3]), int64(len(buf)))
	if _, _, _, err := er.readElement(); err != errMalformed && err != io.EOF {
		t.Fatalf("truncated: err = %v, want errMalformed or io.EOF", err)
	}
}
