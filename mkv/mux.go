package mkv

import (
	"encoding/binary"
	"math"
	"os"
)

// Muxer writes a minimal standard-form Matroska file: SimpleBlocks (no
// lacing), BlockGroup+BlockDuration only for packets carrying a duration,
// clusters split on video keyframes / int16 range / 4 MiB, Cues on video
// keyframes, SeekHead and Duration patched on Finalize.
type Muxer struct {
	f     *os.File
	scale uint64

	off         int64 // current write offset
	segDataOff  int64 // offset of first byte inside Segment (position base)
	segSizePos  int64 // where the Segment 8-byte size vint lives
	seekHeadPos int64
	durPos      int64 // where the Duration float64 payload lives

	tracks     []TrackEntry
	hasVideo   bool
	videoTrack uint64

	clusterOpen    bool
	clusterSizePos int64
	clusterTS      int64
	clusterBytes   int64

	infoPos, tracksPos, cuesPos int64 // segment-relative, for SeekHead
	cues                        []cuePoint
	buf                         []byte
}

type cuePoint struct {
	time    int64
	track   uint64
	cluster int64 // segment-relative cluster offset
}

const seekHeadReserve = 80 // SeekHead payload reservation (patched into Void)

func NewMuxer(f *os.File, scale uint64) *Muxer {
	if scale == 0 {
		scale = 1000000
	}
	return &Muxer{f: f, scale: scale}
}

func (m *Muxer) write(b []byte) error {
	if _, err := m.f.WriteAt(b, m.off); err != nil {
		return err
	}
	m.off += int64(len(b))
	return nil
}

// writeUnknownSize8 writes an 8-byte size vint placeholder and returns its
// position for later patching.
func (m *Muxer) writeUnknownSize8() (int64, error) {
	pos := m.off
	return pos, m.write([]byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF})
}

// patchSize8 rewrites an 8-byte size vint at pos with the size of
// [pos+8, m.off).
func (m *Muxer) patchSize8(pos int64) error {
	size := m.off - pos - 8
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(size))
	b[0] = 0x01
	_, err := m.f.WriteAt(b[:], pos)
	return err
}

func (m *Muxer) WriteHeader(info FileInfo, tracks []TrackEntry, tags map[string]string) error {
	m.tracks = tracks
	for _, te := range tracks {
		if te.Type == trackTypeVideo && !m.hasVideo {
			m.hasVideo = true
			m.videoTrack = te.Number
		}
	}
	var hdr []byte
	hdr = appendUintElement(hdr, idEBMLVersion, 1)
	hdr = appendUintElement(hdr, idEBMLReadVersion, 1)
	hdr = appendUintElement(hdr, idEBMLMaxIDLength, 4)
	hdr = appendUintElement(hdr, idEBMLMaxSizeLength, 8)
	hdr = appendStringElement(hdr, idDocType, "matroska")
	hdr = appendUintElement(hdr, idDocTypeVersion, 4)
	hdr = appendUintElement(hdr, idDocTypeReadVer, 2)
	if err := m.write(appendElement(nil, idEBML, hdr)); err != nil {
		return err
	}

	if err := m.write(appendVintID(nil, idSegment)); err != nil {
		return err
	}
	var err error
	if m.segSizePos, err = m.writeUnknownSize8(); err != nil {
		return err
	}
	m.segDataOff = m.off

	// SeekHead reservation as Void; patched in Finalize
	m.seekHeadPos = m.off
	void := appendElement(nil, idVoid, make([]byte, seekHeadReserve))
	if err := m.write(void); err != nil {
		return err
	}

	// Info — Duration written as a fixed 8-byte float so it can be patched.
	// durPos must account for the Info element's own ID+size-vint prefix
	// (which isn't known until ib is fully built), not just the offset
	// within ib.
	m.infoPos = m.off - m.segDataOff
	var ib []byte
	ib = appendUintElement(ib, idTimestampScale, m.scale)
	ib = appendVintID(ib, idDuration)
	ib = appendVintSize(ib, 8)
	durOffsetInIB := len(ib)
	ib = append(ib, make([]byte, 8)...) // patched in Finalize
	if info.HasDate {
		var db [8]byte
		binary.BigEndian.PutUint64(db[:], uint64(info.DateUTC))
		ib = appendElement(ib, idDateUTC, db[:])
	}
	ib = appendStringElement(ib, idMuxingApp, "gmc-go")
	ib = appendStringElement(ib, idWritingApp, "gmc-go")
	infoElem := appendElement(nil, idInfo, ib)
	m.durPos = m.off + int64(len(infoElem)-len(ib)) + int64(durOffsetInIB)
	if err := m.write(infoElem); err != nil {
		return err
	}

	// Tracks
	m.tracksPos = m.off - m.segDataOff
	var tb []byte
	for _, te := range tracks {
		tb = appendElement(tb, idTrackEntry, encodeTrackEntry(te))
	}
	if err := m.write(appendElement(nil, idTracks, tb)); err != nil {
		return err
	}

	// Tags (before clusters keeps sequential demuxers simple)
	if len(tags) > 0 {
		var all []byte
		for name, val := range tags {
			var st []byte
			st = appendStringElement(st, idTagName, name)
			st = appendStringElement(st, idTagString, val)
			var tg []byte
			tg = appendElement(tg, idTargets, nil)
			tg = appendElement(tg, idSimpleTag, st)
			all = appendElement(all, idTag, tg)
		}
		if err := m.write(appendElement(nil, idTags, all)); err != nil {
			return err
		}
	}
	return nil
}

