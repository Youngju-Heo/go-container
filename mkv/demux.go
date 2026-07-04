package mkv

import (
	"errors"
	"io"
)

var ErrNotMatroska = errors.New("mkv: not a matroska file")

// TrackEntry mirrors the Matroska TrackEntry fields this converter uses.
type TrackEntry struct {
	Number          uint64
	Type            uint8
	CodecID         string
	CodecPrivate    []byte
	DefaultDuration uint64 // ns per frame, 0 unknown

	PixelWidth, PixelHeight uint64

	SamplingFrequency       float64
	OutputSamplingFrequency float64
	Channels, BitDepth      uint64
}

// FileInfo mirrors the Segment Info fields this converter uses.
type FileInfo struct {
	TimestampScale uint64  // ns per timestamp unit (default 1e6)
	Duration       float64 // in TimestampScale units, 0 unknown
	DateUTC        int64   // ns since 2001-01-01T00:00:00 UTC
	HasDate        bool
}

// Demuxer reads a Matroska file sequentially: header metadata up front,
// then packets via ReadPacket (task 5).
type Demuxer struct {
	er           *ebmlReader
	info         FileInfo
	tracks       []TrackEntry
	tags         map[string]string
	segEnd       int64 // exclusive end of segment (file size for unknown-size)
	clusterStart int64 // offset of the first cluster element

	// cluster walk state
	queue      []Packet
	clusterTS  uint64
	inCluster  bool
	clusterEnd int64
}

// NewDemuxer validates the EBML header (DocType must be "matroska"), enters
// the Segment and parses Info/Tracks/Tags appearing before the first Cluster.
func NewDemuxer(r io.ReaderAt, size int64) (*Demuxer, error) {
	er := newEBMLReader(r, size)
	id, sz, _, err := er.readElement()
	if err != nil || id != idEBML {
		return nil, ErrNotMatroska
	}
	hdr, err := er.readBytes(sz)
	if err != nil {
		return nil, err
	}
	if !docTypeIsMatroska(hdr) {
		return nil, ErrNotMatroska
	}
	id, sz, unknown, err := er.readElement()
	if err != nil || id != idSegment {
		return nil, ErrNotMatroska
	}
	d := &Demuxer{
		er:   er,
		info: FileInfo{TimestampScale: 1000000},
		tags: map[string]string{},
	}
	d.segEnd = size
	if !unknown {
		d.segEnd = er.pos() + sz
	}
	if err := d.parsePreClusters(); err != nil {
		return nil, err
	}
	return d, nil
}

func docTypeIsMatroska(hdr []byte) bool {
	er := newEBMLReader(bytesReaderAt(hdr), int64(len(hdr)))
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			return false
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return false
		}
		if id == idDocType {
			return string(b) == "matroska"
		}
	}
}

// parsePreClusters walks top-level segment children until the first Cluster,
// filling info/tracks/tags. Unknown elements are skipped.
func (d *Demuxer) parsePreClusters() error {
	for d.er.pos() < d.segEnd {
		id, sz, unknown, err := d.er.readElement()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if id == idCluster {
			d.clusterStart = d.er.pos()
			d.inCluster = true
			d.clusterEnd = d.clusterEndFrom(sz, unknown)
			return nil
		}
		if unknown {
			return errMalformed // only Segment/Cluster may be unknown-size
		}
		body, err := d.er.readBytes(sz)
		if err != nil {
			return err
		}
		switch id {
		case idInfo:
			d.parseInfo(body)
		case idTracks:
			d.parseTracks(body)
		case idTags:
			d.parseTags(body)
		}
	}
	d.clusterStart = -1 // no clusters
	return nil
}

func (d *Demuxer) clusterEndFrom(sz int64, unknown bool) int64 {
	if unknown {
		return d.segEnd
	}
	return d.er.pos() + sz
}

func (d *Demuxer) parseInfo(body []byte) {
	er := newEBMLReader(bytesReaderAt(body), int64(len(body)))
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			return
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return
		}
		switch id {
		case idTimestampScale:
			if v := parseUint(b); v > 0 {
				d.info.TimestampScale = v
			}
		case idDuration:
			if f, err := parseFloat(b); err == nil {
				d.info.Duration = f
			}
		case idDateUTC:
			d.info.DateUTC = int64(parseUint(b))
			d.info.HasDate = true
		}
	}
}

func (d *Demuxer) parseTracks(body []byte) {
	er := newEBMLReader(bytesReaderAt(body), int64(len(body)))
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			return
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return
		}
		if id == idTrackEntry {
			d.tracks = append(d.tracks, parseTrackEntry(b))
		}
	}
}

