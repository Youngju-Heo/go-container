package gmc

import "encoding/binary"

// TrackID identifies a track within a file. Assigned by AddTrack.
type TrackID uint16

// TrackKind is a classification hint; container behavior never depends on it,
// except that the writer samples index entries for KindAudio tracks.
type TrackKind uint8

const (
	KindVideo TrackKind = 0
	KindAudio TrackKind = 1
	KindData  TrackKind = 2
)

// TrackInfo describes one track. ID is ignored on AddTrack input and filled
// in on read. All tracks share the same time origin: pts 0 is the session
// origin regardless of per-track timebase.
type TrackInfo struct {
	ID          TrackID
	Kind        TrackKind
	Codec       string
	TimebaseNum uint32
	TimebaseDen uint32
	Private     []byte
}

func encodeTrackInfo(info TrackInfo) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(info.ID))
	b = append(b, byte(info.Kind))
	b = binary.LittleEndian.AppendUint32(b, info.TimebaseNum)
	b = binary.LittleEndian.AppendUint32(b, info.TimebaseDen)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(info.Codec)))
	b = append(b, info.Codec...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(info.Private)))
	return append(b, info.Private...)
}

// decodeTrackInfo parses a TrackInfo payload and reports the bytes consumed,
// so multiple encoded TrackInfos can be parsed back to back (footer).
func decodeTrackInfo(p []byte) (TrackInfo, int, error) {
	c := &cursor{b: p}
	var info TrackInfo
	info.ID = TrackID(c.u16())
	info.Kind = TrackKind(c.u8())
	info.TimebaseNum = c.u32()
	info.TimebaseDen = c.u32()
	info.Codec = string(c.bytes(int(c.u16())))
	priv := c.bytes(int(c.u32()))
	if c.bad {
		return TrackInfo{}, 0, ErrCorrupt
	}
	info.Private = append([]byte(nil), priv...)
	return info, c.pos, nil
}