func encodeTrackEntry(te TrackEntry) []byte {
	var b []byte
	b = appendUintElement(b, idTrackNumber, te.Number)
	b = appendUintElement(b, idTrackUID, te.Number)
	b = appendUintElement(b, idTrackType, uint64(te.Type))
	b = appendUintElement(b, idFlagLacing, 0)
	b = appendStringElement(b, idCodecID, te.CodecID)
	if len(te.CodecPrivate) > 0 {
		b = appendElement(b, idCodecPrivate, te.CodecPrivate)
	}
	if te.DefaultDuration > 0 {
		b = appendUintElement(b, idDefaultDuration, te.DefaultDuration)
	}
	switch te.Type {
	case trackTypeVideo:
		var v []byte
		v = appendUintElement(v, idPixelWidth, te.PixelWidth)
		v = appendUintElement(v, idPixelHeight, te.PixelHeight)
		b = appendElement(b, idVideo, v)
	case trackTypeAudio:
		var a []byte
		a = appendFloatElement(a, idSamplingFreq, te.SamplingFrequency)
		if te.OutputSamplingFrequency > 0 {
			a = appendFloatElement(a, idOutSamplingFreq, te.OutputSamplingFrequency)
		}
		a = appendUintElement(a, idChannels, te.Channels)
		if te.BitDepth > 0 {
			a = appendUintElement(a, idBitDepth, te.BitDepth)
		}
		b = appendElement(b, idAudio, a)
	}
	return b
}

func (m *Muxer) closeCluster() error {
	if !m.clusterOpen {
		return nil
	}
	m.clusterOpen = false
	return m.patchSize8(m.clusterSizePos)
}

func (m *Muxer) openCluster(ts int64) error {
	if err := m.write(appendVintID(nil, idCluster)); err != nil {
		return err
	}
	var err error
	if m.clusterSizePos, err = m.writeUnknownSize8(); err != nil {
		return err
	}
	m.clusterTS = ts
	m.clusterBytes = 0
	m.clusterOpen = true
	return m.write(appendUintElement(nil, idTimestamp, uint64(ts)))
}

