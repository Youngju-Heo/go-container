# media-info 유틸리티 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** gmc/mkv 파일의 저장 상태(타이틀·헤더·미디어·태그·인덱스 요약)를 well-formed JSON으로 출력하는 CLI `media-info`를 구현한다.

**Architecture:** `cmd/media-info` 실행 파일이 옵션을 파싱하고, 확장자+매직바이트로 포맷을 감지한 뒤 gmc/mkv 수집기를 호출한다. 각 수집기는 라이브러리(`gmc.Reader`/`mkv.Demuxer`)의 공개 API만으로 공통 봉투(`Report`)를 채우고, `encoding/json`으로 정렬 출력한다. 인덱스 요약은 추가 패킷 스캔 없이 메타데이터(gmc footer)로만 산출하며, mkv 인덱스는 null이다.

**Tech Stack:** Go 1.25, 표준 라이브러리(`flag`, `encoding/json`, `encoding/hex`, `unicode/utf8`, `time`), 기존 `gmc`/`mkv` 패키지.

## Global Constraints

- 모듈 경로: `github.com/Youngju-Heo/go-container` (import는 이 경로 기준).
- Go 버전 하한: `go 1.25.8` (go.mod).
- 문서·커밋 메시지는 한국어. 코드 내부 로그/에러 메시지는 영어식 표현.
- 설계 문서: `docs/feature/2026-07-07-01-media-info-design.md` (본 계획의 근거).
- 실행 파일명 `media-info`, 배치 `cmd/media-info/`.
- 라이브러리(`gmc`/`mkv`) 변경은 요구 충족에 필요한 최소 표적 변경만. 무관한 리팩터링 금지.
- JSON은 2-space indent. stdout에는 JSON만, 오류·진단은 stderr + 비정상 종료코드.
- 개발 환경은 windows/arm64로 `-race` 미지원. 테스트는 `go test ./...`로 실행(‑race 제외).

---

## 파일 구조

**라이브러리 변경 (기존 파일):**
- `gmc/format.go` — 수정: exported `Version` 상수 추가.
- `gmc/index.go` — 수정: `fileIndex.count()` 추가.
- `gmc/reader.go` — 수정: `summaries` 필드 보존, `TrackSummary` 타입 + `Summaries()` 메서드 추가.
- `gmc/summary_test.go` — 신규: `Summaries()`/`Version` 테스트.
- `mkv/ids.go` — 수정: `idTitle` 상수 추가.
- `mkv/demux.go` — 수정: `FileInfo.Title` 필드 + `parseInfo`의 `idTitle` 케이스.
- `mkv/demux_test.go` — 수정: 픽스처에 Title 요소 + 파싱 단언.

**CLI (신규 파일, 모두 `cmd/media-info/`):**
- `options.go` — 옵션 파서(`Config`, `parseArgs`, usage 텍스트). 순수 로직.
- `options_test.go` — 옵션 파서 단위 테스트.
- `report.go` — 공통 봉투 구조체(`Report`, `FileRef`) + `marshal()`.
- `report_test.go` — 직렬화(섹션 생략/ null 방출) 테스트.
- `collect_gmc.go` — `collectGMC()` + gmc 전용 섹션 구조체.
- `collect_gmc_test.go` — gmc 수집기 테스트.
- `collect_mkv.go` — `collectMKV()` + mkv 전용 섹션 구조체.
- `collect_mkv_test.go` — mkv 수집기 테스트.
- `main.go` — `main`/`run`/`detectFormat` + 출력 처리.
- `main_test.go` — 포맷 감지 + end-to-end 실행 테스트.

각 파일은 하나의 책임만 가진다: 옵션 파싱, 봉투 정의, 포맷별 수집, 실행/디스패치.

---

## Task 1: gmc 읽기측 API 추가 (Version, Summaries, index count)

**Files:**
- Modify: `gmc/format.go` (상수 블록, `formatVersion` 인근)
- Modify: `gmc/index.go` (`fileIndex` 메서드)
- Modify: `gmc/reader.go` (`Reader` 구조체 필드, `loadFooter`, 새 타입/메서드)
- Test: `gmc/summary_test.go` (신규)

**Interfaces:**
- Produces:
  - `const gmc.Version = 1` — 이 패키지가 읽고 쓰는 파일 포맷 버전. `Open`은 다른 버전을 거부하므로 열린 파일은 항상 이 버전.
  - `type gmc.TrackSummary struct { Track TrackID; FirstPTS, LastPTS, Frames uint64 }`
  - `func (r *gmc.Reader) Summaries() ([]TrackSummary, int)` — footer 기반 per-track 요약(finalized일 때만 non-nil)과 총 sync-point 수. non-finalized(복구된) 파일은 요약 slice가 nil이고, 두 번째 반환값만 스캔으로 수집된 sync-point 수.

- [ ] **Step 1: 실패 테스트 작성** — `gmc/summary_test.go`

