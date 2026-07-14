package gmc

import (
	"os"
	"sort"
	"time"
)

// RepairResult reports the outcome of Repair.
type RepairResult struct {
	Repaired  bool           // false = already finalized or zero frames (file unchanged)
	Tracks    []TrackInfo    // tracks ordered by ID
	Summaries []TrackSummary // per-track firstPTS/lastPTS/frames (PTS-based, always accurate)
	Frames    int64          // total data frames recovered (sum of Summaries frames)
	Size      int64          // file size in bytes after repair
	StartTime time.Time      // wall-clock of pts 0 from TagStartTime; zero if the tag is absent
	LastTime  time.Time      // wall-clock of the last frame; zero without a start time
}

// Repair turns a GMC file that crashed before finalization into a normal
// footer-backed file, in place and without rewriting frame data. It is a no-op
// (Repaired=false) when the file is already finalized or holds zero valid
// frames. Repair is idempotent and lossless: it only truncates the incomplete
// bytes past the last valid frame, then appends a footer and trailer.
func Repair(path string) (RepairResult, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return RepairResult{}, err
	}
	r, err := newReaderFromFile(f) // closes f on error
	if err != nil {
		return RepairResult{}, err
	}
	defer r.Close()

	if r.finalized {
		return r.repairResult(false), nil
	}

	tracks, sums := r.recoveredFooter()
	var frames int64
	for _, s := range sums {
		frames += int64(s.frames)
	}
	if frames == 0 {
		return RepairResult{Repaired: false, Frames: 0}, nil
	}

	footerOff := r.committed.Load()
	if err := f.Truncate(footerOff); err != nil {
		return RepairResult{}, err
	}
	chunk := appendChunk(nil, chunkFooter, encodeFooter(tracks, sums, r.idx.dump()))
	if _, err := f.WriteAt(chunk, footerOff); err != nil {
		return RepairResult{}, err
	}
	if _, err := f.WriteAt(encodeTrailer(footerOff), footerOff+int64(len(chunk))); err != nil {
		return RepairResult{}, err
	}
	if err := f.Sync(); err != nil {
		return RepairResult{}, err
	}
	r.finalized = true
	return r.repairResult(true), nil
}

// recoveredFooter builds the footer track list and per-track summaries from a
// recovery scan, mirroring Writer.Finalize: all tracks in ID order, with
// zero-valued summaries for tracks that carry no frames.
func (r *Reader) recoveredFooter() ([]TrackInfo, []trackSummary) {
	ids := make([]TrackID, 0, len(r.tracks))
	for id := range r.tracks {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	tracks := make([]TrackInfo, 0, len(ids))
	sums := make([]trackSummary, 0, len(ids))
	for _, id := range ids {
		tracks = append(tracks, r.tracks[id])
		sums = append(sums, trackSummary{
			track:    id,
			firstPTS: r.firstPTS[id],
			lastPTS:  r.maxPTS[id],
			frames:   r.frames[id],
		})
	}
	return tracks, sums
}

// repairResult assembles the public result from the reader state. Summaries
// come from the footer for finalized files and from the recovery scan
// otherwise. Wall-clock times are populated only when TagStartTime is present.
func (r *Reader) repairResult(repaired bool) RepairResult {
	tracks := r.Tracks()
	sums := r.summaries // footer summaries; nil for recovered files
	if sums == nil {
		_, sums = r.recoveredFooter()
	}
	out := make([]TrackSummary, len(sums))
	var frames int64
	for i, s := range sums {
		out[i] = TrackSummary{Track: s.track, FirstPTS: s.firstPTS, LastPTS: s.lastPTS, Frames: s.frames}
		frames += int64(s.frames)
	}
	res := RepairResult{
		Repaired:  repaired,
		Tracks:    tracks,
		Summaries: out,
		Frames:    frames,
		Size:      r.size(),
	}
	if start, ok := r.StartTime(); ok {
		res.StartTime = start
		res.LastTime = r.latestFrameTime(tracks)
	}
	return res
}

// latestFrameTime returns the largest per-track LastTime across tracks, or the
// zero time when no track has a wall-clock last frame.
func (r *Reader) latestFrameTime(tracks []TrackInfo) time.Time {
	var last time.Time
	for _, tr := range tracks {
		if t, ok := r.LastTime(tr.ID); ok && t.After(last) {
			last = t
		}
	}
	return last
}

// size returns the current file size, or 0 if it cannot be determined.
func (r *Reader) size() int64 {
	if fi, err := r.f.Stat(); err == nil {
		return fi.Size()
	}
	return 0
}
