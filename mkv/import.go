package mkv

import (
	"io"
	"os"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
)

// Range selects a stream-relative time window. The zero value means the
// whole file. From snaps back to the previous sync point per track; the end
// rule is GOP-granular for video and exact for audio/text (design §3.4).
type Range struct {
	From, To time.Duration
}

type ImportOptions struct {
	Range Range
}

type SkippedTrack struct {
	Number  uint64
	CodecID string
	Reason  string
}

type Result struct {
	Tracks        int
	Frames        int
	SkippedTracks []SkippedTrack
}

// mkvEpochUnixNano is 2001-01-01T00:00:00 UTC (the Matroska DateUTC epoch)
// in Unix nanoseconds.
const mkvEpochUnixNano = 978307200 * 1_000_000_000

var supported = map[string]bool{
	codec.CodecAVC: true, codec.CodecHEVC: true,
	codec.CodecPCM: true, codec.CodecOpus: true, codec.CodecAAC: true, codec.CodecFLAC: true,
	codec.CodecTextUTF8: true,
}

// Import converts an MKV file into a finalized GMC file.
func Import(mkvPath, gmcPath string, opts ImportOptions) (*Result, error) {
	f, err := os.Open(mkvPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	res := &Result{}
	d, err := NewDemuxer(f, fi.Size())
	if err != nil {
		return nil, err
	}
	scale := d.Info().TimestampScale

	// range in scale units (0 = unbounded)
	fromTS := int64(mulDiv(uint64(opts.Range.From), 1, scale))
	toTS := int64(mulDiv(uint64(opts.Range.To), 1, scale))
	ranged := opts.Range != Range{}

	// pass 1 (ranged only): keyframe-snap start for video tracks. Audio/text
	// cut exactly at From (symmetric with the To rule) — no snap needed.
	videoTrack := map[uint64]bool{}
	for _, te := range d.Tracks() {
		if te.Type == trackTypeVideo {
			videoTrack[te.Number] = true
		}
	}
	snap := map[uint64]int64{}
	if ranged {
		for {
			p, err := d.ReadPacket()
			if err == io.EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			if videoTrack[p.Track] && p.Keyframe && p.Timestamp <= fromTS {
				snap[p.Track] = p.Timestamp
			}
		}
		// reopen for pass 2
		if d, err = NewDemuxer(f, fi.Size()); err != nil {
			return nil, err
		}
	}

	w, err := gmc.Create(gmcPath, gmc.CreateOptions{})
	if err != nil {
		return nil, err
	}
	defer w.Close() // no-op after Finalize

	// tracks
	gmcID := map[uint64]gmc.TrackID{}
	isVideo := map[uint64]bool{}
	isText := map[uint64]bool{}
	for _, te := range d.Tracks() {
		info, ok := mapTrack(te)
		if !ok {
			res.SkippedTracks = append(res.SkippedTracks, SkippedTrack{Number: te.Number, CodecID: te.CodecID, Reason: "unsupported codec"})
			continue
		}
		info.TimebaseNum = uint32(scale)
		info.TimebaseDen = 1_000_000_000
		id, err := w.AddTrack(info)
		if err != nil {
			return nil, err
		}
		gmcID[te.Number] = id
		isVideo[te.Number] = te.Type == trackTypeVideo
		isText[te.Number] = te.Type == trackTypeSubtitle
		res.Tracks++
	}

	// metadata
	if d.Info().HasDate {
		if err := w.SetStartTime(time.Unix(0, d.Info().DateUTC+mkvEpochUnixNano)); err != nil {
			return nil, err
		}
	}
	for name, val := range d.Tags() {
		if err := w.SetTag(name, []byte(val)); err != nil {
			return nil, err
		}
	}

	// pass 2: frames
	videoDone := map[uint64]bool{}
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		id, ok := gmcID[p.Track]
		if !ok || p.Timestamp < 0 {
			continue
		}
		if ranged {
			if isVideo[p.Track] {
				if p.Timestamp < snap[p.Track] {
					continue
				}
			} else if p.Timestamp < fromTS {
				continue // audio/text: exact cut at From
			}
			if toTS > 0 {
				if isVideo[p.Track] {
					if videoDone[p.Track] {
						continue
					}
					if p.Keyframe && p.Timestamp >= toTS {
						videoDone[p.Track] = true
						continue
					}
				} else if p.Timestamp >= toTS {
					continue
				}
			}
		}
		data := p.Data
		if isText[p.Track] {
			data = codec.EncodeTextFrame(uint64(p.Duration), string(p.Data))
		}
		if err := w.WriteFrame(id, gmc.Frame{PTS: uint64(p.Timestamp), Keyframe: p.Keyframe, Data: data}); err != nil {
			return nil, err
		}
		res.Frames++
	}
	// late tags (Tags element after clusters was collected during packet walk)
	for name, val := range d.Tags() {
		if err := w.SetTag(name, []byte(val)); err != nil {
			return nil, err
		}
	}
	if err := w.Finalize(); err != nil {
		return nil, err
	}
	return res, nil
}

// mapTrack maps a Matroska TrackEntry into a GMC TrackInfo with the codec
// envelope. ok=false for unsupported codec/type combinations.
func mapTrack(te TrackEntry) (gmc.TrackInfo, bool) {
	if !supported[te.CodecID] {
		return gmc.TrackInfo{}, false
	}
	switch te.Type {
	case trackTypeVideo:
		return gmc.TrackInfo{
			Kind:  gmc.KindVideo,
			Codec: te.CodecID,
			Private: codec.EncodeVideoPrivate(codec.VideoParams{
				Width: uint32(te.PixelWidth), Height: uint32(te.PixelHeight),
			}, te.CodecPrivate),
			Reordered: true, // decode-order stream; B-frame presence unknown
		}, true
	case trackTypeAudio:
		out := uint32(te.OutputSamplingFrequency + 0.5)
		if te.OutputSamplingFrequency == te.SamplingFrequency {
			out = 0
		}
		return gmc.TrackInfo{
			Kind:  gmc.KindAudio,
			Codec: te.CodecID,
			Private: codec.EncodeAudioPrivate(codec.AudioParams{
				SampleRate:       uint32(te.SamplingFrequency + 0.5),
				OutputSampleRate: out,
				Channels:         uint8(te.Channels),
				BitDepth:         uint8(te.BitDepth),
			}, te.CodecPrivate),
		}, true
	case trackTypeSubtitle:
		return gmc.TrackInfo{
			Kind:    gmc.KindData,
			Codec:   te.CodecID,
			Private: codec.EncodeTextPrivate(te.CodecPrivate),
		}, true
	default:
		return gmc.TrackInfo{}, false
	}
}