func parseTrackEntry(body []byte) TrackEntry {
	te := TrackEntry{}
	er := newEBMLReader(bytesReaderAt(body), int64(len(body)))
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			return te
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return te
		}
		switch id {
		case idTrackNumber:
			te.Number = parseUint(b)
		case idTrackType:
			te.Type = uint8(parseUint(b))
		case idCodecID:
			te.CodecID = string(b)
		case idCodecPrivate:
			te.CodecPrivate = b
		case idDefaultDuration:
			te.DefaultDuration = parseUint(b)
		case idVideo:
			ver := newEBMLReader(bytesReaderAt(b), int64(len(b)))
			for {
				vid, vsz, _, err := ver.readElement()
				if err != nil {
					break
				}
				vb, err := ver.readBytes(vsz)
				if err != nil {
					break
				}
				switch vid {
				case idPixelWidth:
					te.PixelWidth = parseUint(vb)
				case idPixelHeight:
					te.PixelHeight = parseUint(vb)
				}
			}
		case idAudio:
			aer := newEBMLReader(bytesReaderAt(b), int64(len(b)))
			for {
				aid, asz, _, err := aer.readElement()
				if err != nil {
					break
				}
				ab, err := aer.readBytes(asz)
				if err != nil {
					break
				}
				switch aid {
				case idSamplingFreq:
					te.SamplingFrequency, _ = parseFloat(ab)
				case idOutSamplingFreq:
					te.OutputSamplingFrequency, _ = parseFloat(ab)
				case idChannels:
					te.Channels = parseUint(ab)
				case idBitDepth:
					te.BitDepth = parseUint(ab)
				}
			}
		}
	}
}

func (d *Demuxer) parseTags(body []byte) {
	er := newEBMLReader(bytesReaderAt(body), int64(len(body)))
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			return
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return
		}
		if id != idTag {
			continue
		}
		ter := newEBMLReader(bytesReaderAt(b), int64(len(b)))
		for {
			tid, tsz, _, err := ter.readElement()
			if err != nil {
				break
			}
			tb, err := ter.readBytes(tsz)
			if err != nil {
				break
			}
			if tid != idSimpleTag {
				continue
			}
			var name, value string
			ser := newEBMLReader(bytesReaderAt(tb), int64(len(tb)))
			for {
				sid, ssz, _, err := ser.readElement()
				if err != nil {
					break
				}
				sb, err := ser.readBytes(ssz)
				if err != nil {
					break
				}
				switch sid {
				case idTagName:
					name = string(sb)
				case idTagString:
					value = string(sb)
				}
			}
			if name != "" {
				d.tags[name] = value
			}
		}
	}
}

// Packet is one demuxed frame. Timestamp and Duration are absolute, in
// TimestampScale units.
type Packet struct {
	Track     uint64
	Timestamp int64
	Keyframe  bool
	Duration  int64 // 0 unknown
	Data      []byte
}

// defaultDurTS returns a track's DefaultDuration converted to TimestampScale
// units (0 when unknown).
func (d *Demuxer) defaultDurTS(track uint64) int64 {
	for _, te := range d.tracks {
		if te.Number == track && te.DefaultDuration > 0 {
			return int64(te.DefaultDuration / d.info.TimestampScale)
		}
	}
	return 0
}

// ReadPacket returns the next frame in storage order, io.EOF at the end.
func (d *Demuxer) ReadPacket() (*Packet, error) {
	for {
		if len(d.queue) > 0 {
			p := d.queue[0]
			d.queue = d.queue[1:]
			return &p, nil
		}
		if d.clusterStart < 0 {
			return nil, io.EOF
		}
		if err := d.nextBlock(); err != nil {
			return nil, err
		}
	}
}

// nextBlock advances to the next block-bearing element, filling d.queue.
func (d *Demuxer) nextBlock() error {
	for {
		if d.inCluster && d.er.pos() >= d.clusterEnd {
			d.inCluster = false
		}
		if !d.inCluster {
			// walk top-level until the next cluster (skip Cues/Tags/... but
			// still collect Tags appearing after clusters)
			for {
				if d.er.pos() >= d.segEnd {
					return io.EOF
				}
				id, sz, unknown, err := d.er.readElement()
				if err == io.EOF {
					return io.EOF
				}
				if err != nil {
					return err
				}
				if id == idCluster {
					d.clusterTS = 0
					d.inCluster = true
					d.clusterEnd = d.clusterEndFrom(sz, unknown)
					break
				}
				if unknown {
					return errMalformed
				}
				body, err := d.er.readBytes(sz)
				if err != nil {
					return err
				}
				if id == idTags {
					d.parseTags(body)
				}
			}
		}
		id, sz, unknown, err := d.er.readElement()
		if err == io.EOF {
			return io.EOF
		}
		if err != nil {
			return err
		}
		if id == idCluster {
			// a new cluster begins here (also ends an unknown-size cluster);
			// do NOT read its body — descend into its scope instead
			d.clusterTS = 0
			d.clusterEnd = d.clusterEndFrom(sz, unknown)
			continue
		}
		if unknown {
			return errMalformed
		}
		body, err := d.er.readBytes(sz)
		if err != nil {
			return err
		}
		switch id {
		case idTimestamp:
			d.clusterTS = parseUint(body)
		case idSimpleBlock:
			if err := d.parseBlock(body, true, 0); err != nil {
				return err
			}
			if len(d.queue) > 0 {
				return nil
			}
		case idBlockGroup:
			if err := d.parseBlockGroup(body); err != nil {
				return err
			}
			if len(d.queue) > 0 {
				return nil
			}
		}
	}
}

