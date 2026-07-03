package gmc

import (
	"bytes"
	"errors"
	"testing"
)

func TestFileHeaderRoundtrip(t *testing.T) {
	h := fileHeader{tagsAreaLen: 8192, private: []byte("private-data")}
	buf := encodeFileHeader(h)
	got, hlen, err := decodeFileHeader(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if hlen != int64(len(buf)) {
		t.Fatalf("headerLen = %d, want %d", hlen, len(buf))
	}
	if got.tagsAreaLen != 8192 || !bytes.Equal(got.private, h.private) {
		t.Fatalf("got %+v", got)
	}
}

func TestFileHeaderEmptyPrivate(t *testing.T) {
	buf := encodeFileHeader(fileHeader{tagsAreaLen: 1024})
	got, hlen, err := decodeFileHeader(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if hlen != headerFixedSize || len(got.private) != 0 {
		t.Fatalf("hlen=%d private=%q", hlen, got.private)
	}
}

func TestFileHeaderCorruption(t *testing.T) {
	buf := encodeFileHeader(fileHeader{tagsAreaLen: 1024, private: []byte("p")})

	bad := append([]byte(nil), buf...)
	bad[0] = 'X' // bad magic
	if _, _, err := decodeFileHeader(bytes.NewReader(bad)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("magic: err = %v", err)
	}

	bad = append([]byte(nil), buf...)
	bad[9] ^= 0xFF // corrupt tagsAreaLen -> CRC mismatch
	if _, _, err := decodeFileHeader(bytes.NewReader(bad)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("crc: err = %v", err)
	}
}
