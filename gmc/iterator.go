package gmc

import (
	"context"
	"io"
)

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

// TrackFrame is one frame delivered by Follow, tagged with its track.
type TrackFrame struct {
	Track TrackID
	Frame Frame
}

// Follow tails the file from the current committed position, delivering
// frames of the given tracks (all when none specified) as they are committed.
// The channel closes when the writer finalizes/closes and all remaining data
// has been delivered, or when ctx is canceled. On a non-live reader it drains
// existing data and closes.
func (r *Reader) Follow(ctx context.Context, tracks ...TrackID) <-chan TrackFrame {
	var filter map[TrackID]bool
	if len(tracks) > 0 {
		filter = make(map[TrackID]bool, len(tracks))
		for _, id := range tracks {
			filter[id] = true
		}
	}
	ch := make(chan TrackFrame)
	start := r.streamStart
	if r.w != nil {
		start = r.committed.Load()
	}
	go r.follow(ctx, ch, start, filter)
	return ch
}

func (r *Reader) follow(ctx context.Context, ch chan<- TrackFrame, off int64, filter map[TrackID]bool) {
	defer close(ch)
	if r.w != nil {
		// wake the cond wait below when ctx is canceled
		stop := context.AfterFunc(ctx, func() {
			r.w.mu.Lock()
			r.w.cond.Broadcast()
			r.w.mu.Unlock()
		})
		defer stop()
	}
	for {
		limit := r.committed.Load()
		for off < limit {
			typ, payload, next, err := readChunkAt(r.f, off, limit)
			if err != nil {
				return
			}
			off = next
			if typ != chunkData {
				continue
			}
			id, flags, pts, derr := decodeDataHeader(payload)
			if derr != nil {
				return
			}
			if filter != nil && !filter[id] {
				continue
			}
			tf := TrackFrame{Track: id, Frame: Frame{
				PTS:      pts,
				Keyframe: flags&flagKeyframe != 0,
				Data:     append([]byte(nil), payload[dataHeaderSize:]...),
			}}
			select {
			case ch <- tf:
			case <-ctx.Done():
				return
			}
		}
		if r.w == nil {
			return // opened file: drained
		}
		r.w.mu.Lock()
		for r.committed.Load() == off && !r.w.closed && ctx.Err() == nil {
			r.w.cond.Wait()
		}
		writerClosed := r.w.closed
		r.w.mu.Unlock()
		if ctx.Err() != nil {
			return
		}
		if writerClosed && r.committed.Load() == off {
			return
		}
	}
}
