package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"sort"
)

// Well-known tag keys. The "gmc." prefix is reserved.
const (
	TagStartTime = "gmc.start_time_unix_ns"
	TagLocation  = "gmc.location"
)

// encodeTagsSlot serializes a tags snapshot:
//
//	seq(8) entryCount(2) { keyLen(2) key valLen(4) val }* crc(4)
//
// Keys are sorted for deterministic output. seq must be >= 1.
func encodeTagsSlot(seq uint64, tags map[string][]byte) []byte {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := binary.LittleEndian.AppendUint64(nil, seq)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(keys)))
	for _, k := range keys {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(k)))
		b = append(b, k...)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(tags[k])))
		b = append(b, tags[k]...)
	}
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// decodeTagsSlot parses one slot. Returns ok=false for zero-filled, torn or
// otherwise invalid slots (seq 0 is reserved as invalid).
func decodeTagsSlot(b []byte) (uint64, map[string][]byte, bool) {
	c := &cursor{b: b}
	seq := c.u64()
	n := int(c.u16())
	tags := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		klen := int(c.u16())
		k := string(c.bytes(klen))
		vlen := int(c.u32())
		v := c.bytes(vlen)
		if c.bad {
			return 0, nil, false
		}
		tags[k] = append([]byte(nil), v...)
	}
	body := c.pos
	crc := c.u32()
	if c.bad || seq == 0 {
		return 0, nil, false
	}
	if crc32.Checksum(b[:body], castagnoli) != crc {
		return 0, nil, false
	}
	return seq, tags, true
}

// pickTagsSlot inspects both slots of the tags area and returns the adopted
// snapshot (nil if none valid), its seq, and the slot index to write next.
func pickTagsSlot(area []byte) (map[string][]byte, uint64, int) {
	slot := len(area) / 2
	seqA, tagsA, okA := decodeTagsSlot(area[:slot])
	seqB, tagsB, okB := decodeTagsSlot(area[slot:])
	switch {
	case okA && (!okB || seqA >= seqB):
		return tagsA, seqA, 1
	case okB:
		return tagsB, seqB, 0
	default:
		return nil, 0, 0
	}
}
