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

	// cluster walk state (packet queue field added in task 5)
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
