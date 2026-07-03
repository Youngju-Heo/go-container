package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// fileHeader is the fixed file header at offset 0. Written once at Create,
// immutable afterwards. Layout (little-endian):
//
//	magic(4) version(2) flags(2) tagsAreaLen(4) privateLen(4) crc(4) private(n)
type fileHeader struct {
	tagsAreaLen uint32
	private     []byte
}

func encodeFileHeader(h fileHeader) []byte {
	buf := make([]byte, headerFixedSize+len(h.private))
	copy(buf[0:4], fileMagic)
	binary.LittleEndian.PutUint16(buf[4:6], formatVersion)
	// flags at [6:8] reserved as zero
	binary.LittleEndian.PutUint32(buf[8:12], h.tagsAreaLen)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(h.private)))
	copy(buf[headerFixedSize:], h.private)
	crc := crc32.Checksum(buf[0:16], castagnoli)
	crc = crc32.Update(crc, castagnoli, h.private)
	binary.LittleEndian.PutUint32(buf[16:20], crc)
	return buf
}

// decodeFileHeader reads and validates the header. Returns the header and its
// total length, which is the offset where the tags area begins.
func decodeFileHeader(r io.ReaderAt) (fileHeader, int64, error) {
	var fixed [headerFixedSize]byte
	if _, err := r.ReadAt(fixed[:], 0); err != nil {
		return fileHeader{}, 0, err
	}
	if string(fixed[0:4]) != fileMagic {
		return fileHeader{}, 0, ErrCorrupt
	}
	if binary.LittleEndian.Uint16(fixed[4:6]) != formatVersion {
		return fileHeader{}, 0, ErrCorrupt
	}
	tagsLen := binary.LittleEndian.Uint32(fixed[8:12])
	privLen := binary.LittleEndian.Uint32(fixed[12:16])
	if tagsLen > maxPayloadLen || privLen > maxPayloadLen {
		return fileHeader{}, 0, ErrCorrupt
	}
	priv := make([]byte, privLen)
	if privLen > 0 {
		if _, err := r.ReadAt(priv, headerFixedSize); err != nil {
			return fileHeader{}, 0, err
		}
	}
	crc := crc32.Checksum(fixed[0:16], castagnoli)
	crc = crc32.Update(crc, castagnoli, priv)
	if crc != binary.LittleEndian.Uint32(fixed[16:20]) {
		return fileHeader{}, 0, ErrCorrupt
	}
	return fileHeader{tagsAreaLen: tagsLen, private: priv}, headerFixedSize + int64(privLen), nil
}
