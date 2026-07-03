package gmc

import "encoding/binary"

const (
	// dataHeaderSize is trackID(2) + flags(1) + pts(8).
	dataHeaderSize = 11

	flagKeyframe byte = 0x01
)

func encodeDataPayload(dst []byte, id TrackID, flags byte, pts uint64, data []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(id))
	dst = append(dst, flags)
	dst = binary.LittleEndian.AppendUint64(dst, pts)
	return append(dst, data...)
}

func decodeDataHeader(p []byte) (TrackID, byte, uint64, error) {
	if len(p) < dataHeaderSize {
		return 0, 0, 0, ErrCorrupt
	}
	return TrackID(binary.LittleEndian.Uint16(p)), p[2], binary.LittleEndian.Uint64(p[3:11]), nil
}
