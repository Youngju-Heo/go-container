package mkv

import (
	"fmt"
	"os"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
)

type ExportOptions struct {
	Range          Range
	TimestampScale uint64 // 0 -> 1_000_000 (1 ms)

	// Tracks restricts export to the given GMC track IDs, in any order.
	// nil or empty exports all tracks (default behavior). Any ID not present
	// in the source file is an error; a selected track that is otherwise
	// unsupported still follows the normal skip semantics.
	Tracks []gmc.TrackID
}

// Export converts a GMC file (written under the gmc/codec conventions) into
// a Matroska file. The GMC file must be openable via gmc.Open (finalized or
// crash-recovered); live export is out of scope.
func Export(gmcPath, mkvPath string, opts ExportOptions) (*Result, error) {
	scale := opts.TimestampScale
	if scale == 0 {
		scale = 1000000
	}
	r, err := gmc.Open(gmcPath)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	res := &Result{}
	type trackMap struct {
		id     gmc.TrackID
		number uint64
		video  bool
		text   bool
		info   gmc.TrackInfo
	}
	tracks := r.Tracks()
	if len(opts.Tracks) > 0 {
		selected := make(map[gmc.TrackID]bool, len(opts.Tracks))
		for _, id := range opts.Tracks {
			selected[id] = true
		}
		for id := range selected {
			found := false
			for _, info := range tracks {
				if info.ID == id {
					found = true
					break
				}
			}
			if !found {
				return nil, fmt.Errorf("mkv: unknown track id %d", id)
			}
		}
		var filtered []gmc.TrackInfo
		for _, info := range tracks {
			if selected[info.ID] {
				filtered = append(filtered, info)
			}
		}
		tracks = filtered
	}

	var maps []trackMap
	var entries []TrackEntry
	num := uint64(1)
	for _, info := range tracks {
		te, ok := mapTrackOut(info, num)
		if !ok {
			res.SkippedTracks = append(res.SkippedTracks, SkippedTrack{Number: uint64(info.ID), CodecID: info.Codec, Reason: "unsupported codec or bad envelope"})
			continue
		}
		maps = append(maps, trackMap{id: info.ID, number: num, video: info.Kind == gmc.KindVideo, text: info.Kind == gmc.KindData, info: info})
		entries = append(entries, te)
		num++
		res.Tracks++
	}

	// pts -> scale units: pts * num * 1e9 / (den * scale)
	toScale := func(info gmc.TrackInfo, pts uint64) int64 {
		ns := mulDiv(pts, uint64(info.TimebaseNum)*1_000_000_000, uint64(info.TimebaseDen))
		return int64(ns / scale)
	}

	// range preparation. Video: keyframe snap via SeekPTS (its first frame is
	// the last sync point at or before From). Audio/text: exact cut at From —
	// gmc's audio index is sampled, so index-based snapping would pull the
	// start arbitrarily far back; the exact rule is also symmetric with To.
	ranged := opts.Range != Range{}
	snapPTS := map[gmc.TrackID]uint64{}
	toPTS := map[gmc.TrackID]uint64{}
	hasSnap := map[gmc.TrackID]bool{}
	if ranged {
		for _, tm := range maps {
			fromPts := mulDiv(uint64(opts.Range.From), uint64(tm.info.TimebaseDen), uint64(tm.info.TimebaseNum)*1_000_000_000)
			if tm.video {
				it, err := r.SeekPTS(tm.id, fromPts)
				if err != nil {
					return nil, err
				}
				if it.Next() {
					snapPTS[tm.id] = it.Frame().PTS
					hasSnap[tm.id] = true
				}
			} else {
				snapPTS[tm.id] = fromPts
				hasSnap[tm.id] = true
			}
			if opts.Range.To > 0 {
				toPTS[tm.id] = mulDiv(uint64(opts.Range.To), uint64(tm.info.TimebaseDen), uint64(tm.info.TimebaseNum)*1_000_000_000)
			}
		}
	}

	f, err := os.Create(mkvPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	m := NewMuxer(f, scale)

	info := FileInfo{}
	if st, ok := r.StartTime(); ok {
		info.DateUTC = st.UnixNano() - mkvEpochUnixNano
		info.HasDate = true
	}
	tags := map[string]string{}
	for k, v := range r.Tags() {
		if k == gmc.TagStartTime {
			continue // carried via DateUTC
		}
		if len(k) > 4 && k[:4] == "gmc." {
			continue
		}
		tags[k] = string(v)
	}
	if err := m.WriteHeader(info, entries, tags); err != nil {
		return nil, err
	}

	// All tracks skipped: ReadInterleaved treats an empty track list as "all
	// tracks", which would emit blocks for tracks absent from the header.
	// Finalize a headers-only, zero-frame file instead.
	if len(maps) == 0 {
		if err := m.Finalize(0); err != nil {
			return nil, err
		}
		return res, nil
	}

	ids := make([]gmc.TrackID, len(maps))
	byID := map[gmc.TrackID]trackMap{}
	for i, tm := range maps {
		ids[i] = tm.id
		byID[tm.id] = tm
	}
	it, err := r.ReadInterleaved(0, ids...)
	if err != nil {
		return nil, err
	}
	videoDone := map[gmc.TrackID]bool{}
	var maxTS int64
	for it.Next() {
		tm := byID[it.Track()]
		fr := it.Frame()
		if ranged {
			if !hasSnap[tm.id] || fr.PTS < snapPTS[tm.id] {
				continue
			}
			if to, ok := toPTS[tm.id]; ok && opts.Range.To > 0 {
				if tm.video {
					if videoDone[tm.id] {
						continue
					}
					if fr.Keyframe && fr.PTS >= to {
						videoDone[tm.id] = true
						continue
					}
				} else if fr.PTS >= to {
					continue
				}
			}
		}
		p := Packet{Track: tm.number, Timestamp: toScale(tm.info, fr.PTS), Keyframe: fr.Keyframe}
		if tm.text {
			dur, txt, err := codec.DecodeTextFrame(fr.Data)
			if err != nil {
				continue // non-conforming payload: skip frame
			}
			p.Duration = toScale(tm.info, dur)
			p.Data = []byte(txt)
		} else {
			p.Data = fr.Data
		}
		if err := m.WritePacket(p); err != nil {
			return nil, err
		}
		if p.Timestamp > maxTS {
			maxTS = p.Timestamp
		}
		res.Frames++
	}
	if err := it.Err(); err != nil {
		return nil, err
	}
	if err := m.Finalize(float64(maxTS)); err != nil {
		return nil, err
	}
	return res, nil
}

// mapTrackOut converts a GMC TrackInfo (codec conventions) into a Matroska
// TrackEntry. ok=false when the codec is unsupported or the envelope fails
// to decode.
func mapTrackOut(info gmc.TrackInfo, number uint64) (TrackEntry, bool) {
	if !supported[info.Codec] {
		return TrackEntry{}, false
	}
	te := TrackEntry{Number: number, CodecID: info.Codec}
	switch info.Kind {
	case gmc.KindVideo:
		p, priv, err := codec.DecodeVideoPrivate(info.Private)
		if err != nil {
			return TrackEntry{}, false
		}
		te.Type = trackTypeVideo
		te.PixelWidth = uint64(p.Width)
		te.PixelHeight = uint64(p.Height)
		te.CodecPrivate = priv
	case gmc.KindAudio:
		p, priv, err := codec.DecodeAudioPrivate(info.Private)
		if err != nil {
			return TrackEntry{}, false
		}
		te.Type = trackTypeAudio
		te.SamplingFrequency = float64(p.SampleRate)
		if p.OutputSampleRate > 0 {
			te.OutputSamplingFrequency = float64(p.OutputSampleRate)
		}
		te.Channels = uint64(p.Channels)
		te.BitDepth = uint64(p.BitDepth)
		te.CodecPrivate = priv
	case gmc.KindData:
		priv, err := codec.DecodeTextPrivate(info.Private)
		if err != nil {
			return TrackEntry{}, false
		}
		te.Type = trackTypeSubtitle
		te.CodecPrivate = priv
	default:
		return TrackEntry{}, false
	}
	return te, true
}
