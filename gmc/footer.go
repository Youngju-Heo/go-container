package gmc

import (
	"encoding/binary"
	"hash/crc32"
)

// trackSummary is per-track summary info stored in the footer.
type trackSummary struct {
	track    TrackID
	firstPTS uint64
	lastPTS  uint64
	frames   uint64
}

// encodeFooter serializes the consolidated footer:
//
//	trackCount(2) TrackInfo* summaryCount(2) summary* entryCount(4) cpEntry*
func encodeFooter(tracks []TrackInfo, sums []trackSummary, entries []cpEntry) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(tracks)))
	for _, tr := range tracks {
		b = append(b, encodeTrackInfo(tr)...)
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(sums)))
	for _, s := range sums {
		b = binary.LittleEndian.AppendUint16(b, uint16(s.track))
		b = binary.LittleEndian.AppendUint64(b, s.firstPTS)
		b = binary.LittleEndian.AppendUint64(b, s.lastPTS)
		b = binary.LittleEndian.AppendUint64(b, s.frames)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(entries)))
	return appendCPEntries(b, entries)
}

func decodeFooter(p []byte) ([]TrackInfo, []trackSummary, []cpEntry, error) {
	c := &cursor{b: p}
	nt := int(c.u16())
	if c.bad {
		return nil, nil, nil, ErrCorrupt
	}
	tracks := make([]TrackInfo, 0, nt)
	for i := 0; i < nt; i++ {
		info, n, err := decodeTrackInfo(p[c.pos:])
		if err != nil {
			return nil, nil, nil, err
		}
		c.pos += n
		tracks = append(tracks, info)
	}
	ns := int(c.u16())
	if ns < 0 || !c.need(ns*26) {
		return nil, nil, nil, ErrCorrupt
	}
	sums := make([]trackSummary, 0, ns)
	for i := 0; i < ns; i++ {
		s := trackSummary{}
		s.track = TrackID(c.u16())
		s.firstPTS = c.u64()
		s.lastPTS = c.u64()
		s.frames = c.u64()
		sums = append(sums, s)
	}
	ne := int(c.u32())
	entries := readCPEntries(c, ne)
	if c.bad {
		return nil, nil, nil, ErrCorrupt
	}
	return tracks, sums, entries, nil
}

// encodeTrailer builds the fixed 16-byte trailer:
//
//	footerOffset(8) crc(4) endMagic(4)
func encodeTrailer(footerOff int64) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(footerOff))
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
	return append(b, endMagic...)
}

func decodeTrailer(b []byte) (int64, bool) {
	if len(b) != trailerSize || string(b[12:16]) != endMagic {
		return 0, false
	}
	if crc32.Checksum(b[:8], castagnoli) != binary.LittleEndian.Uint32(b[8:12]) {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(b[:8])), true
}
