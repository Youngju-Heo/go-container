package gmc

import (
	"encoding/binary"
	"errors"
	"os"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Frame is one media frame / sample / metadata event.
type Frame struct {
	PTS      uint64
	Keyframe bool
	Data     []byte
}

// CreateOptions configures a new file.
type CreateOptions struct {
	Private            []byte        // file-level private data, immutable
	TagsAreaSize       int           // total tags area size (2 slots); default 8 KiB
	CheckpointBytes    int64         // checkpoint trigger by bytes; default 8 MiB
	CheckpointInterval time.Duration // checkpoint trigger by time; default 1s
}

type trackState struct {
	info          TrackInfo
	hasLast       bool
	firstPTS      uint64
	lastPTS       uint64
	frames        uint64
	indexedThisCP bool
}

// shouldIndexSync reports whether the current keyframe should become an index
// entry. Audio tracks are sampled to one entry per checkpoint interval; all
// other kinds index every keyframe.
func (ts *trackState) shouldIndexSync() bool {
	if ts.info.Kind != KindAudio {
		return true
	}
	if ts.indexedThisCP {
		return false
	}
	ts.indexedThisCP = true
	return true
}

// Writer appends frames to a GMC file. Safe for concurrent use; all writes
// are serialized by an internal mutex.
type Writer struct {
	mu   sync.Mutex
	cond *sync.Cond

	f       *os.File
	path    string
	private []byte
	closed  bool

	committed   atomic.Int64
	streamStart int64

	tagsOff  int64
	slotSize int
	tagsSeq  uint64
	nextSlot int
	tags     map[string][]byte

	tracks    map[TrackID]*trackState
	nextTrack TrackID
	idx       *fileIndex

	pending    []cpEntry
	prevCPOff  int64
	lastCPEnd  int64
	lastCPTime time.Time
	cpBytes    int64
	cpInterval time.Duration

	scratch  []byte
	chunkBuf []byte
}

// Create creates a new GMC file. It fails if the file already exists.
func Create(path string, opts CreateOptions) (*Writer, error) {
	if opts.TagsAreaSize == 0 {
		opts.TagsAreaSize = defaultTagsAreaSize
	}
	if opts.TagsAreaSize < 256 || opts.TagsAreaSize%2 != 0 {
		return nil, errors.New("gmc: tags area size must be even and >= 256")
	}
	if opts.CheckpointBytes == 0 {
		opts.CheckpointBytes = 8 << 20
	}
	if opts.CheckpointInterval == 0 {
		opts.CheckpointInterval = time.Second
	}
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, err
	}
	hdr := encodeFileHeader(fileHeader{tagsAreaLen: uint32(opts.TagsAreaSize), private: opts.Private})
	if _, err := f.Write(append(hdr, make([]byte, opts.TagsAreaSize)...)); err != nil {
		f.Close()
		return nil, err
	}
	w := &Writer{
		f:           f,
		path:        path,
		private:     opts.Private,
		streamStart: int64(len(hdr) + opts.TagsAreaSize),
		tagsOff:     int64(len(hdr)),
		slotSize:    opts.TagsAreaSize / 2,
		tags:        make(map[string][]byte),
		tracks:      make(map[TrackID]*trackState),
		nextTrack:   1,
		idx:         newFileIndex(),
		cpBytes:     opts.CheckpointBytes,
		cpInterval:  opts.CheckpointInterval,
		lastCPTime:  time.Now(),
	}
	w.lastCPEnd = w.streamStart
	w.committed.Store(w.streamStart)
	w.cond = sync.NewCond(&w.mu)
	return w, nil
}

// appendChunkLocked frames and appends one chunk, then advances committedSize
// and wakes tail followers. Callers must hold w.mu.
func (w *Writer) appendChunkLocked(typ byte, payload []byte) error {
	w.chunkBuf = appendChunk(w.chunkBuf[:0], typ, payload)
	end := w.committed.Load()
	if _, err := w.f.WriteAt(w.chunkBuf, end); err != nil {
		return err
	}
	w.committed.Store(end + int64(len(w.chunkBuf)))
	w.cond.Broadcast()
	return nil
}

// AddTrack registers a new track. Must be called before writing any frame of
// that track.
func (w *Writer) AddTrack(info TrackInfo) (TrackID, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return 0, ErrClosed
	}
	if info.TimebaseNum == 0 || info.TimebaseDen == 0 {
		return 0, errors.New("gmc: timebase must be non-zero")
	}
	info.ID = w.nextTrack
	if err := w.appendChunkLocked(chunkTrackInfo, encodeTrackInfo(info)); err != nil {
		return 0, err
	}
	w.nextTrack++
	w.tracks[info.ID] = &trackState{info: info}
	return info.ID, nil
}

