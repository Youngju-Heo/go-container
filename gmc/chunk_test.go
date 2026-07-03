package gmc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestChunkRoundtrip(t *testing.T) {
	payload := []byte("hello payload")
	buf := appendChunk(nil, chunkData, payload)
	if len(buf) != chunkFramingSize+len(payload) {
		t.Fatalf("frame size = %d, want %d", len(buf), chunkFramingSize+len(payload))
	}
	typ, got, next, err := readChunkAt(bytes.NewReader(buf), 0, int64(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	if typ != chunkData || !bytes.Equal(got, payload) {
		t.Fatalf("typ=%d payload=%q", typ, got)
	}
	if next != int64(len(buf)) {
		t.Fatalf("next = %d, want %d", next, len(buf))
	}
}

func TestChunkCleanEOF(t *testing.T) {
	buf := appendChunk(nil, chunkData, []byte("x"))
	if _, _, _, err := readChunkAt(bytes.NewReader(buf), int64(len(buf)), int64(len(buf))); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestChunkCorruption(t *testing.T) {
	payload := []byte("payload-bytes")
	base := appendChunk(nil, chunkData, payload)

	flip := append([]byte(nil), base...)
	flip[7] ^= 0xFF // corrupt payload byte -> CRC mismatch
	if _, _, _, err := readChunkAt(bytes.NewReader(flip), 0, int64(len(flip))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("crc mismatch: err = %v", err)
	}

	trunc := base[:len(base)-3] // torn tail
	if _, _, _, err := readChunkAt(bytes.NewReader(trunc), 0, int64(len(trunc))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: err = %v", err)
	}

	huge := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(huge[0:4], maxPayloadLen+1) // oversize length
	if _, _, _, err := readChunkAt(bytes.NewReader(huge), 0, int64(len(huge))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversize: err = %v", err)
	}
}