func (d *Demuxer) parseBlockGroup(body []byte) error {
	er := newEBMLReader(bytesReaderAt(body), int64(len(body)))
	var blk []byte
	var duration int64
	hasRef := false
	for {
		id, sz, _, err := er.readElement()
		if err != nil {
			break
		}
		b, err := er.readBytes(sz)
		if err != nil {
			return err
		}
		switch id {
		case idBlock:
			blk = b
		case idBlockDur:
			duration = int64(parseUint(b))
		case idRefBlock:
			hasRef = true
		}
	}
	if blk == nil {
		return errMalformed
	}
	keyframe := !hasRef
	return d.parseBlockPayload(blk, keyframe, false, duration)
}

// parseBlock handles a SimpleBlock (keyframe from flags bit 0x80).
func (d *Demuxer) parseBlock(body []byte, simple bool, duration int64) error {
	return d.parseBlockPayload(body, false, simple, duration)
}

// parseBlockPayload decodes track/timestamp/flags and de-laces frames into
// d.queue. For SimpleBlocks (simple=true) keyframe comes from flags 0x80;
// otherwise the caller passes it via kf.
func (d *Demuxer) parseBlockPayload(body []byte, kf, simple bool, duration int64) error {
	track, n := readVintAt(body)
	if n == 0 || len(body) < n+3 {
		return errMalformed
	}
	rel := int16(uint16(body[n])<<8 | uint16(body[n+1]))
	flags := body[n+2]
	if simple {
		kf = flags&0x80 != 0
	}
	ts := int64(d.clusterTS) + int64(rel)
	rest := body[n+3:]

	lacing := (flags >> 1) & 0x03
	if lacing == 0 {
		d.queue = append(d.queue, Packet{Track: track, Timestamp: ts, Keyframe: kf, Duration: duration, Data: append([]byte(nil), rest...)})
		return nil
	}
	if len(rest) < 1 {
		return errMalformed
	}
	count := int(rest[0]) + 1
	rest = rest[1:]
	sizes := make([]int, count)
	switch lacing {
	case 1: // Xiph
		for i := 0; i < count-1; i++ {
			for {
				if len(rest) == 0 {
					return errMalformed
				}
				v := int(rest[0])
				rest = rest[1:]
				sizes[i] += v
				if v != 255 {
					break
				}
			}
		}
	case 2: // fixed
		if len(rest)%count != 0 {
			return errMalformed
		}
		for i := range sizes {
			sizes[i] = len(rest) / count
		}
	case 3: // EBML
		v, vn := readVintAt(rest)
		if vn == 0 {
			return errMalformed
		}
		rest = rest[vn:]
		sizes[0] = int(v)
		for i := 1; i < count-1; i++ {
			dlt, dn := readSignedVintAt(rest)
			if dn == 0 {
				return errMalformed
			}
			rest = rest[dn:]
			sizes[i] = sizes[i-1] + int(dlt)
			if sizes[i] < 0 {
				return errMalformed
			}
		}
	}
	// last frame (Xiph/EBML): remainder
	if lacing != 2 {
		used := 0
		for i := 0; i < count-1; i++ {
			used += sizes[i]
		}
		if used > len(rest) {
			return errMalformed
		}
		sizes[count-1] = len(rest) - used
	}
	step := d.defaultDurTS(track)
	off := 0
	for i := 0; i < count; i++ {
		if off+sizes[i] > len(rest) {
			return errMalformed
		}
		d.queue = append(d.queue, Packet{
			Track:     track,
			Timestamp: ts + int64(i)*step,
			Keyframe:  kf,
			Duration:  duration,
			Data:      append([]byte(nil), rest[off:off+sizes[i]]...),
		})
		off += sizes[i]
	}
	return nil
}

func (d *Demuxer) Info() FileInfo          { return d.info }
func (d *Demuxer) Tracks() []TrackEntry    { return d.tracks }
func (d *Demuxer) Tags() map[string]string { return d.tags }

// bytesReaderAt adapts a byte slice to io.ReaderAt without importing bytes
// in every parse helper.
type bytesReaderAt []byte

func (b bytesReaderAt) ReadAt(p []byte, off int64) (int, error) {
	if off >= int64(len(b)) {
		return 0, io.EOF
	}
	n := copy(p, b[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}