// WriteFrame appends one frame. PTS must be non-decreasing within a track.
func (w *Writer) WriteFrame(id TrackID, fr Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	ts, ok := w.tracks[id]
	if !ok {
		return ErrUnknownTrack
	}
	if ts.hasLast && fr.PTS < ts.lastPTS {
		return ErrNonMonotonicPTS
	}
	var flags byte
	if fr.Keyframe {
		flags |= flagKeyframe
	}
	w.scratch = encodeDataPayload(w.scratch[:0], id, flags, fr.PTS, fr.Data)
	off := w.committed.Load()
	if err := w.appendChunkLocked(chunkData, w.scratch); err != nil {
		return err
	}
	if !ts.hasLast {
		ts.firstPTS = fr.PTS
	}
	ts.hasLast = true
	ts.lastPTS = fr.PTS
	ts.frames++
	if fr.Keyframe && ts.shouldIndexSync() {
		w.idx.add(id, fr.PTS, off)
		w.pending = append(w.pending, cpEntry{id, fr.PTS, off})
	}
	return w.maybeCheckpointLocked()
}

// maybeCheckpointLocked writes an IndexCheckpoint chunk when the byte or time
// threshold since the last checkpoint has been reached. Callers hold w.mu.
func (w *Writer) maybeCheckpointLocked() error {
	if w.committed.Load()-w.lastCPEnd < w.cpBytes && time.Since(w.lastCPTime) < w.cpInterval {
		return nil
	}
	if len(w.pending) == 0 {
		w.lastCPTime = time.Now()
		w.lastCPEnd = w.committed.Load()
		for _, ts := range w.tracks {
			ts.indexedThisCP = false
		}
		return nil
	}
	off := w.committed.Load()
	payload := encodeCheckpoint(w.prevCPOff, w.pending)
	if err := w.appendChunkLocked(chunkCheckpoint, payload); err != nil {
		return err
	}
	w.prevCPOff = off
	w.pending = w.pending[:0]
	w.lastCPTime = time.Now()
	w.lastCPEnd = w.committed.Load()
	for _, ts := range w.tracks {
		ts.indexedThisCP = false
	}
	return nil
}

// SetTag adds or updates one session tag. The full snapshot is rewritten into
// the inactive slot of the tags area (ping-pong), so a torn write can never
// destroy the previous value.
func (w *Writer) SetTag(key string, value []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	next := make(map[string][]byte, len(w.tags)+1)
	for k, v := range w.tags {
		next[k] = v
	}
	next[key] = append([]byte(nil), value...)
	buf := encodeTagsSlot(w.tagsSeq+1, next)
	if len(buf) > w.slotSize {
		return ErrTagsTooLarge
	}
	off := w.tagsOff + int64(w.nextSlot)*int64(w.slotSize)
	if _, err := w.f.WriteAt(buf, off); err != nil {
		return err
	}
	w.tagsSeq++
	w.nextSlot = 1 - w.nextSlot
	w.tags = next
	return nil
}

// SetStartTime stores the absolute wall-clock time of pts 0 (all tracks share
// the same time origin) under the TagStartTime key.
func (w *Writer) SetStartTime(t time.Time) error {
	var v [8]byte
	binary.LittleEndian.PutUint64(v[:], uint64(t.UnixNano()))
	return w.SetTag(TagStartTime, v[:])
}

// NewReader returns a reader that shares this writer's in-memory index and
// committed size, over its own read-only file handle.
func (w *Writer) NewReader() (*Reader, error) {
	f, err := os.Open(w.path)
	if err != nil {
		return nil, err
	}
	return &Reader{
		f:           f,
		w:           w,
		idx:         w.idx,
		committed:   &w.committed,
		streamStart: w.streamStart,
	}, nil
}

// tagsSnapshot returns a copy of the current tags map.
func (w *Writer) tagsSnapshot() map[string][]byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := make(map[string][]byte, len(w.tags))
	for k, v := range w.tags {
		out[k] = v
	}
	return out
}

// Sync flushes file contents to stable storage.
func (w *Writer) Sync() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	return w.f.Sync()
}

// Close closes the file without writing a footer. The file remains a valid
// unfinalized GMC file and reopens through the scan-recovery path.
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	w.closed = true
	w.cond.Broadcast()
	return w.f.Close()
}

// Finalize writes the consolidated footer and trailer, syncs, and closes the
// file. The footer is a convenience cache: the file is fully readable through
// the scan path even without it.
func (w *Writer) Finalize() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return ErrClosed
	}
	ids := make([]TrackID, 0, len(w.tracks))
	for id := range w.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tracks := make([]TrackInfo, 0, len(ids))
	sums := make([]trackSummary, 0, len(ids))
	for _, id := range ids {
		ts := w.tracks[id]
		tracks = append(tracks, ts.info)
		sums = append(sums, trackSummary{track: id, firstPTS: ts.firstPTS, lastPTS: ts.lastPTS, frames: ts.frames})
	}
	footerOff := w.committed.Load()
	if err := w.appendChunkLocked(chunkFooter, encodeFooter(tracks, sums, w.idx.dump())); err != nil {
		return err
	}
	if _, err := w.f.WriteAt(encodeTrailer(footerOff), w.committed.Load()); err != nil {
		return err
	}
	if err := w.f.Sync(); err != nil {
		return err
	}
	w.closed = true
	w.cond.Broadcast()
	return w.f.Close()
}