func (m *Muxer) WritePacket(p Packet) error {
	rel := p.Timestamp - m.clusterTS
	needNew := !m.clusterOpen ||
		(m.hasVideo && p.Track == m.videoTrack && p.Keyframe) ||
		rel > 32000 || rel < -32000 ||
		m.clusterBytes > 4<<20
	if needNew {
		if err := m.closeCluster(); err != nil {
			return err
		}
		if err := m.openCluster(p.Timestamp); err != nil {
			return err
		}
		rel = 0
	}
	if m.hasVideo && p.Track == m.videoTrack && p.Keyframe {
		m.cues = append(m.cues, cuePoint{time: p.Timestamp, track: p.Track, cluster: m.off - m.segDataOff - clusterHeaderLen(m)})
	}

	var blk []byte
	blk = appendVintSize(blk, int64(p.Track))
	blk = append(blk, byte(uint16(rel)>>8), byte(uint16(rel)))
	if p.Duration > 0 {
		blk = append(blk, 0x00) // Block flags: no keyframe bit, no lacing
		blk = append(blk, p.Data...)
		var bg []byte
		bg = appendElement(bg, idBlock, blk)
		bg = appendUintElement(bg, idBlockDur, uint64(p.Duration))
		m.buf = appendElement(m.buf[:0], idBlockGroup, bg)
	} else {
		var flags byte
		if p.Keyframe {
			flags |= 0x80
		}
		blk = append(blk, flags)
		blk = append(blk, p.Data...)
		m.buf = appendElement(m.buf[:0], idSimpleBlock, blk)
	}
	m.clusterBytes += int64(len(m.buf))
	return m.write(m.buf)
}

// clusterHeaderLen is the byte distance from the cluster element ID to the
// current write position at cue-record time: ID(4) + size(8) + the timestamp
// element already written. Computed dynamically to stay correct.
func clusterHeaderLen(m *Muxer) int64 {
	// cluster start = position right after openCluster wrote ID+size+timestamp;
	// cue wants the offset of the cluster ID itself.
	tsLen := int64(len(appendUintElement(nil, idTimestamp, uint64(m.clusterTS))))
	return 4 + 8 + tsLen
}

func (m *Muxer) Finalize(duration float64) error {
	if err := m.closeCluster(); err != nil {
		return err
	}
	// Cues
	m.cuesPos = m.off - m.segDataOff
	if len(m.cues) > 0 {
		var cb []byte
		for _, c := range m.cues {
			var pos []byte
			pos = appendUintElement(pos, idCueTrack, c.track)
			pos = appendUintElement(pos, idCueClusterPos, uint64(c.cluster))
			var cp []byte
			cp = appendUintElement(cp, idCueTime, uint64(c.time))
			cp = appendElement(cp, idCueTrackPos, pos)
			cb = appendElement(cb, idCuePoint, cp)
		}
		if err := m.write(appendElement(nil, idCues, cb)); err != nil {
			return err
		}
	}
	// patch Segment size
	if err := m.patchSize8(m.segSizePos); err != nil {
		return err
	}
	// patch Duration
	var db [8]byte
	binary.BigEndian.PutUint64(db[:], math.Float64bits(duration))
	if _, err := m.f.WriteAt(db[:], m.durPos); err != nil {
		return err
	}
	// SeekHead into the reserved Void
	var sh []byte
	for _, e := range []struct {
		id  uint32
		pos int64
	}{{idInfo, m.infoPos}, {idTracks, m.tracksPos}, {idCues, m.cuesPos}} {
		var sk []byte
		sk = appendElement(sk, idSeekID, appendVintID(nil, e.id))
		sk = appendUintElement(sk, idSeekPos, uint64(e.pos))
		sh = appendElement(sh, idSeek, sk)
	}
	head := appendElement(nil, idSeekHead, sh)
	reserved := int64(len(appendElement(nil, idVoid, make([]byte, seekHeadReserve))))
	if int64(len(head)) > reserved-2 {
		return errMalformed // reservation too small (should not happen: 3 seeks fit in 80 bytes)
	}
	// remaining space refilled as Void; compute the payload length from the
	// actual size-vint length (rather than assuming 1 byte) so the Void
	// element exactly fills what's left of the reservation.
	rem := reserved - int64(len(head))
	sizeVint := appendVintSize(nil, rem-2)
	payloadLen := rem - 1 - int64(len(sizeVint))
	head = appendVintID(head, idVoid)
	head = appendVintSize(head, payloadLen)
	head = append(head, make([]byte, payloadLen)...)
	if _, err := m.f.WriteAt(head, m.seekHeadPos); err != nil {
		return err
	}
	return m.f.Sync()
}
