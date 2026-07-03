package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// chunkCRC computes CRC-32C over type byte followed by payload.
func chunkCRC(typ byte, payload []byte) uint32 {
	crc := crc32.Checksum([]byte{typ}, castagnoli)
	return crc32.Update(crc, castagnoli, payload)
}

// appendChunk serializes one chunk: [payloadLen u32][type u8][payload][crc u32].
func appendChunk(dst []byte, typ byte, payload []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, typ)
	dst = append(dst, payload...)
	return binary.LittleEndian.AppendUint32(dst, chunkCRC(typ, payload))
}

// readChunkAt reads and CRC-verifies the chunk at off. limit is the exclusive
// upper bound of readable bytes (committed size or logical EOF).
// Returns io.EOF when off == limit, ErrCorrupt on any framing violation.
func readChunkAt(r io.ReaderAt, off, limit int64) (typ byte, payload []byte, next int64, err error) {
	if off == limit {
		return 0, nil, 0, io.EOF
	}
	if off+chunkHeaderSize > limit {
		return 0, nil, 0, ErrCorrupt
	}
	var hdr [chunkHeaderSize]byte
	if _, err := r.ReadAt(hdr[:], off); err != nil {
		return 0, nil, 0, err
	}
	plen := int64(binary.LittleEndian.Uint32(hdr[0:4]))
	typ = hdr[4]
	if plen > maxPayloadLen || off+chunkFramingSize+plen > limit {
		return 0, nil, 0, ErrCorrupt
	}
	body := make([]byte, plen+4)
	if _, err := r.ReadAt(body, off+chunkHeaderSize); err != nil {
		return 0, nil, 0, err
	}
	payload = body[:plen]
	if chunkCRC(typ, payload) != binary.LittleEndian.Uint32(body[plen:]) {
		return 0, nil, 0, ErrCorrupt
	}
	return typ, payload, off + chunkFramingSize + plen, nil
}