```go
package gmc

import (
	"path/filepath"
	"testing"
)

func TestReaderSummariesFinalized(t *testing.T) {
	if Version != 1 {
		t.Fatalf("Version = %d, want 1", Version)
	}

	path := filepath.Join(t.TempDir(), "sum.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "V_TEST", TimebaseNum: 1, TimebaseDen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// pts 0,10,20: two keyframes (0,20), one non-keyframe (10).
	for i, pts := range []uint64{0, 10, 20} {
		if err := w.WriteFrame(id, Frame{PTS: pts, Keyframe: i%2 == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	sums, sync := r.Summaries()
	if len(sums) != 1 {
		t.Fatalf("summaries = %d, want 1", len(sums))
	}
	s := sums[0]
	if s.Track != id || s.FirstPTS != 0 || s.LastPTS != 20 || s.Frames != 3 {
		t.Fatalf("summary = %+v", s)
	}
	if sync < 1 {
		t.Fatalf("syncPoints = %d, want >=1", sync)
	}
}

func TestReaderSummariesNonFinalized(t *testing.T) {
	path := filepath.Join(t.TempDir(), "open.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "A_TEST", TimebaseNum: 1, TimebaseDen: 48000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(id, Frame{PTS: 0, Keyframe: true, Data: []byte{0}}); err != nil {
		t.Fatal(err)
	}
	// Close without Finalize: no footer, recovered by scan.
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Finalized() {
		t.Fatal("expected non-finalized")
	}
	sums, _ := r.Summaries()
	if sums != nil {
		t.Fatalf("summaries = %+v, want nil for non-finalized", sums)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./gmc/ -run TestReaderSummaries -v`
Expected: 컴파일 실패 — `undefined: Version`, `r.Summaries undefined`.

- [ ] **Step 3: `gmc/format.go`에 Version 상수 추가**

`gmc/format.go`의 상수 블록에서 `formatVersion = 1` 아래에 exported 별칭 추가:

```go
	formatVersion = 1

	// Version is the file format version this package reads and writes. Open
	// rejects files carrying any other version, so any opened file is Version.
	Version = formatVersion
```

- [ ] **Step 4: `gmc/index.go`에 count() 추가**

`fileIndex`의 `dump()` 메서드 아래(또는 인근)에 추가:

```go
// count returns the total number of index entries across all tracks.
func (ix *fileIndex) count() int {
	ix.mu.RLock()
	defer ix.mu.RUnlock()
	n := 0
	for _, es := range ix.tracks {
		n += len(es)
	}
	return n
}
```

- [ ] **Step 5: `gmc/reader.go` — summaries 필드 + loadFooter 보존 + Summaries 메서드**

(5a) `Reader` 구조체(`finalized bool` 인근)에 필드 추가:

```go
	finalized bool

	summaries []trackSummary // footer per-track summary; nil for recovered files
```

(5b) `loadFooter`에서 sums를 버리지 않고 보존. 기존:

```go
	for _, s := range sums {
		if s.frames > 0 {
			r.maxPTS[s.track] = s.lastPTS // stores maxPTS since task 2
		}
	}
```

바로 위(또는 아래)에 한 줄 추가하여 보존:

```go
	r.summaries = sums
	for _, s := range sums {
		if s.frames > 0 {
			r.maxPTS[s.track] = s.lastPTS // stores maxPTS since task 2
		}
	}
```

(5c) 파일 하단(공개 메서드들 사이, 예: `Finalized()` 인근)에 타입과 메서드 추가:

```go
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
```

- [ ] **Step 6: 테스트 통과 확인**

Run: `go test ./gmc/ -run TestReaderSummaries -v`
Expected: PASS (두 테스트 모두).

- [ ] **Step 7: 전체 gmc 회귀 확인**

Run: `go test ./gmc/`
Expected: ok (기존 테스트 무회귀).

- [ ] **Step 8: 커밋**

```bash
git add gmc/format.go gmc/index.go gmc/reader.go gmc/summary_test.go
git commit -m "기능: gmc 읽기측 요약 API 추가 (Version 상수, Reader.Summaries, 인덱스 카운트)"
```

---

## Task 2: mkv Title 파싱

**Files:**
- Modify: `mkv/ids.go` (Info 하위 요소 ID 인근)
- Modify: `mkv/demux.go` (`FileInfo` 구조체, `parseInfo`)
- Test: `mkv/demux_test.go` (기존 `buildTestMKV`/`TestDemuxerHeaderInfoTracks`)

**Interfaces:**
- Produces: `mkv.FileInfo.Title string` — Segment Info의 Title 요소(없으면 "").

- [ ] **Step 1: 실패 테스트 작성** — `mkv/demux_test.go` 수정

(1a) `buildTestMKV`의 info 블록(`appendUintElement(info, idDateUTC, 0)` 다음 줄)에 Title 추가:

```go
	info = appendUintElement(info, idDateUTC, 0)      // placeholder; parsed as signed
	info = appendStringElement(info, idTitle, "demo-title")
```

(1b) `TestDemuxerHeaderInfoTracks`의 info 단언 직후에 Title 단언 추가:

```go
	if d.Info().Title != "demo-title" {
		t.Fatalf("title = %q", d.Info().Title)
	}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./mkv/ -run TestDemuxerHeaderInfoTracks -v`
Expected: 컴파일 실패 — `undefined: idTitle`, `d.Info().Title undefined`.

- [ ] **Step 3: `mkv/ids.go`에 idTitle 추가**

Info 하위 요소 ID들과 함께(`idTimestampScale`/`idDuration`/`idDateUTC` 인근) 추가:

```go
	idTitle uint32 = 0x7BA9
```

- [ ] **Step 4: `mkv/demux.go` — FileInfo.Title 필드 + parseInfo 케이스**

(4a) `FileInfo` 구조체에 필드 추가:

```go
type FileInfo struct {
	TimestampScale uint64  // ns per timestamp unit (default 1e6)
	Duration       float64 // in TimestampScale units, 0 unknown
	DateUTC        int64   // ns since 2001-01-01T00:00:00 UTC
	HasDate        bool
	Title          string  // Segment Info Title, "" if absent
}
```

