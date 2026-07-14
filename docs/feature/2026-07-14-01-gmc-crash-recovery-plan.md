# GMC 크래시 복구 `Repair` API 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 크래시로 footer를 못 쓴 `.gmc` 파일을 제자리에서 정상(footer 있는) 파일로 만드는 `gmc.Repair(path)` API를 무손실·멱등으로 추가한다.

**Architecture:** 기존 복구 스캔(`Reader.scan`)이 이미 `tracks`·`idx`·`committed`·`maxPTS`를 재구성한다. 여기에 트랙별 `firstPTS`/`frames` 두 값 집계만 추가하면 `Writer.Finalize`와 동일한 재료가 갖춰진다. `Repair`는 `O_RDWR`로 열어 스캔한 뒤, `committed`로 truncate하고 `encodeFooter`+`encodeTrailer`를 append한다. `Open`과 셋업을 공유하는 `newReaderFromFile(f)` 헬퍼로 중복을 없앤다.

**Tech Stack:** Go 1.25.8, 표준 라이브러리만 사용. 기존 `gmc` 패키지 내부 함수(`scan`·`encodeFooter`·`encodeTrailer`·`appendChunk`·`ptsToNano`) 재사용.

## Global Constraints

- Go 버전 하한: `go 1.25.8` (신규 의존성 추가 금지, 표준 라이브러리만).
- 패키지: `package gmc` (본 리포 `github.com/Youngju-Heo/go-container`, `gmc/` 디렉터리).
- 커밋 메시지·문서는 **한국어**, 코드 내 주석/로그/에러 메시지는 **영어**.
- 외과적 변경: 요청과 무관한 리팩터링·포맷팅 금지. `Reader.Summaries()`의 기존 공개 동작(복구 파일은 nil 반환)은 **불변**으로 유지.
- 커밋 끝에 다음 줄 추가: `Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>`
- 테스트 실행 표준: `go test ./gmc/ -run <TestName> -v`.

---

## 파일 구조

- **수정** `gmc/reader.go`
  - `Open`에서 파일 핸들 셋업 로직을 `newReaderFromFile(f *os.File) (*Reader, error)`로 추출(Task 1).
  - `Reader` 구조체에 `firstPTS`/`frames` 맵 추가, `scan`에서 집계(Task 2).
- **생성** `gmc/repair.go` — `RepairResult` 타입, `Repair(path)`, 내부 헬퍼(`recoveredFooter`/`repairResult`/`latestFrameTime`/`size`)(Task 3~6).
- **생성** `gmc/repair_test.go` — 전 태스크 테스트.

기존 테스트 헬퍼 `buildUnfinalized`·`writeTemp`·`countFrames`([gmc/reader_recover_test.go](../../gmc/reader_recover_test.go))는 같은 패키지라 재사용한다.

---

## Task 1: `newReaderFromFile` 헬퍼 추출 (순수 리팩터)

`Open`의 파일 셋업 로직을 `Repair`와 공유할 수 있도록 이미 열린 핸들을 받는 헬퍼로 분리한다. 동작 변화 없음 — 기존 테스트가 게이트다.

**Files:**
- Modify: `gmc/reader.go:34-80` (`Open`)

**Interfaces:**
- Produces: `func newReaderFromFile(f *os.File) (*Reader, error)` — 이미 열린 `*os.File`(ReadAt 지원)로 Reader를 구성하고, 유효 trailer면 footer 로드, 아니면 `scan` 복구. 에러 시 `f`를 닫는다.

- [ ] **Step 1: 리팩터 전 전체 스위트 green 확인 (baseline)**

Run: `go test ./gmc/`
Expected: `ok  github.com/Youngju-Heo/go-container/gmc` (전부 통과)

- [ ] **Step 2: `Open`을 헬퍼 호출로 교체하고 헬퍼를 추가**

