package codec

import "encoding/binary"

// EncodeTextFrame builds an S_TEXT/UTF8 frame payload: duration(u64 LE, track
// timebase units; 0 when unknown) followed by the UTF-8 text. MKV keeps the
// duration outside the block (BlockDuration); GMC frames prefix it instead.
func EncodeTextFrame(duration uint64, text string) []byte {
	b := binary.LittleEndian.AppendUint64(nil, duration)
	return append(b, text...)
}

func DecodeTextFrame(b []byte) (uint64, string, error) {
	if len(b) < 8 {
		return 0, "", ErrInvalidFrame
	}
	return binary.LittleEndian.Uint64(b), string(b[8:]), nil
}