(4b) `parseInfo`의 switch에 케이스 추가(`case idDateUTC:` 블록 다음):

```go
		case idDateUTC:
			d.info.DateUTC = int64(parseUint(b))
			d.info.HasDate = true
		case idTitle:
			d.info.Title = string(b)
```

- [ ] **Step 5: 테스트 통과 확인**

Run: `go test ./mkv/ -run TestDemuxerHeaderInfoTracks -v`
Expected: PASS.

- [ ] **Step 6: 전체 mkv 회귀 확인**

Run: `go test ./mkv/`
Expected: ok.

- [ ] **Step 7: 커밋**

```bash
git add mkv/ids.go mkv/demux.go mkv/demux_test.go
git commit -m "기능: mkv Segment Info Title 파싱 (FileInfo.Title)"
```

---

## Task 3: media-info 옵션 파서

**Files:**
- Create: `cmd/media-info/options.go`
- Test: `cmd/media-info/options_test.go`

**Interfaces:**
- Produces:
  - `type Config struct { Header, Media, Tag, Index bool; Output, File string }`
  - `var errHelp = errors.New("help requested")`
  - `func parseArgs(args []string) (Config, error)` — `--help` 시 `errHelp` 반환. 검증 실패 시 일반 error. 성공 시 Config.
  - `const usageText string` — `--help`/오류 시 출력할 한국어 설명.

- [ ] **Step 1: 실패 테스트 작성** — `cmd/media-info/options_test.go`

```go
package main

import (
	"errors"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"a.gmc"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Header || !cfg.Media || !cfg.Tag {
		t.Fatalf("default sections off: %+v", cfg)
	}
	if cfg.Index {
		t.Fatal("index should default off")
	}
	if cfg.File != "a.gmc" || cfg.Output != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsInfoAll(t *testing.T) {
	cfg, err := parseArgs([]string{"--info-all", "--info-header", "no", "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Header || !cfg.Media || !cfg.Tag || !cfg.Index {
		t.Fatalf("info-all must force all on: %+v", cfg)
	}
}

func TestParseArgsExplicitNo(t *testing.T) {
	cfg, err := parseArgs([]string{"--info-media", "no", "--info-index", "yes", "--output", "o.json", "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Media || !cfg.Index || cfg.Output != "o.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsHelp(t *testing.T) {
	if _, err := parseArgs([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Fatalf("err = %v, want errHelp", err)
	}
}

func TestParseArgsBadValue(t *testing.T) {
	if _, err := parseArgs([]string{"--info-header", "maybe", "a.gmc"}); err == nil {
		t.Fatal("expected error for non yes/no value")
	}
}

func TestParseArgsArgCount(t *testing.T) {
	if _, err := parseArgs([]string{}); err == nil {
		t.Fatal("expected error for zero files")
	}
	if _, err := parseArgs([]string{"a.gmc", "b.mkv"}); err == nil {
		t.Fatal("expected error for two files")
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./cmd/media-info/ -run TestParseArgs -v`
Expected: 컴파일 실패 — `undefined: parseArgs`, `undefined: errHelp`.

- [ ] **Step 3: `options.go` 구현**

```go
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// Config is the resolved set of options for one media-info run.
type Config struct {
	Header bool
	Media  bool
	Tag    bool
	Index  bool
	Output string
	File   string
}

// errHelp signals that --help was requested; the caller prints usage and exits 0.
var errHelp = errors.New("help requested")

const usageText = `media-info — gmc/mkv 파일의 저장 상태를 JSON으로 요약합니다.

사용법:
  media-info [options ...] <filename.gmc | filename.mkv>

옵션:
  --info-all            header/media/tag/index 를 모두 포함 (다른 --info-* 보다 우선)
  --info-header yes|no  헤더 섹션 포함 여부 (기본 yes)
  --info-media  yes|no  미디어(트랙) 섹션 포함 여부 (기본 yes)
  --info-tag    yes|no  태그 섹션 포함 여부 (기본 yes)
  --info-index  yes|no  인덱스 요약 섹션 포함 여부 (기본 no)
  --output <file>       결과를 파일로 저장 (없으면 표준출력)
  --help                이 도움말을 표시

인자:
  정확히 하나의 .gmc 또는 .mkv 파일 경로.
`

func yesNo(name, v string) (bool, error) {
	switch v {
	case "yes":
		return true, nil
	case "no":
		return false, nil
	default:
		return false, fmt.Errorf("%s: value must be yes or no, got %q", name, v)
	}
}

// parseArgs resolves argv into a Config. It returns errHelp for --help.
func parseArgs(args []string) (Config, error) {
	fs := flag.NewFlagSet("media-info", flag.ContinueOnError)
	fs.SetOutput(io.Discard) // we render our own usage text
	fs.Usage = func() {}

	infoAll := fs.Bool("info-all", false, "")
	help := fs.Bool("help", false, "")
	header := fs.String("info-header", "yes", "")
	media := fs.String("info-media", "yes", "")
	tag := fs.String("info-tag", "yes", "")
	index := fs.String("info-index", "no", "")
	output := fs.String("output", "", "")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if *help {
		return Config{}, errHelp
	}

	cfg := Config{Output: *output}
	var err error
	if cfg.Header, err = yesNo("--info-header", *header); err != nil {
		return Config{}, err
	}
	if cfg.Media, err = yesNo("--info-media", *media); err != nil {
		return Config{}, err
	}
	if cfg.Tag, err = yesNo("--info-tag", *tag); err != nil {
		return Config{}, err
	}
	if cfg.Index, err = yesNo("--info-index", *index); err != nil {
		return Config{}, err
	}
	if *infoAll {
		cfg.Header, cfg.Media, cfg.Tag, cfg.Index = true, true, true, true
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return Config{}, fmt.Errorf("expected exactly one input file, got %d", len(rest))
	}
	cfg.File = rest[0]
	return cfg, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./cmd/media-info/ -run TestParseArgs -v`
