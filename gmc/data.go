package gmc

import "encoding/binary"

const (
	// dataHeaderSize is trackID(2) + flags(1) + pts(8) without DTS.
	dataHeaderSize = 11
	// dataHeaderDTSSize additionally includes the optional dts(8).
	dataHeaderDTSSize = 19

	flagKeyframe byte = 0x01
	flagHasDTS   byte = 0x02
)

// dataHeader is the decoded fixed header of a Data payload. n is the header
// length in bytes (11, or 19 when flagHasDTS is set); the frame body is p[n:].
type dataHeader struct {
	id    TrackID
	flags byte
	pts   uint64
	dts   uint64 // valid only when flags&flagHasDTS != 0
	n     int
}

// encodeDataPayload serializes a Data payload. dts is written only when
// flags&flagHasDTS is set.
func encodeDataPayload(dst []byte, id TrackID, flags byte, pts, dts uint64, data []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(id))
	dst = append(dst, flags)
	dst = binary.LittleEndian.AppendUint64(dst, pts)
	if flags&flagHasDTS != 0 {
		dst = binary.LittleEndian.AppendUint64(dst, dts)
	}
	return append(dst, data...)
}

func decodeDataHeader(p []byte) (dataHeader, error) {
	if len(p) < dataHeaderSize {
		return dataHeader{}, ErrCorrupt
	}
	h := dataHeader{
		id:    TrackID(binary.LittleEndian.Uint16(p)),
		flags: p[2],
		pts:   binary.LittleEndian.Uint64(p[3:11]),
		n:     dataHeaderSize,
	}
	if h.flags&flagHasDTS != 0 {
		if len(p) < dataHeaderDTSSize {
			return dataHeader{}, ErrCorrupt
		}
		h.dts = binary.LittleEndian.Uint64(p[11:19])
		h.n = dataHeaderDTSSize
	}
	return h, nil
}
