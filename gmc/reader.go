package gmc

import (
	"encoding/binary"
	"os"
	"sort"
	"sync/atomic"
	"time"
)

// Reader provides random access and tailing over a GMC file. Live readers
// (from Writer.NewReader) share the writer's in-memory index and committed
// size; opened readers own an index built from the footer or a recovery scan.
type Reader struct {
	f *os.File
	w *Writer // non-nil for live readers

	idx         *fileIndex
	committed   *atomic.Int64
	streamStart int64

	private []byte
	tags    map[string][]byte
	tracks  map[TrackID]TrackInfo
	maxPTS  map[TrackID]uint64 // per-track max committed pts (non-live readers)

	finalized bool

	summaries []trackSummary // footer per-track summary; nil for recovered files
}

// Open opens an existing GMC file. A valid trailer loads everything from the
// footer in one read; otherwise the file is recovered by a full CRC scan.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	hdr, headerLen, err := decodeFileHeader(f)
	if err != nil {
		f.Close()
		return nil, err
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	size := fi.Size()
	streamStart := headerLen + int64(hdr.tagsAreaLen)
	if size < streamStart {
		f.Close()
		return nil, ErrCorrupt
	}
	area := make([]byte, hdr.tagsAreaLen)
	if _, err := f.ReadAt(area, headerLen); err != nil {
		f.Close()
		return nil, err
	}
	tags, _, _ := pickTagsSlot(area)
	if tags == nil {
		tags = map[string][]byte{}
	}
	r := &Reader{
		f:           f,
		idx:         newFileIndex(),
		committed:   new(atomic.Int64),
		streamStart: streamStart,
		private:     hdr.private,
		tags:        tags,
		tracks:      map[TrackID]TrackInfo{},
		maxPTS:      map[TrackID]uint64{},
	}
	if err := r.loadFooter(size); err != nil {
		r.scan(size)
	} else {
		r.finalized = true
	}
	return r, nil
}

// scan recovers an unfinalized or crashed file by a forward pass that
// CRC-verifies every chunk (payload included — page-cache flush order gives
// no ordering guarantee, so headers alone cannot be trusted). The first
// invalid chunk marks the logical EOF. Sync points after the last checkpoint
// are collected from Data chunk keyframe flags.
func (r *Reader) scan(size int64) {
	off := r.streamStart
	var tail []cpEntry
	for {
		typ, payload, next, err := readChunkAt(r.f, off, size)
		if err != nil {
			break // io.EOF or corruption: off is the logical EOF
		}
		switch typ {
		case chunkTrackInfo:
			if info, _, derr := decodeTrackInfo(payload); derr == nil {
				r.tracks[info.ID] = info
			}
		case chunkCheckpoint:
			if _, entries, derr := decodeCheckpoint(payload); derr == nil {
				for _, e := range entries {
					r.idx.add(e.track, e.pts, e.off)
				}
			}
			tail = tail[:0] // everything before this checkpoint is covered
		case chunkData:
			if h, derr := decodeDataHeader(payload); derr == nil {
				if v, ok := r.maxPTS[h.id]; !ok || h.pts > v {
					r.maxPTS[h.id] = h.pts
				}
				if h.flags&flagKeyframe != 0 {
					tail = append(tail, cpEntry{h.id, h.pts, off})
				}
			}
		default:
			// unknown chunk type: skip for forward compatibility
		}
		off = next
	}
	for _, e := range tail {
		r.idx.add(e.track, e.pts, e.off)
	}
	r.committed.Store(off)
}

