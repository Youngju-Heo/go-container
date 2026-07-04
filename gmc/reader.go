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
	}
	if err := r.loadFooter(size); err != nil {
		r.scan(size)
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
			if h, derr := decodeDataHeader(payload); derr == nil && h.flags&flagKeyframe != 0 {
				tail = append(tail, cpEntry{h.id, h.pts, off})
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
	tracks, _, entries, err := decodeFooter(payload)
	if err != nil {
		return err
	}
	for _, tr := range tracks {
		r.tracks[tr.ID] = tr
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

// Close closes the reader's file handle.
func (r *Reader) Close() error {
	return r.f.Close()
}
