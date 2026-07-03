package gmc

import "io"

// Iterator walks Data chunks in storage order, filtered by track.
type Iterator struct {
	r      *Reader
	off    int64
	filter map[TrackID]bool // nil = all tracks
	track  TrackID
	frame  Frame
	err    error
}

// Next advances to the next matching frame. It returns false at the end of
// committed data or on error (check Err).
func (it *Iterator) Next() bool {
	if it.err != nil {
		return false
	}
	for {
		limit := it.r.committed.Load()
		typ, payload, next, err := readChunkAt(it.r.f, it.off, limit)
		if err == io.EOF {
			return false
		}
		if err != nil {
			it.err = err
			return false
		}
		it.off = next
		if typ != chunkData {
			continue
		}
		id, flags, pts, derr := decodeDataHeader(payload)
		if derr != nil {
			it.err = derr
			return false
		}
		if it.filter != nil && !it.filter[id] {
			continue
		}
		it.track = id
		it.frame = Frame{
			PTS:      pts,
			Keyframe: flags&flagKeyframe != 0,
			Data:     append([]byte(nil), payload[dataHeaderSize:]...),
		}
		return true
	}
}

// Frame returns the current frame. Valid after Next returned true.
func (it *Iterator) Frame() Frame { return it.frame }

// Track returns the current frame's track.
func (it *Iterator) Track() TrackID { return it.track }

// Err returns the first error encountered, if any.
func (it *Iterator) Err() error { return it.err }