Expected: PASS (모든 케이스).

- [ ] **Step 5: 커밋**

```bash
git add cmd/media-info/options.go cmd/media-info/options_test.go
git commit -m "기능: media-info 옵션 파서 (--info-all/--info-* yes|no, --output, --help)"
```

---

## Task 4: 공통 봉투 구조체와 직렬화

**Files:**
- Create: `cmd/media-info/report.go`
- Test: `cmd/media-info/report_test.go`

**Interfaces:**
- Produces:
  - `type FileRef struct { Path, Name string; Size int64 }` (json: path/name/size)
  - `type Report struct { File FileRef; Format string; Title *string; Header, Media, Tags, Index json.RawMessage }`
    - json 태그: `file`, `format`, `title`(항상 존재, null 가능), `header,omitempty`, `media,omitempty`, `tags,omitempty`, `index,omitempty`.
    - 섹션 필드는 `json.RawMessage`: nil이면 생략(omitempty), `RawMessage("null")`이면 명시적 null 방출, 그 외는 해당 JSON.
  - `func (r *Report) marshal() ([]byte, error)` — 2-space indent JSON.
  - `var jsonNull = json.RawMessage("null")` — 선택되었으나 값 없는 섹션(mkv index) 표시용.

- [ ] **Step 1: 실패 테스트 작성** — `cmd/media-info/report_test.go`

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestReportMarshalOmitsAndNulls(t *testing.T) {
	title := "Clip"
	r := &Report{
		File:   FileRef{Path: "a.mkv", Name: "a.mkv", Size: 42},
		Format: "mkv",
		Title:  &title,
		Header: json.RawMessage(`{"timestampScale":1000000}`),
		Index:  jsonNull, // selected but no value
		// Media and Tags left nil -> omitted
	}
	data, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid json: %s", data)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["media"]; ok {
		t.Fatal("media should be omitted")
	}
	if _, ok := m["tags"]; ok {
		t.Fatal("tags should be omitted")
	}
	if v, ok := m["index"]; !ok || string(v) != "null" {
		t.Fatalf("index = %s (ok=%v), want explicit null", v, ok)
	}
	if v, ok := m["title"]; !ok || string(v) != `"Clip"` {
		t.Fatalf("title = %s", v)
	}
}

