package gmc

import (
	"sort"
	"sync"
)

type indexEntry struct {
	pts uint64
	off int64
}

// fileIndex is the shared in-memory sync-point index: per track, a slice of
// (pts, offset) entries in ascending pts order. The writer appends (pts is
// monotonic per track), readers do binary-search lookups.
type fileIndex struct {
	mu     sync.RWMutex
	tracks map[TrackID][]indexEntry
}

func newFileIndex() *fileIndex {
	return &fileIndex{tracks: make(map[TrackID][]indexEntry)}
}

func (ix *fileIndex) add(id TrackID, pts uint64, off int64) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	ix.tracks[id] = append(ix.tracks[id], indexEntry{pts, off})
}

// seek returns the offset of the last entry with entry.pts <= pts.
func (ix *fileIndex) seek(id TrackID, pts uint64) (int64, bool) {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	es := ix.tracks[id]
	i := sort.Search(len(es), func(i int) bool { return es[i].pts > pts })
	if i == 0 {
		return 0, false
	}
	return es[i-1].off, true
}

// dump returns every entry ordered by track id then pts, for the footer.
func (ix *fileIndex) dump() []cpEntry {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	ids := make([]TrackID, 0, len(ix.tracks))
	for id := range ix.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	var out []cpEntry
	for _, id := range ids {
		for _, e := range ix.tracks[id] {
			out = append(out, cpEntry{id, e.pts, e.off})
		}
	}
	return out
}
