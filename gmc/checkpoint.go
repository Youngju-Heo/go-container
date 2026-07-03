package gmc

import "encoding/binary"

// cpEntry is one sync-point index entry: (track, pts) -> file offset of the
// Data chunk. Used by checkpoints, the footer, and the in-memory index.
type cpEntry struct {
	track TrackID
	pts   uint64
	off   int64
}

const cpEntrySize = 18 // track(2) + pts(8) + off(8)

func appendCPEntries(b []byte, entries []cpEntry) []byte {
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, uint16(e.track))
		b = binary.LittleEndian.AppendUint64(b, e.pts)
		b = binary.LittleEndian.AppendUint64(b, uint64(e.off))
	}
	return b
}

func readCPEntries(c *cursor, n int) []cpEntry {
	if n < 0 || !c.need(n*cpEntrySize) {
		c.bad = true
		return nil
	}
	entries := make([]cpEntry, 0, n)
	for i := 0; i < n; i++ {
		e := cpEntry{}
		e.track = TrackID(c.u16())
		e.pts = c.u64()
		e.off = int64(c.u64())
		entries = append(entries, e)
	}
	return entries
}

func encodeCheckpoint(prevOff int64, entries []cpEntry) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(prevOff))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(entries)))
	return appendCPEntries(b, entries)
}

func decodeCheckpoint(p []byte) (int64, []cpEntry, error) {
	c := &cursor{b: p}
	prev := int64(c.u64())
	n := int(c.u32())
	entries := readCPEntries(c, n)
	if c.bad {
		return 0, nil, ErrCorrupt
	}
	return prev, entries, nil
}