// loadFooter validates the trailer and loads tracks + index from the footer.
func (r *Reader) loadFooter(size int64) error {
	if size < r.streamStart+trailerSize {
		return ErrCorrupt
	}
	var tb [trailerSize]byte
	if _, err := r.f.ReadAt(tb[:], size-trailerSize); err != nil {
		return err
	}
	footerOff, ok := decodeTrailer(tb[:])
	if !ok || footerOff < r.streamStart || footerOff >= size-trailerSize {
		return ErrCorrupt
	}
	typ, payload, next, err := readChunkAt(r.f, footerOff, size-trailerSize)
	if err != nil || typ != chunkFooter || next != size-trailerSize {
		return ErrCorrupt
	}
	tracks, sums, entries, err := decodeFooter(payload)
	if err != nil {
		return err
	}
	for _, tr := range tracks {
		r.tracks[tr.ID] = tr
	}
	r.summaries = sums
	for _, s := range sums {
		if s.frames > 0 {
			r.maxPTS[s.track] = s.lastPTS // stores maxPTS since task 2
		}
	}
	for _, e := range entries {
		r.idx.add(e.track, e.pts, e.off)
	}
	r.committed.Store(footerOff)
	return nil
}

// Tracks returns all tracks ordered by ID.
func (r *Reader) Tracks() []TrackInfo {
	var out []TrackInfo
	if r.w != nil {
		r.w.mu.Lock()
		for _, ts := range r.w.tracks {
			out = append(out, ts.info)
		}
		r.w.mu.Unlock()
	} else {
		for _, info := range r.tracks {
			out = append(out, info)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FilePrivate returns the immutable file-level private data.
func (r *Reader) FilePrivate() []byte {
	if r.w != nil {
		return r.w.private
	}
	return r.private
}

// Tags returns the latest session tags snapshot.
func (r *Reader) Tags() map[string][]byte {
	if r.w != nil {
		return r.w.tagsSnapshot()
	}
	out := make(map[string][]byte, len(r.tags))
	for k, v := range r.tags {
		out[k] = append([]byte(nil), v...)
	}
	return out
}

// StartTime decodes the TagStartTime tag if present.
func (r *Reader) StartTime() (time.Time, bool) {
	v, ok := r.Tags()[TagStartTime]
	if !ok || len(v) != 8 {
		return time.Time{}, false
	}
	return time.Unix(0, int64(binary.LittleEndian.Uint64(v))), true
}

func (r *Reader) hasTrack(id TrackID) bool {
	if r.w != nil {
		r.w.mu.Lock()
		defer r.w.mu.Unlock()
		_, ok := r.w.tracks[id]
		return ok
	}
	_, ok := r.tracks[id]
	return ok
}

// trackInfo returns the TrackInfo for id from the live writer or the opened
// snapshot.
func (r *Reader) trackInfo(id TrackID) (TrackInfo, bool) {
	if r.w != nil {
		r.w.mu.Lock()
		defer r.w.mu.Unlock()
		ts, ok := r.w.tracks[id]
		if !ok {
			return TrackInfo{}, false
		}
		return ts.info, true
	}
	info, ok := r.tracks[id]
	return info, ok
}

// LastPTS returns the highest committed pts of the track. For live readers it
// reflects exactly the frames written so far; for opened files it comes from
// the footer summary or the recovery scan. ok is false when the track is
// unknown or has no frames.
func (r *Reader) LastPTS(id TrackID) (uint64, bool) {
	if r.w != nil {
		r.w.mu.Lock()
		defer r.w.mu.Unlock()
		ts, ok := r.w.tracks[id]
		if !ok || !ts.hasLast {
			return 0, false
		}
		return ts.maxPTS, true
	}
	v, ok := r.maxPTS[id]
	return v, ok
}

// LastTime returns the absolute wall-clock time of LastPTS:
// StartTime + LastPTS×timebase. ok is false without a start time, an unknown
// track, or an empty track.
func (r *Reader) LastTime(id TrackID) (time.Time, bool) {
	start, ok := r.StartTime()
	if !ok {
		return time.Time{}, false
	}
	pts, ok := r.LastPTS(id)
	if !ok {
		return time.Time{}, false
	}
	info, ok := r.trackInfo(id)
	if !ok {
		return time.Time{}, false
	}
	return start.Add(time.Duration(ptsToNano(pts, info))), true
}

func (r *Reader) trackIDs() []TrackID {
	tracks := r.Tracks()
	ids := make([]TrackID, len(tracks))
	for i, tr := range tracks {
		ids[i] = tr.ID
	}
	return ids
}

// SeekPTS positions an iterator at the last sync point at or before pts on
// the given track. The iterator yields frames from the sync point onward, so
// callers receive the decode warm-up frames before the target pts.
func (r *Reader) SeekPTS(id TrackID, pts uint64) (*Iterator, error) {
	if !r.hasTrack(id) {
		return nil, ErrUnknownTrack
	}
	off, ok := r.idx.seek(id, pts)
	if !ok {
		off = r.streamStart
	}
	return &Iterator{r: r, off: off, filter: map[TrackID]bool{id: true}}, nil
}

// ReadInterleaved iterates frames of the given tracks (all tracks when none
// specified) in storage order, starting at the minimum offset among each
// track's last sync point at or before pts — so no track misses its own
// sync point for the target position.
func (r *Reader) ReadInterleaved(pts uint64, tracks ...TrackID) (*Iterator, error) {
	ids := tracks
	if len(ids) == 0 {
		ids = r.trackIDs()
	}
	filter := make(map[TrackID]bool, len(ids))
	start := r.committed.Load()
	for _, id := range ids {
		if !r.hasTrack(id) {
			return nil, ErrUnknownTrack
		}
		filter[id] = true
		off, ok := r.idx.seek(id, pts)
		if !ok {
			off = r.streamStart
		}
		if off < start {
			start = off
		}
	}
	return &Iterator{r: r, off: start, filter: filter}, nil
}

// SeekTime positions an interleaved iterator at the absolute wall-clock time
// t, converting it into each track's timebase (all tracks when none given).
// Placement follows ReadInterleaved: the minimum offset among each track's
// last sync point at or before the converted pts. Requires the
// gmc.start_time_unix_ns tag; returns ErrNoStartTime otherwise. Times before
// the start clamp to the beginning of the stream.
func (r *Reader) SeekTime(t time.Time, tracks ...TrackID) (*Iterator, error) {
	start, ok := r.StartTime()
	if !ok {
		return nil, ErrNoStartTime
	}
	var dns uint64
	if d := t.UnixNano() - start.UnixNano(); d > 0 {
		dns = uint64(d)
	}
	ids := tracks
	if len(ids) == 0 {
		ids = r.trackIDs()
	}
	filter := make(map[TrackID]bool, len(ids))
	startOff := r.committed.Load()
	for _, id := range ids {
		info, ok := r.trackInfo(id)
		if !ok {
			return nil, ErrUnknownTrack
		}
		filter[id] = true
		off, found := r.idx.seek(id, nanoToPTS(dns, info))
		if !found {
			off = r.streamStart
		}
		if off < startOff {
			startOff = off
		}
	}
	return &Iterator{r: r, off: startOff, filter: filter}, nil
}

// Finalized reports whether the file was properly closed with a footer and
// trailer. Live readers always report false (the file is still being written).
func (r *Reader) Finalized() bool {
	return r.finalized
}

// TrackSummary is a per-track storage summary derived from the footer.
type TrackSummary struct {
	Track    TrackID
	FirstPTS uint64
	LastPTS  uint64
	Frames   uint64
}

// Summaries returns per-track footer summaries and the total number of sync
// points in the index. For finalized files the summaries come from the footer
// (Frames accurate). For recovered (non-finalized) files there is no footer,
// so the summary slice is nil; the sync-point count still reflects the
// recovery scan.
func (r *Reader) Summaries() ([]TrackSummary, int) {
	sync := r.idx.count()
	if r.summaries == nil {
		return nil, sync
	}
	out := make([]TrackSummary, len(r.summaries))
	for i, s := range r.summaries {
		out[i] = TrackSummary{Track: s.track, FirstPTS: s.firstPTS, LastPTS: s.lastPTS, Frames: s.frames}
	}
	return out, sync
}

// Close closes the reader's file handle.
func (r *Reader) Close() error {
	return r.f.Close()
}