[gmc/reader.go:34-80](../../gmc/reader.go#L34-L80)의 `Open` 함수 전체를 아래로 교체한다:

```go
// Open opens an existing GMC file. A valid trailer loads everything from the
// footer in one read; otherwise the file is recovered by a full CRC scan.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	return newReaderFromFile(f)
}

// newReaderFromFile builds a Reader over an already-open file handle (any open
// mode that supports ReadAt). It loads from the footer when a valid trailer is
// present, otherwise recovers by a full CRC scan. It closes f on any error.
func newReaderFromFile(f *os.File) (*Reader, error) {
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
```

- [ ] **Step 3: 스위트 재실행 — 여전히 green**

Run: `go test ./gmc/`
Expected: `ok` (동작 변화 없음, 전부 통과)

- [ ] **Step 4: 커밋**

```bash
git add gmc/reader.go
git commit -m "리팩터: Open의 파일 셋업을 newReaderFromFile 헬퍼로 추출

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 2: `scan`에 트랙별 `firstPTS`/`frames` 집계 추가

footer의 트랙 요약을 만들려면 스캔이 트랙별 첫 PTS와 프레임 수를 알아야 한다. 스캔은 전방(offset 오름차순)으로 진행하고 트랙 내 PTS는 비감소이므로, 트랙별 **첫 등장** data chunk의 pts가 곧 `firstPTS`다.

**Files:**
- Modify: `gmc/reader.go` (`Reader` 구조체 필드 추가, `newReaderFromFile`의 Reader 리터럴, `scan`의 `chunkData` 케이스)
- Test: `gmc/repair_test.go` (신규)

**Interfaces:**
- Consumes: `newReaderFromFile` (Task 1)
- Produces: `Reader.firstPTS map[TrackID]uint64` (트랙별 첫 data chunk pts), `Reader.frames map[TrackID]uint64` (트랙별 data chunk 수) — `scan` 경로에서 채워짐. footer 로드 경로에서는 빈 맵.

- [ ] **Step 1: 실패 테스트 작성**

`gmc/repair_test.go`를 새로 만들고 아래를 넣는다:

```go
package gmc

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestScanAggregatesFirstPTSAndFrames(t *testing.T) {
	// buildUnfinalized writes 20 video frames at pts 0,3000,...,57000 (no Finalize).
	data, video, _, _ := buildUnfinalized(t, 200)
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.frames[video] != 20 {
		t.Fatalf("frames[video] = %d, want 20", r.frames[video])
	}
	if r.firstPTS[video] != 0 {
		t.Fatalf("firstPTS[video] = %d, want 0", r.firstPTS[video])
	}
	if r.maxPTS[video] != 57000 {
		t.Fatalf("maxPTS[video] = %d, want 57000", r.maxPTS[video])
	}
}
```

> 참고: `bytes`/`os`/`filepath`/`reflect`/`time` import는 이후 태스크의 테스트에서 모두 쓰인다. 이 태스크만 단독 컴파일하면 미사용 import 에러가 나므로, Step 2를 이어서 진행해 컴파일을 맞춘 뒤 실행한다.

- [ ] **Step 2: `Reader` 구조체·리터럴·`scan` 수정**

(2a) [gmc/reader.go:14-30](../../gmc/reader.go#L14-L30) `Reader` 구조체의 `maxPTS` 필드 바로 아래에 두 필드를 추가:

```go
	maxPTS  map[TrackID]uint64 // per-track max committed pts (non-live readers)

	firstPTS map[TrackID]uint64 // per-track first data-chunk pts (recovery scan)
	frames   map[TrackID]uint64 // per-track data-chunk count (recovery scan)
```

(2b) `newReaderFromFile`의 `&Reader{...}` 리터럴(Task 1에서 만든 것)에서 `maxPTS:` 줄 아래에 초기화 추가:

```go
		maxPTS:      map[TrackID]uint64{},
		firstPTS:    map[TrackID]uint64{},
		frames:      map[TrackID]uint64{},
```

(2c) [gmc/reader.go:107-115](../../gmc/reader.go#L107-L115) `scan`의 `case chunkData:` 블록을 아래로 교체(첫 등장 시 `firstPTS` 기록, 매 프레임 `frames++` 추가):

```go
		case chunkData:
			if h, derr := decodeDataHeader(payload); derr == nil {
				if _, seen := r.frames[h.id]; !seen {
					r.firstPTS[h.id] = h.pts
				}
				r.frames[h.id]++
				if v, ok := r.maxPTS[h.id]; !ok || h.pts > v {
					r.maxPTS[h.id] = h.pts
				}
				if h.flags&flagKeyframe != 0 {
					tail = append(tail, cpEntry{h.id, h.pts, off})
				}
			}
```

- [ ] **Step 3: 테스트 실행 — 통과**

Run: `go test ./gmc/ -run TestScanAggregatesFirstPTSAndFrames -v`
Expected: PASS

- [ ] **Step 4: 회귀 없음 확인**

Run: `go test ./gmc/`
Expected: `ok` (전체 통과)

- [ ] **Step 5: 커밋**

```bash
git add gmc/reader.go gmc/repair_test.go
git commit -m "기능: 복구 스캔에 트랙별 firstPTS/frames 집계 추가

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 3: `Repair(path)` — 복구 happy path + `RepairResult`

크래시 파일을 복구해 정상 파일로 만들고, 완결된 `RepairResult`(트랙·PTS 요약·벽시계 포함)를 반환하는 핵심 경로를 구현한다.

**Files:**
- Create: `gmc/repair.go`
- Test: `gmc/repair_test.go` (추가)

**Interfaces:**
- Consumes: `newReaderFromFile` (Task 1); `Reader.firstPTS`/`frames`/`maxPTS`/`tracks`/`idx`/`committed`/`summaries` (Task 2 및 기존); `encodeFooter`·`encodeTrailer`·`appendChunk`·`ptsToNano`·`Reader.Tracks`·`Reader.StartTime`·`Reader.LastTime`·`chunkFooter`·`trackSummary`·`TrackSummary`(기존).
- Produces:
  - `func Repair(path string) (RepairResult, error)`
  - `type RepairResult struct { Repaired bool; Tracks []TrackInfo; Summaries []TrackSummary; Frames int64; Size int64; StartTime time.Time; LastTime time.Time }`
  - `func (r *Reader) recoveredFooter() ([]TrackInfo, []trackSummary)`
  - `func (r *Reader) repairResult(repaired bool) RepairResult`
  - `func (r *Reader) latestFrameTime(tracks []TrackInfo) time.Time`
  - `func (r *Reader) size() int64`

- [ ] **Step 1: 실패 테스트 작성**

`gmc/repair_test.go`에 헬퍼와 테스트를 추가:

```go
// buildCrashFile writes 20 video frames (pts 0..57000) plus a start-time tag
// and Closes WITHOUT Finalize, simulating a crash. Returns the on-disk path.
func buildCrashFile(t *testing.T, start time.Time) (path string, video TrackID) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "crash.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: 200, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetStartTime(start); err != nil {
		t.Fatal(err)
	}
	video, _ = w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	for i := 0; i < 20; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 50)})
	}
	if err := w.Close(); err != nil { // crash: Close without Finalize
		t.Fatal(err)
	}
	return path, video
}

func TestRepairFinalizesCrashFile(t *testing.T) {
	start := time.Unix(1700000000, 0)
	path, video := buildCrashFile(t, start)

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired {
		t.Fatal("Repaired = false, want true")
	}
	if res.Frames != 20 {
		t.Fatalf("Frames = %d, want 20", res.Frames)
	}
	if len(res.Summaries) != 1 || res.Summaries[0].FirstPTS != 0 ||
		res.Summaries[0].LastPTS != 57000 || res.Summaries[0].Frames != 20 {
		t.Fatalf("Summaries = %+v", res.Summaries)
	}
	if !res.StartTime.Equal(start) {
		t.Fatalf("StartTime = %v, want %v", res.StartTime, start)
	}
	wantLast := start.Add(time.Duration(ptsToNano(57000, res.Tracks[0])))
	if !res.LastTime.Equal(wantLast) {
		t.Fatalf("LastTime = %v, want %v", res.LastTime, wantLast)
	}

	// Reopen: must be finalized and fully readable.
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("reopened file not finalized")
	}
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames after repair = %d, want 20", got)
	}
	if _, ok := r.idx.seek(video, 57000); !ok {
		t.Fatal("sync point missing after repair")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestRepairFinalizesCrashFile -v`
Expected: FAIL — 컴파일 에러(`undefined: Repair`)

- [ ] **Step 3: `gmc/repair.go` 구현**

```go
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
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./gmc/ -run TestRepairFinalizesCrashFile -v`
Expected: PASS

- [ ] **Step 5: 커밋**

```bash
git add gmc/repair.go gmc/repair_test.go
git commit -m "기능: gmc.Repair — 크래시 파일 복구 및 재마감 (happy path)

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 4: 멱등성 + 이미-정상 no-op

`Repair`를 두 번 호출해도, 또는 정상 마감된 파일에 호출해도 파일을 바꾸지 않고 올바른 no-op 결과를 반환함을 검증한다.

**Files:**
- Test: `gmc/repair_test.go` (추가)

**Interfaces:**
- Consumes: `Repair`, `RepairResult`, `buildCrashFile` (Task 3)

- [ ] **Step 1: 실패 테스트 작성**

`gmc/repair_test.go`에 추가:

```go
func TestRepairIdempotent(t *testing.T) {
	path, _ := buildCrashFile(t, time.Unix(1700000000, 0))

	res1, err := Repair(path)
	if err != nil || !res1.Repaired {
		t.Fatalf("first repair: res=%+v err=%v", res1, err)
	}
	after1, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res2, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res2.Repaired {
		t.Fatal("second repair: Repaired = true, want false (already finalized)")
	}
	after2, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after1, after2) {
		t.Fatal("second repair modified the file")
	}
	if res2.Frames != 20 || res2.Size != int64(len(after2)) {
		t.Fatalf("no-op result: Frames=%d Size=%d fileSize=%d", res2.Frames, res2.Size, len(after2))
	}
}

func TestRepairAlreadyFinalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ok.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	w.SetStartTime(time.Unix(1700000000, 0))
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repaired {
		t.Fatal("Repaired = true on already-finalized file")
	}
	if res.Frames != 2 || len(res.Summaries) != 1 ||
		res.Summaries[0].Frames != 2 || res.Summaries[0].LastPTS != 3000 {
		t.Fatalf("result = %+v", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("repair modified an already-finalized file")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run 'TestRepairIdempotent|TestRepairAlreadyFinalized' -v`
Expected: FAIL 또는 통과 예상 — 이미 Task 3 구현이 이 동작을 커버하므로 **통과할 수 있다**. 통과하면 회귀 방지 테스트로 그대로 채택하고 Step 3으로 진행. (TDD상 새 코드가 필요 없는 검증 태스크다.)

- [ ] **Step 3: (필요 시) 구현 조정 후 통과 확인**

Run: `go test ./gmc/ -run 'TestRepairIdempotent|TestRepairAlreadyFinalized' -v`
Expected: PASS (실패했다면 `repairResult`/no-op 분기를 수정해 통과시킨다)

- [ ] **Step 4: 커밋**

```bash
git add gmc/repair_test.go
git commit -m "테스트: Repair 멱등성 및 이미-정상 파일 no-op 검증

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 5: 엣지 케이스 — 0프레임 / torn-tail

프레임이 하나도 없는 파일은 마감하지 않고 `Frames=0`으로 되돌리고(호출자가 삭제), 마지막 청크가 잘린 파일은 마지막 완전 프레임까지만 살려 재마감함을 검증한다.

**Files:**
- Test: `gmc/repair_test.go` (추가)

**Interfaces:**
- Consumes: `Repair`, `buildUnfinalized`·`writeTemp`·`countFrames` (기존 테스트 헬퍼)

- [ ] **Step 1: 실패 테스트 작성**

`gmc/repair_test.go`에 추가:

```go
func TestRepairZeroFrames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // crash before any frame
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if res.Repaired || res.Frames != 0 {
		t.Fatalf("result = %+v, want Repaired=false Frames=0", res)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("zero-frame repair modified the file")
	}
	// File must remain a valid, unfinalized GMC (no bogus footer written).
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Finalized() {
		t.Fatal("zero-frame file should remain unfinalized")
	}
}

func TestRepairTornTail(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30) // no checkpoint -> last chunk is Data
	path := writeTemp(t, data[:len(data)-3])    // torn: drop last 3 bytes

	res, err := Repair(path)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Repaired || res.Frames != 19 {
		t.Fatalf("result = %+v, want Repaired=true Frames=19", res)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("not finalized after repair")
	}
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19 (torn frame dropped)", got)
	}
}
```

- [ ] **Step 2: 실행 — 통과 확인 (Task 3 구현이 커버)**

Run: `go test ./gmc/ -run 'TestRepairZeroFrames|TestRepairTornTail' -v`
Expected: PASS (실패 시 0프레임/`committed` truncate 분기를 수정해 통과시킨다)

- [ ] **Step 3: 커밋**

```bash
git add gmc/repair_test.go
git commit -m "테스트: Repair 0프레임 및 torn-tail 엣지 케이스 검증

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## Task 6: `Finalize`와의 footer 동등성 (집계 정확성 앵커)

동일한 쓰기 시퀀스(프레임 0인 트랙 포함)를 하나는 정상 `Finalize`, 다른 하나는 `Close`+`Repair` 했을 때 footer의 트랙 요약(firstPTS/lastPTS/frames)이 **완전히 일치**함을 검증한다. Task 2의 집계 정확성을 못박는 핵심 테스트다.

**Files:**
- Test: `gmc/repair_test.go` (추가)

**Interfaces:**
- Consumes: `Repair`, `Reader.Summaries()`(기존), `TrackSummary`(기존)

- [ ] **Step 1: 실패 테스트 작성**

`gmc/repair_test.go`에 추가:

```go
// writeMixed writes an identical multi-track sequence (video + audio + an
// empty data track) and either Finalizes or Closes (crash) based on finalize.
func writeMixed(t *testing.T, path string, finalize bool) {
	t.Helper()
	w, err := Create(path, CreateOptions{CheckpointBytes: 200, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	w.SetStartTime(time.Unix(1700000000, 0))
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})
	w.AddTrack(TrackInfo{Kind: KindData, Codec: "meta", TimebaseNum: 1, TimebaseDen: 1000}) // no frames
	for i := 0; i < 12; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 40)})
	}
	for i := 0; i < 8; i++ {
		w.WriteFrame(audio, Frame{PTS: uint64(i * 1024), Keyframe: true, Data: make([]byte, 20)})
	}
	if finalize {
		if err := w.Finalize(); err != nil {
			t.Fatal(err)
		}
		return
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func footerSummaries(t *testing.T, path string) []TrackSummary {
	t.Helper()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if !r.Finalized() {
		t.Fatal("expected a finalized file")
	}
	sums, _ := r.Summaries()
	return sums
}

func TestRepairFooterMatchesFinalize(t *testing.T) {
	dir := t.TempDir()
	fin := filepath.Join(dir, "finalized.gmc")
	rep := filepath.Join(dir, "repaired.gmc")
	writeMixed(t, fin, true)
	writeMixed(t, rep, false)

	if _, err := Repair(rep); err != nil {
		t.Fatal(err)
	}

	want := footerSummaries(t, fin)
	got := footerSummaries(t, rep)
	if !reflect.DeepEqual(want, got) {
		t.Fatalf("summaries mismatch:\n finalize = %+v\n repair   = %+v", want, got)
	}
}
```

- [ ] **Step 2: 실행 — 통과 확인**

Run: `go test ./gmc/ -run TestRepairFooterMatchesFinalize -v`
Expected: PASS (불일치 시 `recoveredFooter` 집계가 `Finalize`의 `sums` 구성과 어긋난 지점을 수정)

- [ ] **Step 3: 전체 스위트 최종 확인**

Run: `go test ./gmc/`
Expected: `ok` (전체 통과)

- [ ] **Step 4: 커밋**

```bash
git add gmc/repair_test.go
git commit -m "테스트: Repair footer가 Finalize 결과와 동등함을 검증

Co-Authored-By: Claude Opus 4.8 (1M context) <noreply@anthropic.com>"
```

---

## 모든 태스크 완료 후 (수동/확인 필요)

- **최종 리뷰:** superpowers/requesting-code-review로 `gmc-crash-recovery` 브랜치 전체를 1회 리뷰(Opus). (CLAUDE.md §6)
- **버전 태그:** media-recorder(작업 B)가 `go.mod`에서 참조할 새 버전을 만들려면 이 리포에 git 태그가 필요하다. 태그 생성/푸시는 **사용자 확인 후** 진행한다(자동 금지).
- **작업 (B)는 별도 리포**(media-recorder)에서 진행 — 본 계획 범위 밖. 설계 §5 참조.

---

## Self-Review 결과

- **Spec 커버리지:** 설계 §4(API·동작순서·순증로직) → Task 1~3, §6 엣지케이스(이미-finalized/0프레임/torn/부분마감) → Task 4·5, §3 집계 정확성 → Task 2·6. 모두 태스크로 매핑됨.
- **Placeholder:** 없음 — 모든 코드 스텝에 실제 코드 포함.
- **타입 일관성:** `RepairResult`/`TrackSummary`/`trackSummary` 필드명, `recoveredFooter`/`repairResult`/`latestFrameTime`/`size` 시그니처가 정의부(Task 3)와 사용부(Task 3~6) 전반에서 일치. `firstPTS`/`frames` 필드는 Task 2에서 정의, Task 3에서 사용.
- **범위:** 단일 구현 계획으로 적정(gmc 라이브러리 작업 A 한정).