func TestReportMarshalNullTitle(t *testing.T) {
	r := &Report{File: FileRef{Path: "a.gmc", Name: "a.gmc", Size: 1}, Format: "gmc"}
	data, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["title"]; !ok || string(v) != "null" {
		t.Fatalf("title = %s (ok=%v), want null", v, ok)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./cmd/media-info/ -run TestReport -v`
Expected: 컴파일 실패 — `undefined: Report`, `undefined: FileRef`, `undefined: jsonNull`.

- [ ] **Step 3: `report.go` 구현**

```go
package main

import "encoding/json"

// FileRef identifies the input file in the output envelope.
type FileRef struct {
	Path string `json:"path"`
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Report is the common JSON envelope. Section fields are pre-marshaled JSON:
// a nil RawMessage is omitted; jsonNull emits an explicit null (a section that
// was requested but has no value, e.g. mkv index).
type Report struct {
	File   FileRef         `json:"file"`
	Format string          `json:"format"`
	Title  *string         `json:"title"`
	Header json.RawMessage `json:"header,omitempty"`
	Media  json.RawMessage `json:"media,omitempty"`
	Tags   json.RawMessage `json:"tags,omitempty"`
	Index  json.RawMessage `json:"index,omitempty"`
}

// jsonNull marks a requested section that carries no value.
var jsonNull = json.RawMessage("null")

// marshal renders the report as 2-space indented JSON. MarshalIndent reflows
// the embedded RawMessage sections, so the whole document is consistently
// indented.
func (r *Report) marshal() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./cmd/media-info/ -run TestReport -v`
Expected: PASS.

- [ ] **Step 5: 커밋**

```bash
git add cmd/media-info/report.go cmd/media-info/report_test.go
git commit -m "기능: media-info 공통 봉투 구조체와 JSON 직렬화 (섹션 생략/null 방출)"
```

---

## Task 5: gmc 수집기

**Files:**
- Create: `cmd/media-info/collect_gmc.go`
- Test: `cmd/media-info/collect_gmc_test.go`

**Interfaces:**
- Consumes: `Config`, `Report`, `FileRef`, `jsonNull` (Task 3·4); `gmc.Open`, `gmc.Reader.{Tracks,Tags,StartTime,FilePrivate,Finalized,LastPTS,Summaries}`, `gmc.Version`, `gmc.TrackSummary`, `gmc.TrackInfo`, `gmc.KindVideo/KindAudio/KindData` (Task 1).
- Produces: `func collectGMC(path string, cfg Config) (*Report, error)`.

- [ ] **Step 1: 실패 테스트 작성** — `cmd/media-info/collect_gmc_test.go`

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Youngju-Heo/go-container/gmc"
)

func buildGMC(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.gmc")
	w, err := gmc.Create(path, gmc.CreateOptions{Private: []byte("mf")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(gmc.TrackInfo{Kind: gmc.KindVideo, Codec: "V_TEST", TimebaseNum: 1, TimebaseDen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetTag("title", []byte("My GMC")); err != nil {
		t.Fatal(err)
	}
	for i, pts := range []uint64{0, 10, 20} {
		if err := w.WriteFrame(id, gmc.Frame{PTS: pts, Keyframe: i%2 == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectGMCAllSections(t *testing.T) {
	path := buildGMC(t)
	cfg := Config{Header: true, Media: true, Tag: true, Index: true, File: path}
	rep, err := collectGMC(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != "gmc" {
		t.Fatalf("format = %s", rep.Format)
	}
	if rep.Title == nil || *rep.Title != "My GMC" {
		t.Fatalf("title = %v", rep.Title)
	}
	fi, _ := os.Stat(path)
	if rep.File.Size != fi.Size() || rep.File.Name != filepath.Base(path) {
		t.Fatalf("file = %+v", rep.File)
	}
	// index frames should be present and equal to 3 for the single track.
	var idx struct {
		SyncPoints int `json:"syncPoints"`
		Tracks     []struct {
			Frames *uint64 `json:"frames"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rep.Index, &idx); err != nil {
		t.Fatalf("index unmarshal: %v (%s)", err, rep.Index)
	}
	if len(idx.Tracks) != 1 || idx.Tracks[0].Frames == nil || *idx.Tracks[0].Frames != 3 {
		t.Fatalf("index = %+v", idx)
	}
	// header must carry version and trackCount.
	var hdr struct {
		Version    int  `json:"version"`
		Finalized  bool `json:"finalized"`
		TrackCount int  `json:"trackCount"`
	}
	if err := json.Unmarshal(rep.Header, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Version != 1 || !hdr.Finalized || hdr.TrackCount != 1 {
		t.Fatalf("header = %+v", hdr)
	}
}

func TestCollectGMCOmitsUnselected(t *testing.T) {
	path := buildGMC(t)
	cfg := Config{Header: true, File: path} // media/tag/index off
	rep, err := collectGMC(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Media != nil || rep.Tags != nil || rep.Index != nil {
		t.Fatalf("unselected sections not nil: media=%s tags=%s index=%s", rep.Media, rep.Tags, rep.Index)
	}
	if rep.Header == nil {
		t.Fatal("header should be present")
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./cmd/media-info/ -run TestCollectGMC -v`
Expected: 컴파일 실패 — `undefined: collectGMC`.

- [ ] **Step 3: `collect_gmc.go` 구현**

```go
package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/Youngju-Heo/go-container/gmc"
)

type gmcHeaderJSON struct {
	Version    int     `json:"version"`
	Finalized  bool    `json:"finalized"`
	StartTime  *string `json:"startTime"`
	PrivateLen int     `json:"privateLen"`
	TrackCount int     `json:"trackCount"`
}

type gmcTimebase struct {
	Num uint32 `json:"num"`
	Den uint32 `json:"den"`
}

type gmcTrackJSON struct {
	ID         int         `json:"id"`
	Kind       string      `json:"kind"`
	Codec      string      `json:"codec"`
	Timebase   gmcTimebase `json:"timebase"`
	Reordered  bool        `json:"reordered"`
	PrivateLen int         `json:"privateLen"`
	LastPTS    *uint64     `json:"lastPTS"`
}

type gmcMediaJSON struct {
	Tracks []gmcTrackJSON `json:"tracks"`
}

type gmcIndexTrackJSON struct {
	ID       int     `json:"id"`
	FirstPTS *uint64 `json:"firstPTS"`
	LastPTS  *uint64 `json:"lastPTS"`
	Frames   *uint64 `json:"frames"`
}

type gmcIndexJSON struct {
	SyncPoints int                 `json:"syncPoints"`
	Tracks     []gmcIndexTrackJSON `json:"tracks"`
}

func gmcKindName(k gmc.TrackKind) string {
	switch k {
	case gmc.KindVideo:
		return "video"
	case gmc.KindAudio:
		return "audio"
	case gmc.KindData:
		return "data"
	default:
		return "unknown"
	}
}

// tagValueString renders a raw tag value: UTF-8 text as-is, otherwise a
// hex-encoded string with a "hex:" marker.
func tagValueString(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return "hex:" + hex.EncodeToString(b)
}

func collectGMC(path string, cfg Config) (*Report, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	r, err := gmc.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	rep := &Report{
		File:   FileRef{Path: path, Name: filepath.Base(path), Size: fi.Size()},
		Format: "gmc",
	}

	tags := r.Tags()
	if v, ok := tags["title"]; ok {
		s := tagValueString(v)
		rep.Title = &s
	}

	if cfg.Header {
		hdr := gmcHeaderJSON{
			Version:    gmc.Version,
			Finalized:  r.Finalized(),
			PrivateLen: len(r.FilePrivate()),
			TrackCount: len(r.Tracks()),
		}
		if t, ok := r.StartTime(); ok {
			s := t.UTC().Format(time.RFC3339)
			hdr.StartTime = &s
		}
		b, err := json.Marshal(hdr)
		if err != nil {
			return nil, err
		}
		rep.Header = b
	}

	if cfg.Media {
		var m gmcMediaJSON
		for _, tr := range r.Tracks() {
			jt := gmcTrackJSON{
				ID:         int(tr.ID),
				Kind:       gmcKindName(tr.Kind),
				Codec:      tr.Codec,
				Timebase:   gmcTimebase{Num: tr.TimebaseNum, Den: tr.TimebaseDen},
				Reordered:  tr.Reordered,
				PrivateLen: len(tr.Private),
			}
			if v, ok := r.LastPTS(tr.ID); ok {
				jt.LastPTS = &v
			}
			m.Tracks = append(m.Tracks, jt)
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		rep.Media = b
	}

	if cfg.Tag {
		tm := make(map[string]string, len(tags))
		for k, v := range tags {
			tm[k] = tagValueString(v)
		}
		b, err := json.Marshal(tm)
		if err != nil {
			return nil, err
		}
		rep.Tags = b
	}

	if cfg.Index {
		sums, sync := r.Summaries()
		idx := gmcIndexJSON{SyncPoints: sync}
		if sums != nil {
			for _, s := range sums {
				fp, lp, fr := s.FirstPTS, s.LastPTS, s.Frames
				idx.Tracks = append(idx.Tracks, gmcIndexTrackJSON{
					ID: int(s.Track), FirstPTS: &fp, LastPTS: &lp, Frames: &fr,
				})
			}
		} else {
			// Recovered (non-finalized): frames/firstPTS unknown, lastPTS best-effort.
			for _, tr := range r.Tracks() {
				it := gmcIndexTrackJSON{ID: int(tr.ID)}
				if v, ok := r.LastPTS(tr.ID); ok {
					it.LastPTS = &v
				}
				idx.Tracks = append(idx.Tracks, it)
			}
		}
		b, err := json.Marshal(idx)
		if err != nil {
			return nil, err
		}
		rep.Index = b
	}

	return rep, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./cmd/media-info/ -run TestCollectGMC -v`
Expected: PASS.

- [ ] **Step 5: 커밋**

```bash
git add cmd/media-info/collect_gmc.go cmd/media-info/collect_gmc_test.go
git commit -m "기능: media-info gmc 수집기 (헤더/미디어/태그/인덱스 요약 → 공통 봉투)"
```

---

## Task 6: mkv 수집기

**Files:**
- Create: `cmd/media-info/collect_mkv.go`
- Test: `cmd/media-info/collect_mkv_test.go`

**Interfaces:**
- Consumes: `Config`, `Report`, `FileRef`, `jsonNull` (Task 3·4); `mkv.NewDemuxer`, `mkv.Demuxer.{Info,Tracks,Tags}`, `mkv.FileInfo`(with Title, Task 2), `mkv.TrackEntry`.
- Produces: `func collectMKV(path string, cfg Config) (*Report, error)`.
- 참고: `mkv.NewDemuxer(io.ReaderAt, int64)` 형태이므로 `os.Open` + `Stat().Size()`로 연다(예제 `example/mkv-info` 패턴과 동일).

- [ ] **Step 1: 실패 테스트 작성** — `cmd/media-info/collect_mkv_test.go`

```go
package main

import (
	"encoding/json"
	"testing"
)

func TestCollectMKVSample(t *testing.T) {
	const path = "../../sample/test-clip.mkv"
	cfg := Config{Header: true, Media: true, Tag: true, Index: true, File: path}
	rep, err := collectMKV(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != "mkv" {
		t.Fatalf("format = %s", rep.Format)
	}
	if rep.File.Name != "test-clip.mkv" || rep.File.Size <= 0 {
		t.Fatalf("file = %+v", rep.File)
	}
	// index is metadata-only for mkv: explicit null when selected.
	if string(rep.Index) != "null" {
		t.Fatalf("index = %s, want null", rep.Index)
	}
	// media must contain at least one track with a codecID.
	var media struct {
		Tracks []struct {
			Number  int    `json:"number"`
			Type    string `json:"type"`
			CodecID string `json:"codecID"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rep.Media, &media); err != nil {
		t.Fatal(err)
	}
	if len(media.Tracks) == 0 || media.Tracks[0].CodecID == "" {
		t.Fatalf("media = %+v", media)
	}
	// header must carry timestampScale.
	var hdr struct {
		TimestampScale uint64 `json:"timestampScale"`
	}
	if err := json.Unmarshal(rep.Header, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.TimestampScale == 0 {
		t.Fatalf("header = %+v", hdr)
	}
}

func TestCollectMKVOmitsUnselected(t *testing.T) {
	const path = "../../sample/test-clip.mkv"
	cfg := Config{Media: true, File: path} // header/tag/index off
	rep, err := collectMKV(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Header != nil || rep.Tags != nil || rep.Index != nil {
		t.Fatalf("unselected not nil: header=%s tags=%s index=%s", rep.Header, rep.Tags, rep.Index)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./cmd/media-info/ -run TestCollectMKV -v`
Expected: 컴파일 실패 — `undefined: collectMKV`.

- [ ] **Step 3: `collect_mkv.go` 구현**

```go
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Youngju-Heo/go-container/mkv"
)

// matroskaEpoch is the Matroska DateUTC reference: 2001-01-01T00:00:00 UTC.
var matroskaEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

type mkvHeaderJSON struct {
	TimestampScale uint64  `json:"timestampScale"`
	Duration       float64 `json:"duration"`
	DateUTC        *string `json:"dateUTC"`
}

type mkvTrackJSON struct {
	Number          int     `json:"number"`
	Type            string  `json:"type"`
	CodecID         string  `json:"codecID"`
	PixelWidth      uint64  `json:"pixelWidth,omitempty"`
	PixelHeight     uint64  `json:"pixelHeight,omitempty"`
	SamplingFreq    float64 `json:"samplingFrequency,omitempty"`
	Channels        uint64  `json:"channels,omitempty"`
	BitDepth        uint64  `json:"bitDepth,omitempty"`
	DefaultDuration uint64  `json:"defaultDuration,omitempty"`
	CodecPrivateLen int     `json:"codecPrivateLen"`
}

type mkvMediaJSON struct {
	Tracks []mkvTrackJSON `json:"tracks"`
}

func mkvTypeName(t uint8) string {
	switch t {
	case 1:
		return "video"
	case 2:
		return "audio"
	case 17:
		return "subtitle"
	default:
		return "unknown"
	}
}

func collectMKV(path string, cfg Config) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	d, err := mkv.NewDemuxer(f, fi.Size())
	if err != nil {
		return nil, err
	}
	info := d.Info()

	rep := &Report{
		File:   FileRef{Path: path, Name: filepath.Base(path), Size: fi.Size()},
		Format: "mkv",
	}
	if info.Title != "" {
		s := info.Title
		rep.Title = &s
	}

	if cfg.Header {
		hdr := mkvHeaderJSON{TimestampScale: info.TimestampScale, Duration: info.Duration}
		if info.HasDate {
			s := matroskaEpoch.Add(time.Duration(info.DateUTC)).UTC().Format(time.RFC3339)
			hdr.DateUTC = &s
		}
		b, err := json.Marshal(hdr)
		if err != nil {
			return nil, err
		}
		rep.Header = b
	}

	if cfg.Media {
		var m mkvMediaJSON
		for _, te := range d.Tracks() {
			m.Tracks = append(m.Tracks, mkvTrackJSON{
				Number:          int(te.Number),
				Type:            mkvTypeName(te.Type),
				CodecID:         te.CodecID,
				PixelWidth:      te.PixelWidth,
				PixelHeight:     te.PixelHeight,
				SamplingFreq:    te.SamplingFrequency,
				Channels:        te.Channels,
				BitDepth:        te.BitDepth,
				DefaultDuration: te.DefaultDuration,
				CodecPrivateLen: len(te.CodecPrivate),
			})
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		rep.Media = b
	}

	if cfg.Tag {
		b, err := json.Marshal(d.Tags())
		if err != nil {
			return nil, err
		}
		rep.Tags = b
	}

	if cfg.Index {
		// mkv index summary requires a full packet scan, which the
		// metadata-only policy forbids; report explicit null.
		rep.Index = jsonNull
	}

	return rep, nil
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./cmd/media-info/ -run TestCollectMKV -v`
Expected: PASS.

- [ ] **Step 5: 커밋**

```bash
git add cmd/media-info/collect_mkv.go cmd/media-info/collect_mkv_test.go
git commit -m "기능: media-info mkv 수집기 (헤더/미디어/태그, 인덱스는 메타데이터-only로 null)"
```

---

## Task 7: main — 포맷 감지, 디스패치, 출력

**Files:**
- Create: `cmd/media-info/main.go`
- Test: `cmd/media-info/main_test.go`

**Interfaces:**
- Consumes: `parseArgs`, `errHelp`, `usageText`, `collectGMC`, `collectMKV`, `Report.marshal` (Task 3~6).
- Produces:
  - `func detectFormat(path string) (string, error)` — 확장자(.gmc/.mkv, 대소문자 무시)로 판별 후 매직바이트 검증. 반환 "gmc"|"mkv".
  - `func run(args []string, stdout, stderr io.Writer) int` — 종료코드 반환(0 성공, 2 사용법/인자 오류, 1 실행 오류).
  - `func main()` — `os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))`.

- [ ] **Step 1: 실패 테스트 작성** — `cmd/media-info/main_test.go`

```go
package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFormatByMagic(t *testing.T) {
	if f, err := detectFormat("../../sample/test-clip.mkv"); err != nil || f != "mkv" {
		t.Fatalf("mkv detect = %q, %v", f, err)
	}
	if f, err := detectFormat("../../sample/test-clip-000.gmc"); err != nil || f != "gmc" {
		t.Fatalf("gmc detect = %q, %v", f, err)
	}
}

func TestDetectFormatMismatch(t *testing.T) {
	// .gmc extension but MKV content -> error.
	bad := filepath.Join(t.TempDir(), "fake.gmc")
	src, err := os.ReadFile("../../sample/test-clip.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := detectFormat(bad); err == nil {
		t.Fatal("expected magic mismatch error")
	}
}

func TestDetectFormatUnknownExt(t *testing.T) {
	if _, err := detectFormat("movie.avi"); err == nil {
		t.Fatal("expected unknown extension error")
	}
}

func TestRunMKVToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"../../sample/test-clip.mkv"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid json: %s", out.String())
	}
}

func TestRunOutputFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "o.json")
	var out, errb bytes.Buffer
	code := run([]string{"--output", dst, "../../sample/test-clip-000.gmc"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout should be empty when --output given: %s", out.String())
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid json file: %s", data)
	}
}

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("help code = %d", code)
	}
	if !strings.Contains(out.String()+errb.String(), "media-info") {
		t.Fatal("usage text missing")
	}
}

func TestRunBadArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{}, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
```

- [ ] **Step 2: 테스트 실패 확인**

Run: `go test ./cmd/media-info/ -run 'TestDetectFormat|TestRun' -v`
Expected: 컴파일 실패 — `undefined: detectFormat`, `undefined: run`.

- [ ] **Step 3: `main.go` 구현**

```go
// Command media-info summarizes the stored state of a gmc or mkv file as JSON.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Format magic signatures (format contracts, stable identifiers).
var (
	gmcMagic = []byte("GMC1")                 // gmc file header magic
	mkvMagic = []byte{0x1A, 0x45, 0xDF, 0xA3} // EBML header id
)

// detectFormat resolves the container format from the extension and verifies
// it against the leading magic bytes.
func detectFormat(path string) (string, error) {
	var want []byte
	var format string
	switch strings.ToLower(filepath.Ext(path)) {
	case ".gmc":
		want, format = gmcMagic, "gmc"
	case ".mkv":
		want, format = mkvMagic, "mkv"
	default:
		return "", fmt.Errorf("unsupported extension %q (expected .gmc or .mkv)", filepath.Ext(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	head := make([]byte, len(want))
	if _, err := io.ReadFull(f, head); err != nil {
		return "", fmt.Errorf("read magic: %w", err)
	}
	if !bytes.Equal(head, want) {
		return "", fmt.Errorf("magic mismatch: %s content does not match extension", format)
	}
	return format, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	cfg, err := parseArgs(args)
	if errors.Is(err, errHelp) {
		fmt.Fprint(stdout, usageText)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "media-info:", err)
		fmt.Fprint(stderr, usageText)
		return 2
	}

	format, err := detectFormat(cfg.File)
	if err != nil {
		fmt.Fprintln(stderr, "media-info:", err)
		return 1
	}

	var rep *Report
	switch format {
	case "gmc":
		rep, err = collectGMC(cfg.File, cfg)
	case "mkv":
		rep, err = collectMKV(cfg.File, cfg)
	}
	if err != nil {
		fmt.Fprintln(stderr, "media-info:", err)
		return 1
	}

	data, err := rep.marshal()
	if err != nil {
		fmt.Fprintln(stderr, "media-info:", err)
		return 1
	}

	if cfg.Output != "" {
		if err := os.WriteFile(cfg.Output, data, 0o644); err != nil {
			fmt.Fprintln(stderr, "media-info:", err)
			return 1
		}
		return 0
	}
	stdout.Write(data)
	fmt.Fprintln(stdout)
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}
```

- [ ] **Step 4: 테스트 통과 확인**

Run: `go test ./cmd/media-info/ -run 'TestDetectFormat|TestRun' -v`
Expected: PASS (모든 케이스).

- [ ] **Step 5: 패키지 전체 테스트 + 빌드**

Run: `go test ./cmd/media-info/ && go build -o /dev/null ./cmd/media-info/`
Expected: ok, 빌드 성공. (Windows에서는 `go build -o nul ./cmd/media-info/`)

- [ ] **Step 6: 커밋**

```bash
git add cmd/media-info/main.go cmd/media-info/main_test.go
git commit -m "기능: media-info 진입점 (포맷 감지·디스패치·출력) 및 end-to-end 테스트"
```

---

## Task 8: 실제 실행 검증 및 문서 갱신

**Files:**
- Modify: `README.md` (도구 목록/예제에 media-info 항목이 있으면 추가; 없으면 생략 가능)
- 검증 전용(코드 변경 없음 가능)

- [ ] **Step 1: 실제 샘플에 대해 실행 확인**

Run:
```bash
go run ./cmd/media-info --info-all sample/test-clip.mkv
go run ./cmd/media-info sample/test-clip-000.gmc
go run ./cmd/media-info --info-index yes sample/test-clip-000.gmc
```
Expected: 각각 well-formed JSON. mkv는 `"index": null`, gmc는 `index.tracks[].frames` 값 존재. 선택되지 않은 섹션은 출력에 없음.

- [ ] **Step 2: 전체 테스트 스위트**

Run: `go test ./...`
Expected: 모든 패키지 ok.

- [ ] **Step 3: (선택) README에 media-info 사용법 추가**

`README.md`에 CLI/예제 목록이 있으면 media-info 항목과 옵션 요약을 한국어로 추가한다. 목록이 없으면 이 단계는 생략.

- [ ] **Step 4: 커밋**

```bash
git add -A
git commit -m "문서: media-info 사용법 반영 및 실행 검증"
```

---

## Self-Review (계획 대비 스펙 점검)

- **스펙 커버리지:**
  - 인자 gmc/mkv 입력 → Task 7 detectFormat + 디스패치. ✓
  - 타이틀(mkv=Title / gmc=title 태그) → Task 2 + Task 5·6. ✓
  - 헤더/미디어/태그/인덱스 섹션 → Task 5·6. ✓
  - `--info-all` → Task 3. ✓
  - `--info-header|media|tag|index yes|no` + 기본값 → Task 3. ✓
  - `--output` → Task 7. ✓
  - `--help` 전체 설명 → Task 3 usageText + Task 7. ✓
  - well-formed JSON → Task 4 marshal + Task 7 json.Valid 테스트. ✓
  - 인덱스 요약 통계, mkv=null, 스캔 없음 → Task 5·6. ✓
  - 라이브러리 최소 변경(mkv Title, gmc 요약) → Task 1·2. ✓
- **플레이스홀더:** 없음. 모든 코드 블록은 실제 구현 포함.
- **타입 일관성:** `Config`/`Report`/`FileRef`/`jsonNull`/`collectGMC`/`collectMKV`/`run`/`detectFormat`/`gmc.Summaries`/`gmc.TrackSummary`/`gmc.Version`/`mkv.FileInfo.Title`가 정의 태스크와 소비 태스크에서 동일 시그니처. ✓
- **범위:** 단일 구현 계획으로 적합(하나의 CLI + 두 개의 작은 라이브러리 확장).
- **모호성:** Task 7의 `filepathExt`는 표준 `filepath.Ext`로 대체하도록 명시(중복 제거).
