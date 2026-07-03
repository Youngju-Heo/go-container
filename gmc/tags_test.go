package gmc

import (
	"bytes"
	"testing"
)

func sampleTags() map[string][]byte {
	return map[string][]byte{
		TagLocation: []byte("37.5665,126.9780"),
		"camera.id": []byte("cam-03"),
	}
}

func TestTagsSlotRoundtrip(t *testing.T) {
	buf := encodeTagsSlot(7, sampleTags())
	seq, tags, ok := decodeTagsSlot(buf)
	if !ok || seq != 7 {
		t.Fatalf("ok=%v seq=%d", ok, seq)
	}
	if !bytes.Equal(tags["camera.id"], []byte("cam-03")) || len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	// deterministic encoding (sorted keys)
	if !bytes.Equal(buf, encodeTagsSlot(7, sampleTags())) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestTagsSlotRejectsCorruptionAndZeroFill(t *testing.T) {
	buf := encodeTagsSlot(1, sampleTags())
	buf[9] ^= 0xFF
	if _, _, ok := decodeTagsSlot(buf); ok {
		t.Fatal("corrupt slot accepted")
	}
	if _, _, ok := decodeTagsSlot(make([]byte, 4096)); ok {
		t.Fatal("zero-filled slot accepted")
	}
}

func TestPickTagsSlot(t *testing.T) {
	const slot = 4096
	area := make([]byte, 2*slot)

	// both invalid (fresh file) -> no tags, write slot 0 next
	tags, seq, next := pickTagsSlot(area)
	if tags != nil || seq != 0 || next != 0 {
		t.Fatalf("fresh: tags=%v seq=%d next=%d", tags, seq, next)
	}

	// slot A valid seq=1 -> adopt A, write slot 1 next
	copy(area[0:], encodeTagsSlot(1, map[string][]byte{"k": []byte("v1")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 1 || next != 1 || !bytes.Equal(tags["k"], []byte("v1")) {
		t.Fatalf("A only: seq=%d next=%d tags=%v", seq, next, tags)
	}

	// slot B valid seq=2 -> adopt B, write slot 0 next
	copy(area[slot:], encodeTagsSlot(2, map[string][]byte{"k": []byte("v2")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 2 || next != 0 || !bytes.Equal(tags["k"], []byte("v2")) {
		t.Fatalf("B newer: seq=%d next=%d tags=%v", seq, next, tags)
	}

	// torn write on B (seq=4 partially written) -> fall back to A
	torn := encodeTagsSlot(4, map[string][]byte{"k": []byte("v4")})
	copy(area[slot:], torn[:len(torn)-2])
	area[slot+len(torn)-1] = 0
	copy(area[0:], encodeTagsSlot(3, map[string][]byte{"k": []byte("v3")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 3 || next != 1 || !bytes.Equal(tags["k"], []byte("v3")) {
		t.Fatalf("torn B: seq=%d next=%d tags=%v", seq, next, tags)
	}
}
