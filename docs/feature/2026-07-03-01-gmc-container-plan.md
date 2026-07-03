# GMC 컨테이너 구현 계획

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**목표:** 설계 문서(`docs/feature/2026-07-03-01-gmc-container-design.md`)의 GMC 멀티미디어 컨테이너를 Go 패키지 `gmc`로 구현한다 — 쓰기 도중 읽기(라이브 테일 + 과거 시크), 크래시 복구, Tags 영역, Footer 빠른 오픈 포함.

**아키텍처:** append-only 청크 스트림 + 파일 앞쪽 Tags 고정 영역(더블 슬롯). 단일 Writer가 mutex로 직렬화하며 `committedSize`(atomic)를 전진시키고, 같은 프로세스 Reader들은 in-memory 인덱스(RWMutex)와 committedSize를 공유해 `ReadAt`으로 읽는다. 별도 오픈은 트레일러→Footer 경로 또는 CRC 전수 검증 스캔 복구.

**기술 스택:** Go 1.22+, 표준 라이브러리만 (외부 의존성 금지).

## 전역 제약

- 모듈 경로: `github.com/Youngju-Heo/go-container`, 패키지 디렉터리: `gmc/` (임포트 경로 `github.com/Youngju-Heo/go-container/gmc`, 모든 소스·테스트가 `package gmc` — white-box 테스트 허용)
- 바이트 오더 리틀 엔디언, CRC는 CRC-32C(`crc32.MakeTable(crc32.Castagnoli)`) — 설계 문서 §3
- payloadLen 상한 256 MiB, 트랙 내 pts 단조 비감소 강제, 미지 청크 타입 skip — 설계 문서 §3.4·§3.5
- 코드 내 로그·에러 메시지·주석은 영어, 커밋 메시지는 한국어
- 모든 태스크는 TDD(실패 테스트 → 구현 → 통과 → 커밋). 테스트 실행은 리포 루트에서 `go test ./gmc/ ...`
- 태스크 16의 동시성 테스트는 반드시 `-race`로 실행
- 각 태스크 완료 시 git 커밋 (메시지는 각 태스크의 커밋 스텝 참조)

## 파일 구조

```
go.mod                  — 모듈 정의 (태스크 1)
gmc/
  format.go             — 상수·에러·CRC 테이블 (태스크 1)
  chunk.go              — 청크 프레이밍 인코딩/디코딩 (태스크 1)
  header.go             — 파일 헤더 (태스크 2)
  cursor.go             — 바이너리 파싱 커서 헬퍼 (태스크 3)
  tags.go               — Tags 슬롯 코덱 + 더블 슬롯 선택 (태스크 3)
  track.go              — 공개 타입(TrackID/TrackKind/TrackInfo) + TrackInfo 코덱 (태스크 4)
  data.go               — Data 페이로드 코덱 (태스크 4)
  checkpoint.go         — IndexCheckpoint 코덱 (태스크 5)
  footer.go             — Footer·트레일러 코덱 (태스크 5)
  index.go              — in-memory 인덱스 (태스크 6)
  writer.go             — Writer: Create/AddTrack/WriteFrame/SetTag/체크포인트/Finalize (태스크 7~10)
  reader.go             — Reader: Open/스캔 복구/접근자 (태스크 11~12)
  iterator.go           — Iterator + SeekPTS/ReadInterleaved/Follow (태스크 13~15)
  *_test.go             — 각 태스크의 테스트
```

---

### 태스크 1: 모듈 스캐폴딩 + 청크 프레이밍

**Files:**
- Create: `go.mod`, `gmc/format.go`, `gmc/chunk.go`
- Test: `gmc/chunk_test.go`

**Interfaces:**
- Produces: 상수 `fileMagic="GMC1"`, `endMagic="GMCE"`, `formatVersion=1`, `headerFixedSize=20`, `trailerSize=16`, `chunkHeaderSize=5`, `chunkFramingSize=9`, `maxPayloadLen=256<<20`, `defaultTagsAreaSize=8<<10`, 청크 타입 `chunkTrackInfo=0x01`/`chunkData=0x02`/`chunkCheckpoint=0x03`/`chunkFooter=0x04`, `castagnoli` CRC 테이블, 에러 `ErrCorrupt`/`ErrNonMonotonicPTS`/`ErrTagsTooLarge`/`ErrUnknownTrack`/`ErrClosed`
- Produces: `appendChunk(dst []byte, typ byte, payload []byte) []byte`, `chunkCRC(typ byte, payload []byte) uint32`, `readChunkAt(r io.ReaderAt, off, limit int64) (typ byte, payload []byte, next int64, err error)` — `off==limit`이면 `io.EOF`, 길이 이상/CRC 불일치면 `ErrCorrupt`

- [ ] **Step 1: 모듈 초기화**

```powershell
go mod init github.com/Youngju-Heo/go-container
New-Item -ItemType Directory -Force gmc
```

- [ ] **Step 2: 실패하는 테스트 작성** — `gmc/chunk_test.go`

```go
package gmc

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"
)

func TestChunkRoundtrip(t *testing.T) {
	payload := []byte("hello payload")
	buf := appendChunk(nil, chunkData, payload)
	if len(buf) != chunkFramingSize+len(payload) {
		t.Fatalf("frame size = %d, want %d", len(buf), chunkFramingSize+len(payload))
	}
	typ, got, next, err := readChunkAt(bytes.NewReader(buf), 0, int64(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	if typ != chunkData || !bytes.Equal(got, payload) {
		t.Fatalf("typ=%d payload=%q", typ, got)
	}
	if next != int64(len(buf)) {
		t.Fatalf("next = %d, want %d", next, len(buf))
	}
}

func TestChunkCleanEOF(t *testing.T) {
	buf := appendChunk(nil, chunkData, []byte("x"))
	if _, _, _, err := readChunkAt(bytes.NewReader(buf), int64(len(buf)), int64(len(buf))); err != io.EOF {
		t.Fatalf("err = %v, want io.EOF", err)
	}
}

func TestChunkCorruption(t *testing.T) {
	payload := []byte("payload-bytes")
	base := appendChunk(nil, chunkData, payload)

	flip := append([]byte(nil), base...)
	flip[7] ^= 0xFF // corrupt payload byte -> CRC mismatch
	if _, _, _, err := readChunkAt(bytes.NewReader(flip), 0, int64(len(flip))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("crc mismatch: err = %v", err)
	}

	trunc := base[:len(base)-3] // torn tail
	if _, _, _, err := readChunkAt(bytes.NewReader(trunc), 0, int64(len(trunc))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("truncated: err = %v", err)
	}

	huge := append([]byte(nil), base...)
	binary.LittleEndian.PutUint32(huge[0:4], maxPayloadLen+1) // oversize length
	if _, _, _, err := readChunkAt(bytes.NewReader(huge), 0, int64(len(huge))); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("oversize: err = %v", err)
	}
}
```

- [ ] **Step 3: 실패 확인**

Run: `go test ./gmc/ -v`
Expected: `FAIL ... [build failed]` (undefined: appendChunk 등)

- [ ] **Step 4: 구현** — `gmc/format.go`

```go
// Package gmc implements the GMC media container format.
package gmc

import (
	"errors"
	"hash/crc32"
)

const (
	fileMagic = "GMC1"
	endMagic  = "GMCE"

	formatVersion = 1

	headerFixedSize = 20 // magic(4)+version(2)+flags(2)+tagsAreaLen(4)+privateLen(4)+crc(4)
	trailerSize     = 16 // footerOffset(8)+crc(4)+endMagic(4)

	chunkHeaderSize  = 5 // payloadLen(4)+type(1)
	chunkFramingSize = 9 // header(5)+crc(4)

	maxPayloadLen = 256 << 20 // 256 MiB

	defaultTagsAreaSize = 8 << 10
)

const (
	chunkTrackInfo  byte = 0x01
	chunkData       byte = 0x02
	chunkCheckpoint byte = 0x03
	chunkFooter     byte = 0x04
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var (
	ErrCorrupt         = errors.New("gmc: corrupt data")
	ErrNonMonotonicPTS = errors.New("gmc: non-monotonic pts within track")
	ErrTagsTooLarge    = errors.New("gmc: tags exceed slot capacity")
	ErrUnknownTrack    = errors.New("gmc: unknown track")
	ErrClosed          = errors.New("gmc: writer closed")
)
```

`gmc/chunk.go`:

```go
package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// chunkCRC computes CRC-32C over type byte followed by payload.
func chunkCRC(typ byte, payload []byte) uint32 {
	crc := crc32.Checksum([]byte{typ}, castagnoli)
	return crc32.Update(crc, castagnoli, payload)
}

// appendChunk serializes one chunk: [payloadLen u32][type u8][payload][crc u32].
func appendChunk(dst []byte, typ byte, payload []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, uint32(len(payload)))
	dst = append(dst, typ)
	dst = append(dst, payload...)
	return binary.LittleEndian.AppendUint32(dst, chunkCRC(typ, payload))
}

// readChunkAt reads and CRC-verifies the chunk at off. limit is the exclusive
// upper bound of readable bytes (committed size or logical EOF).
// Returns io.EOF when off == limit, ErrCorrupt on any framing violation.
func readChunkAt(r io.ReaderAt, off, limit int64) (typ byte, payload []byte, next int64, err error) {
	if off == limit {
		return 0, nil, 0, io.EOF
	}
	if off+chunkHeaderSize > limit {
		return 0, nil, 0, ErrCorrupt
	}
	var hdr [chunkHeaderSize]byte
	if _, err := r.ReadAt(hdr[:], off); err != nil {
		return 0, nil, 0, err
	}
	plen := int64(binary.LittleEndian.Uint32(hdr[0:4]))
	typ = hdr[4]
	if plen > maxPayloadLen || off+chunkFramingSize+plen > limit {
		return 0, nil, 0, ErrCorrupt
	}
	body := make([]byte, plen+4)
	if _, err := r.ReadAt(body, off+chunkHeaderSize); err != nil {
		return 0, nil, 0, err
	}
	payload = body[:plen]
	if chunkCRC(typ, payload) != binary.LittleEndian.Uint32(body[plen:]) {
		return 0, nil, 0, ErrCorrupt
	}
	return typ, payload, off + chunkFramingSize + plen, nil
}
```

- [ ] **Step 5: 통과 확인**

Run: `go test ./gmc/ -v`
Expected: `PASS` (TestChunkRoundtrip, TestChunkCleanEOF, TestChunkCorruption)

- [ ] **Step 6: 커밋**

```powershell
git add go.mod gmc/
git commit -m "구현: Go 모듈 초기화 및 청크 프레이밍 인코딩/디코딩 (CRC-32C 검증 포함)"
```

---

### 태스크 2: 파일 헤더

**Files:**
- Create: `gmc/header.go`
- Test: `gmc/header_test.go`

**Interfaces:**
- Consumes: `castagnoli`, `ErrCorrupt`, `headerFixedSize`, `fileMagic`, `formatVersion`, `maxPayloadLen` (태스크 1)
- Produces: `type fileHeader struct { tagsAreaLen uint32; private []byte }`, `encodeFileHeader(h fileHeader) []byte`, `decodeFileHeader(r io.ReaderAt) (fileHeader, int64, error)` — 두 번째 반환값은 헤더 전체 길이(= Tags 영역 시작 오프셋)

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/header_test.go`

```go
package gmc

import (
	"bytes"
	"errors"
	"testing"
)

func TestFileHeaderRoundtrip(t *testing.T) {
	h := fileHeader{tagsAreaLen: 8192, private: []byte("private-data")}
	buf := encodeFileHeader(h)
	got, hlen, err := decodeFileHeader(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if hlen != int64(len(buf)) {
		t.Fatalf("headerLen = %d, want %d", hlen, len(buf))
	}
	if got.tagsAreaLen != 8192 || !bytes.Equal(got.private, h.private) {
		t.Fatalf("got %+v", got)
	}
}

func TestFileHeaderEmptyPrivate(t *testing.T) {
	buf := encodeFileHeader(fileHeader{tagsAreaLen: 1024})
	got, hlen, err := decodeFileHeader(bytes.NewReader(buf))
	if err != nil {
		t.Fatal(err)
	}
	if hlen != headerFixedSize || len(got.private) != 0 {
		t.Fatalf("hlen=%d private=%q", hlen, got.private)
	}
}

func TestFileHeaderCorruption(t *testing.T) {
	buf := encodeFileHeader(fileHeader{tagsAreaLen: 1024, private: []byte("p")})

	bad := append([]byte(nil), buf...)
	bad[0] = 'X' // bad magic
	if _, _, err := decodeFileHeader(bytes.NewReader(bad)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("magic: err = %v", err)
	}

	bad = append([]byte(nil), buf...)
	bad[9] ^= 0xFF // corrupt tagsAreaLen -> CRC mismatch
	if _, _, err := decodeFileHeader(bytes.NewReader(bad)); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("crc: err = %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestFileHeader -v`
Expected: `FAIL ... [build failed]` (undefined: fileHeader)

- [ ] **Step 3: 구현** — `gmc/header.go`

```go
package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"io"
)

// fileHeader is the fixed file header at offset 0. Written once at Create,
// immutable afterwards. Layout (little-endian):
//
//	magic(4) version(2) flags(2) tagsAreaLen(4) privateLen(4) crc(4) private(n)
type fileHeader struct {
	tagsAreaLen uint32
	private     []byte
}

func encodeFileHeader(h fileHeader) []byte {
	buf := make([]byte, headerFixedSize+len(h.private))
	copy(buf[0:4], fileMagic)
	binary.LittleEndian.PutUint16(buf[4:6], formatVersion)
	// flags at [6:8] reserved as zero
	binary.LittleEndian.PutUint32(buf[8:12], h.tagsAreaLen)
	binary.LittleEndian.PutUint32(buf[12:16], uint32(len(h.private)))
	copy(buf[headerFixedSize:], h.private)
	crc := crc32.Checksum(buf[0:16], castagnoli)
	crc = crc32.Update(crc, castagnoli, h.private)
	binary.LittleEndian.PutUint32(buf[16:20], crc)
	return buf
}

// decodeFileHeader reads and validates the header. Returns the header and its
// total length, which is the offset where the tags area begins.
func decodeFileHeader(r io.ReaderAt) (fileHeader, int64, error) {
	var fixed [headerFixedSize]byte
	if _, err := r.ReadAt(fixed[:], 0); err != nil {
		return fileHeader{}, 0, err
	}
	if string(fixed[0:4]) != fileMagic {
		return fileHeader{}, 0, ErrCorrupt
	}
	if binary.LittleEndian.Uint16(fixed[4:6]) != formatVersion {
		return fileHeader{}, 0, ErrCorrupt
	}
	tagsLen := binary.LittleEndian.Uint32(fixed[8:12])
	privLen := binary.LittleEndian.Uint32(fixed[12:16])
	if tagsLen > maxPayloadLen || privLen > maxPayloadLen {
		return fileHeader{}, 0, ErrCorrupt
	}
	priv := make([]byte, privLen)
	if privLen > 0 {
		if _, err := r.ReadAt(priv, headerFixedSize); err != nil {
			return fileHeader{}, 0, err
		}
	}
	crc := crc32.Checksum(fixed[0:16], castagnoli)
	crc = crc32.Update(crc, castagnoli, priv)
	if crc != binary.LittleEndian.Uint32(fixed[16:20]) {
		return fileHeader{}, 0, ErrCorrupt
	}
	return fileHeader{tagsAreaLen: tagsLen, private: priv}, headerFixedSize + int64(privLen), nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestFileHeader -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/header.go gmc/header_test.go
git commit -m "구현: 파일 헤더 인코딩/디코딩 (magic·version·private data·CRC 검증)"
```

---

### 태스크 3: Tags 슬롯 코덱 + 더블 슬롯 선택

**Files:**
- Create: `gmc/cursor.go`, `gmc/tags.go`
- Test: `gmc/tags_test.go`

**Interfaces:**
- Consumes: `castagnoli`, `ErrCorrupt` (태스크 1)
- Produces: `type cursor struct{...}` — `u8()/u16()/u32()/u64()/bytes(n)/need(n)` 파싱 헬퍼 (`bad` 필드로 오류 전파)
- Produces: `TagStartTime = "gmc.start_time_unix_ns"`, `TagLocation = "gmc.location"` (공개 상수)
- Produces: `encodeTagsSlot(seq uint64, tags map[string][]byte) []byte` (키 정렬로 결정적), `decodeTagsSlot(b []byte) (seq uint64, tags map[string][]byte, ok bool)`, `pickTagsSlot(area []byte) (tags map[string][]byte, seq uint64, nextSlot int)` — 유효 슬롯 중 seq 큰 쪽 채택, nextSlot은 다음에 쓸 슬롯(0/1)

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/tags_test.go`

```go
package gmc

import (
	"bytes"
	"testing"
)

func sampleTags() map[string][]byte {
	return map[string][]byte{
		TagLocation: []byte("37.5665,126.9780"),
		"camera.id": []byte("cam-03"),
	}
}

func TestTagsSlotRoundtrip(t *testing.T) {
	buf := encodeTagsSlot(7, sampleTags())
	seq, tags, ok := decodeTagsSlot(buf)
	if !ok || seq != 7 {
		t.Fatalf("ok=%v seq=%d", ok, seq)
	}
	if !bytes.Equal(tags["camera.id"], []byte("cam-03")) || len(tags) != 2 {
		t.Fatalf("tags = %v", tags)
	}
	// deterministic encoding (sorted keys)
	if !bytes.Equal(buf, encodeTagsSlot(7, sampleTags())) {
		t.Fatal("encoding is not deterministic")
	}
}

func TestTagsSlotRejectsCorruptionAndZeroFill(t *testing.T) {
	buf := encodeTagsSlot(1, sampleTags())
	buf[9] ^= 0xFF
	if _, _, ok := decodeTagsSlot(buf); ok {
		t.Fatal("corrupt slot accepted")
	}
	if _, _, ok := decodeTagsSlot(make([]byte, 4096)); ok {
		t.Fatal("zero-filled slot accepted")
	}
}

func TestPickTagsSlot(t *testing.T) {
	const slot = 4096
	area := make([]byte, 2*slot)

	// both invalid (fresh file) -> no tags, write slot 0 next
	tags, seq, next := pickTagsSlot(area)
	if tags != nil || seq != 0 || next != 0 {
		t.Fatalf("fresh: tags=%v seq=%d next=%d", tags, seq, next)
	}

	// slot A valid seq=1 -> adopt A, write slot 1 next
	copy(area[0:], encodeTagsSlot(1, map[string][]byte{"k": []byte("v1")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 1 || next != 1 || !bytes.Equal(tags["k"], []byte("v1")) {
		t.Fatalf("A only: seq=%d next=%d tags=%v", seq, next, tags)
	}

	// slot B valid seq=2 -> adopt B, write slot 0 next
	copy(area[slot:], encodeTagsSlot(2, map[string][]byte{"k": []byte("v2")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 2 || next != 0 || !bytes.Equal(tags["k"], []byte("v2")) {
		t.Fatalf("B newer: seq=%d next=%d tags=%v", seq, next, tags)
	}

	// torn write on B (seq=4 partially written) -> fall back to A
	torn := encodeTagsSlot(4, map[string][]byte{"k": []byte("v4")})
	copy(area[slot:], torn[:len(torn)-2])
	area[slot+len(torn)-1] = 0
	copy(area[0:], encodeTagsSlot(3, map[string][]byte{"k": []byte("v3")}))
	tags, seq, next = pickTagsSlot(area)
	if seq != 3 || next != 1 || !bytes.Equal(tags["k"], []byte("v3")) {
		t.Fatalf("torn B: seq=%d next=%d tags=%v", seq, next, tags)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestTags -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/cursor.go`

```go
package gmc

import "encoding/binary"

// cursor is a bounds-checked sequential reader over a byte slice.
// Any out-of-range access sets bad and returns zero values.
type cursor struct {
	b   []byte
	pos int
	bad bool
}

func (c *cursor) need(n int) bool {
	if c.bad || n < 0 || c.pos+n > len(c.b) {
		c.bad = true
		return false
	}
	return true
}

func (c *cursor) u8() byte {
	if !c.need(1) {
		return 0
	}
	v := c.b[c.pos]
	c.pos++
	return v
}

func (c *cursor) u16() uint16 {
	if !c.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(c.b[c.pos:])
	c.pos += 2
	return v
}

func (c *cursor) u32() uint32 {
	if !c.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(c.b[c.pos:])
	c.pos += 4
	return v
}

func (c *cursor) u64() uint64 {
	if !c.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(c.b[c.pos:])
	c.pos += 8
	return v
}

func (c *cursor) bytes(n int) []byte {
	if !c.need(n) {
		return nil
	}
	v := c.b[c.pos : c.pos+n]
	c.pos += n
	return v
}
```

`gmc/tags.go`:

```go
package gmc

import (
	"encoding/binary"
	"hash/crc32"
	"sort"
)

// Well-known tag keys. The "gmc." prefix is reserved.
const (
	TagStartTime = "gmc.start_time_unix_ns"
	TagLocation  = "gmc.location"
)

// encodeTagsSlot serializes a tags snapshot:
//
//	seq(8) entryCount(2) { keyLen(2) key valLen(4) val }* crc(4)
//
// Keys are sorted for deterministic output. seq must be >= 1.
func encodeTagsSlot(seq uint64, tags map[string][]byte) []byte {
	keys := make([]string, 0, len(tags))
	for k := range tags {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b := binary.LittleEndian.AppendUint64(nil, seq)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(keys)))
	for _, k := range keys {
		b = binary.LittleEndian.AppendUint16(b, uint16(len(k)))
		b = append(b, k...)
		b = binary.LittleEndian.AppendUint32(b, uint32(len(tags[k])))
		b = append(b, tags[k]...)
	}
	return binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
}

// decodeTagsSlot parses one slot. Returns ok=false for zero-filled, torn or
// otherwise invalid slots (seq 0 is reserved as invalid).
func decodeTagsSlot(b []byte) (uint64, map[string][]byte, bool) {
	c := &cursor{b: b}
	seq := c.u64()
	n := int(c.u16())
	tags := make(map[string][]byte, n)
	for i := 0; i < n; i++ {
		klen := int(c.u16())
		k := string(c.bytes(klen))
		vlen := int(c.u32())
		v := c.bytes(vlen)
		if c.bad {
			return 0, nil, false
		}
		tags[k] = append([]byte(nil), v...)
	}
	body := c.pos
	crc := c.u32()
	if c.bad || seq == 0 {
		return 0, nil, false
	}
	if crc32.Checksum(b[:body], castagnoli) != crc {
		return 0, nil, false
	}
	return seq, tags, true
}

// pickTagsSlot inspects both slots of the tags area and returns the adopted
// snapshot (nil if none valid), its seq, and the slot index to write next.
func pickTagsSlot(area []byte) (map[string][]byte, uint64, int) {
	slot := len(area) / 2
	seqA, tagsA, okA := decodeTagsSlot(area[:slot])
	seqB, tagsB, okB := decodeTagsSlot(area[slot:])
	switch {
	case okA && (!okB || seqA >= seqB):
		return tagsA, seqA, 1
	case okB:
		return tagsB, seqB, 0
	default:
		return nil, 0, 0
	}
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestTags -v` 그리고 `go test ./gmc/ -run TestPickTagsSlot -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/cursor.go gmc/tags.go gmc/tags_test.go
git commit -m "구현: Tags 슬롯 코덱과 더블 슬롯 채택 로직 (torn write 폴백 포함)"
```

---

### 태스크 4: TrackInfo·Data 페이로드 코덱

**Files:**
- Create: `gmc/track.go`, `gmc/data.go`
- Test: `gmc/track_test.go`

**Interfaces:**
- Consumes: `cursor` (태스크 3), `ErrCorrupt` (태스크 1)
- Produces (공개): `type TrackID uint16`, `type TrackKind uint8`, `KindVideo TrackKind = 0` / `KindAudio = 1` / `KindData = 2`, `type TrackInfo struct { ID TrackID; Kind TrackKind; Codec string; TimebaseNum, TimebaseDen uint32; Private []byte }`
- Produces (내부): `encodeTrackInfo(info TrackInfo) []byte`, `decodeTrackInfo(p []byte) (TrackInfo, int, error)` — 두 번째 반환값은 소비한 바이트 수(Footer 파싱용)
- Produces (내부): `const dataHeaderSize = 11`, `const flagKeyframe byte = 0x01`, `encodeDataPayload(dst []byte, id TrackID, flags byte, pts uint64, data []byte) []byte`, `decodeDataHeader(p []byte) (TrackID, byte, uint64, error)` — (트랙, flags, pts) 반환. 프레임 본문은 `p[dataHeaderSize:]`

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/track_test.go`

```go
package gmc

import (
	"bytes"
	"errors"
	"testing"
)

func TestTrackInfoRoundtrip(t *testing.T) {
	info := TrackInfo{
		ID: 3, Kind: KindAudio, Codec: "pcm_s16le",
		TimebaseNum: 1, TimebaseDen: 48000,
		Private: []byte{0x01, 0x02},
	}
	p := encodeTrackInfo(info)
	// trailing bytes must not confuse the decoder (self-delimiting)
	got, n, err := decodeTrackInfo(append(p, 0xAA, 0xBB))
	if err != nil {
		t.Fatal(err)
	}
	if n != len(p) {
		t.Fatalf("consumed = %d, want %d", n, len(p))
	}
	if got.ID != 3 || got.Kind != KindAudio || got.Codec != "pcm_s16le" ||
		got.TimebaseDen != 48000 || !bytes.Equal(got.Private, info.Private) {
		t.Fatalf("got %+v", got)
	}
}

func TestTrackInfoTruncated(t *testing.T) {
	p := encodeTrackInfo(TrackInfo{ID: 1, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if _, _, err := decodeTrackInfo(p[:len(p)-1]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}

func TestDataPayloadRoundtrip(t *testing.T) {
	body := []byte("frame-bytes")
	p := encodeDataPayload(nil, 5, flagKeyframe, 90000, body)
	if len(p) != dataHeaderSize+len(body) {
		t.Fatalf("len = %d", len(p))
	}
	id, flags, pts, err := decodeDataHeader(p)
	if err != nil {
		t.Fatal(err)
	}
	if id != 5 || flags != flagKeyframe || pts != 90000 || !bytes.Equal(p[dataHeaderSize:], body) {
		t.Fatalf("id=%d flags=%d pts=%d", id, flags, pts)
	}
	if _, _, _, err := decodeDataHeader(p[:10]); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("short header: err = %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run "TestTrackInfo|TestDataPayload" -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/track.go`

```go
package gmc

import "encoding/binary"

// TrackID identifies a track within a file. Assigned by AddTrack.
type TrackID uint16

// TrackKind is a classification hint; container behavior never depends on it,
// except that the writer samples index entries for KindAudio tracks.
type TrackKind uint8

const (
	KindVideo TrackKind = 0
	KindAudio TrackKind = 1
	KindData  TrackKind = 2
)

// TrackInfo describes one track. ID is ignored on AddTrack input and filled
// in on read. All tracks share the same time origin: pts 0 is the session
// origin regardless of per-track timebase.
type TrackInfo struct {
	ID          TrackID
	Kind        TrackKind
	Codec       string
	TimebaseNum uint32
	TimebaseDen uint32
	Private     []byte
}

func encodeTrackInfo(info TrackInfo) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(info.ID))
	b = append(b, byte(info.Kind))
	b = binary.LittleEndian.AppendUint32(b, info.TimebaseNum)
	b = binary.LittleEndian.AppendUint32(b, info.TimebaseDen)
	b = binary.LittleEndian.AppendUint16(b, uint16(len(info.Codec)))
	b = append(b, info.Codec...)
	b = binary.LittleEndian.AppendUint32(b, uint32(len(info.Private)))
	return append(b, info.Private...)
}

// decodeTrackInfo parses a TrackInfo payload and reports the bytes consumed,
// so multiple encoded TrackInfos can be parsed back to back (footer).
func decodeTrackInfo(p []byte) (TrackInfo, int, error) {
	c := &cursor{b: p}
	var info TrackInfo
	info.ID = TrackID(c.u16())
	info.Kind = TrackKind(c.u8())
	info.TimebaseNum = c.u32()
	info.TimebaseDen = c.u32()
	info.Codec = string(c.bytes(int(c.u16())))
	priv := c.bytes(int(c.u32()))
	if c.bad {
		return TrackInfo{}, 0, ErrCorrupt
	}
	info.Private = append([]byte(nil), priv...)
	return info, c.pos, nil
}
```

`gmc/data.go`:

```go
package gmc

import "encoding/binary"

const (
	// dataHeaderSize is trackID(2) + flags(1) + pts(8).
	dataHeaderSize = 11

	flagKeyframe byte = 0x01
)

func encodeDataPayload(dst []byte, id TrackID, flags byte, pts uint64, data []byte) []byte {
	dst = binary.LittleEndian.AppendUint16(dst, uint16(id))
	dst = append(dst, flags)
	dst = binary.LittleEndian.AppendUint64(dst, pts)
	return append(dst, data...)
}

func decodeDataHeader(p []byte) (TrackID, byte, uint64, error) {
	if len(p) < dataHeaderSize {
		return 0, 0, 0, ErrCorrupt
	}
	return TrackID(binary.LittleEndian.Uint16(p)), p[2], binary.LittleEndian.Uint64(p[3:11]), nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run "TestTrackInfo|TestDataPayload" -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/track.go gmc/data.go gmc/track_test.go
git commit -m "구현: TrackInfo·Data 페이로드 코덱 및 공개 트랙 타입 정의"
```

---

### 태스크 5: IndexCheckpoint·Footer·트레일러 코덱

**Files:**
- Create: `gmc/checkpoint.go`, `gmc/footer.go`
- Test: `gmc/footer_test.go`

**Interfaces:**
- Consumes: `cursor`, `encodeTrackInfo`/`decodeTrackInfo`, `TrackID`, `TrackInfo`, `castagnoli`, `ErrCorrupt`, `endMagic`, `trailerSize`
- Produces: `type cpEntry struct { track TrackID; pts uint64; off int64 }`, `encodeCheckpoint(prevOff int64, entries []cpEntry) []byte`, `decodeCheckpoint(p []byte) (prevOff int64, entries []cpEntry, err error)`
- Produces: `type trackSummary struct { track TrackID; firstPTS, lastPTS, frames uint64 }`, `encodeFooter(tracks []TrackInfo, sums []trackSummary, entries []cpEntry) []byte`, `decodeFooter(p []byte) ([]TrackInfo, []trackSummary, []cpEntry, error)`, `encodeTrailer(footerOff int64) []byte`, `decodeTrailer(b []byte) (int64, bool)`

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/footer_test.go`

```go
package gmc

import (
	"testing"
)

func TestCheckpointRoundtrip(t *testing.T) {
	in := []cpEntry{{1, 0, 100}, {2, 4800, 220}, {1, 90000, 5000}}
	p := encodeCheckpoint(64, in)
	prev, got, err := decodeCheckpoint(p)
	if err != nil {
		t.Fatal(err)
	}
	if prev != 64 || len(got) != 3 || got[2] != (cpEntry{1, 90000, 5000}) {
		t.Fatalf("prev=%d got=%v", prev, got)
	}
	if _, _, err := decodeCheckpoint(p[:len(p)-1]); err == nil {
		t.Fatal("truncated checkpoint accepted")
	}
}

func TestFooterRoundtrip(t *testing.T) {
	tracks := []TrackInfo{
		{ID: 1, Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000, Private: []byte{1}},
		{ID: 2, Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000},
	}
	sums := []trackSummary{{1, 0, 180000, 61}, {2, 0, 96000, 100}}
	entries := []cpEntry{{1, 0, 100}, {1, 90000, 9000}}
	p := encodeFooter(tracks, sums, entries)
	gt, gs, ge, err := decodeFooter(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(gt) != 2 || gt[0].Codec != "h264" || gt[1].TimebaseDen != 48000 {
		t.Fatalf("tracks = %+v", gt)
	}
	if len(gs) != 2 || gs[1] != (trackSummary{2, 0, 96000, 100}) {
		t.Fatalf("sums = %+v", gs)
	}
	if len(ge) != 2 || ge[1] != (cpEntry{1, 90000, 9000}) {
		t.Fatalf("entries = %+v", ge)
	}
	if _, _, _, err := decodeFooter(p[:len(p)-3]); err == nil {
		t.Fatal("truncated footer accepted")
	}
}

func TestTrailerRoundtrip(t *testing.T) {
	b := encodeTrailer(123456)
	if len(b) != trailerSize {
		t.Fatalf("len = %d", len(b))
	}
	off, ok := decodeTrailer(b)
	if !ok || off != 123456 {
		t.Fatalf("off=%d ok=%v", off, ok)
	}
	bad := append([]byte(nil), b...)
	bad[3] ^= 0xFF
	if _, ok := decodeTrailer(bad); ok {
		t.Fatal("corrupt trailer accepted")
	}
	if _, ok := decodeTrailer(make([]byte, trailerSize)); ok {
		t.Fatal("zero trailer accepted")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run "TestCheckpoint|TestFooter|TestTrailer" -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/checkpoint.go`

```go
package gmc

import "encoding/binary"

// cpEntry is one sync-point index entry: (track, pts) -> file offset of the
// Data chunk. Used by checkpoints, the footer, and the in-memory index.
type cpEntry struct {
	track TrackID
	pts   uint64
	off   int64
}

const cpEntrySize = 18 // track(2) + pts(8) + off(8)

func appendCPEntries(b []byte, entries []cpEntry) []byte {
	for _, e := range entries {
		b = binary.LittleEndian.AppendUint16(b, uint16(e.track))
		b = binary.LittleEndian.AppendUint64(b, e.pts)
		b = binary.LittleEndian.AppendUint64(b, uint64(e.off))
	}
	return b
}

func readCPEntries(c *cursor, n int) []cpEntry {
	if n < 0 || !c.need(n*cpEntrySize) {
		c.bad = true
		return nil
	}
	entries := make([]cpEntry, 0, n)
	for i := 0; i < n; i++ {
		e := cpEntry{}
		e.track = TrackID(c.u16())
		e.pts = c.u64()
		e.off = int64(c.u64())
		entries = append(entries, e)
	}
	return entries
}

func encodeCheckpoint(prevOff int64, entries []cpEntry) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(prevOff))
	b = binary.LittleEndian.AppendUint32(b, uint32(len(entries)))
	return appendCPEntries(b, entries)
}

func decodeCheckpoint(p []byte) (int64, []cpEntry, error) {
	c := &cursor{b: p}
	prev := int64(c.u64())
	n := int(c.u32())
	entries := readCPEntries(c, n)
	if c.bad {
		return 0, nil, ErrCorrupt
	}
	return prev, entries, nil
}
```

`gmc/footer.go`:

```go
package gmc

import (
	"encoding/binary"
	"hash/crc32"
)

// trackSummary is per-track summary info stored in the footer.
type trackSummary struct {
	track    TrackID
	firstPTS uint64
	lastPTS  uint64
	frames   uint64
}

// encodeFooter serializes the consolidated footer:
//
//	trackCount(2) TrackInfo* summaryCount(2) summary* entryCount(4) cpEntry*
func encodeFooter(tracks []TrackInfo, sums []trackSummary, entries []cpEntry) []byte {
	b := binary.LittleEndian.AppendUint16(nil, uint16(len(tracks)))
	for _, tr := range tracks {
		b = append(b, encodeTrackInfo(tr)...)
	}
	b = binary.LittleEndian.AppendUint16(b, uint16(len(sums)))
	for _, s := range sums {
		b = binary.LittleEndian.AppendUint16(b, uint16(s.track))
		b = binary.LittleEndian.AppendUint64(b, s.firstPTS)
		b = binary.LittleEndian.AppendUint64(b, s.lastPTS)
		b = binary.LittleEndian.AppendUint64(b, s.frames)
	}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(entries)))
	return appendCPEntries(b, entries)
}

func decodeFooter(p []byte) ([]TrackInfo, []trackSummary, []cpEntry, error) {
	c := &cursor{b: p}
	nt := int(c.u16())
	if c.bad {
		return nil, nil, nil, ErrCorrupt
	}
	tracks := make([]TrackInfo, 0, nt)
	for i := 0; i < nt; i++ {
		info, n, err := decodeTrackInfo(p[c.pos:])
		if err != nil {
			return nil, nil, nil, err
		}
		c.pos += n
		tracks = append(tracks, info)
	}
	ns := int(c.u16())
	if ns < 0 || !c.need(ns*26) {
		return nil, nil, nil, ErrCorrupt
	}
	sums := make([]trackSummary, 0, ns)
	for i := 0; i < ns; i++ {
		s := trackSummary{}
		s.track = TrackID(c.u16())
		s.firstPTS = c.u64()
		s.lastPTS = c.u64()
		s.frames = c.u64()
		sums = append(sums, s)
	}
	ne := int(c.u32())
	entries := readCPEntries(c, ne)
	if c.bad {
		return nil, nil, nil, ErrCorrupt
	}
	return tracks, sums, entries, nil
}

// encodeTrailer builds the fixed 16-byte trailer:
//
//	footerOffset(8) crc(4) endMagic(4)
func encodeTrailer(footerOff int64) []byte {
	b := binary.LittleEndian.AppendUint64(nil, uint64(footerOff))
	b = binary.LittleEndian.AppendUint32(b, crc32.Checksum(b, castagnoli))
	return append(b, endMagic...)
}

func decodeTrailer(b []byte) (int64, bool) {
	if len(b) != trailerSize || string(b[12:16]) != endMagic {
		return 0, false
	}
	if crc32.Checksum(b[:8], castagnoli) != binary.LittleEndian.Uint32(b[8:12]) {
		return 0, false
	}
	return int64(binary.LittleEndian.Uint64(b[:8])), true
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run "TestCheckpoint|TestFooter|TestTrailer" -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/checkpoint.go gmc/footer.go gmc/footer_test.go
git commit -m "구현: IndexCheckpoint·Footer·트레일러 코덱"
```

---

### 태스크 6: in-memory 인덱스

**Files:**
- Create: `gmc/index.go`
- Test: `gmc/index_test.go`

**Interfaces:**
- Consumes: `TrackID`, `cpEntry` (태스크 4·5)
- Produces: `type fileIndex struct{...}` (RWMutex 내장), `newFileIndex() *fileIndex`, `(ix *fileIndex) add(id TrackID, pts uint64, off int64)` — pts 오름차순 append 가정, `(ix *fileIndex) seek(id TrackID, pts uint64) (off int64, ok bool)` — 목표 pts 이하 마지막 엔트리, `(ix *fileIndex) dump() []cpEntry` — 트랙 ID 오름차순 전체 덤프(Footer용)

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/index_test.go`

```go
package gmc

import "testing"

func TestIndexSeek(t *testing.T) {
	ix := newFileIndex()
	ix.add(1, 0, 100)
	ix.add(1, 90000, 5000)
	ix.add(1, 180000, 9000)
	ix.add(2, 0, 150)

	cases := []struct {
		pts     uint64
		wantOff int64
		wantOK  bool
	}{
		{0, 100, true},        // exact first
		{89999, 100, true},    // between -> previous
		{90000, 5000, true},   // exact middle
		{500000, 9000, true},  // beyond last -> last
	}
	for _, c := range cases {
		off, ok := ix.seek(1, c.pts)
		if ok != c.wantOK || off != c.wantOff {
			t.Fatalf("seek(1, %d) = (%d, %v), want (%d, %v)", c.pts, off, ok, c.wantOff, c.wantOK)
		}
	}
	if _, ok := ix.seek(9, 0); ok {
		t.Fatal("unknown track must return ok=false")
	}
}

func TestIndexDump(t *testing.T) {
	ix := newFileIndex()
	ix.add(2, 0, 150)
	ix.add(1, 0, 100)
	ix.add(1, 90000, 5000)
	got := ix.dump()
	want := []cpEntry{{1, 0, 100}, {1, 90000, 5000}, {2, 0, 150}}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("dump[%d] = %v, want %v", i, got[i], want[i])
		}
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestIndex -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/index.go`

```go
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestIndex -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/index.go gmc/index_test.go
git commit -m "구현: in-memory 인덱스 (이진 탐색 시크, Footer용 덤프)"
```

---

### 태스크 7: Writer 기본 — Create / AddTrack / WriteFrame / Sync / Close

**Files:**
- Create: `gmc/writer.go`
- Test: `gmc/writer_test.go`

**Interfaces:**
- Consumes: 태스크 1~6 전부
- Produces (공개): `type Frame struct { PTS uint64; Keyframe bool; Data []byte }`, `type CreateOptions struct { Private []byte; TagsAreaSize int; CheckpointBytes int64; CheckpointInterval time.Duration }`, `Create(path string, opts CreateOptions) (*Writer, error)`, `(w *Writer) AddTrack(info TrackInfo) (TrackID, error)`, `(w *Writer) WriteFrame(id TrackID, fr Frame) error`, `(w *Writer) Sync() error`, `(w *Writer) Close() error`
- Produces (내부, 후속 태스크가 사용): `Writer` 필드 — `mu sync.Mutex`, `cond *sync.Cond`, `f *os.File`, `path string`, `private []byte`, `closed bool`, `committed atomic.Int64`, `streamStart int64`, `tagsOff int64`, `slotSize int`, `tagsSeq uint64`, `nextSlot int`, `tags map[string][]byte`, `tracks map[TrackID]*trackState`, `nextTrack TrackID`, `idx *fileIndex`, `pending []cpEntry`, `prevCPOff int64`, `lastCPEnd int64`, `lastCPTime time.Time`, `cpBytes int64`, `cpInterval time.Duration`, `scratch, chunkBuf []byte`
- Produces (내부): `type trackState struct { info TrackInfo; hasLast bool; firstPTS, lastPTS uint64; frames uint64; indexedThisCP bool }`, `(w *Writer) appendChunkLocked(typ byte, payload []byte) error`, `(ts *trackState) shouldIndexSync() bool`
- 참고: 이 태스크에서 `maybeCheckpointLocked`는 아직 없다 — WriteFrame은 인덱스/pending 누적까지만 하고, 체크포인트 기록은 태스크 9에서 추가한다.

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/writer_test.go`

```go
package gmc

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// scanChunks reads every chunk of the file for test verification.
type rawChunk struct {
	typ     byte
	payload []byte
	off     int64
}

func scanChunks(t *testing.T, path string, start, limit int64) []rawChunk {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []rawChunk
	off := start
	for {
		typ, payload, next, err := readChunkAt(f, off, limit)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("scan at %d: %v", off, err)
		}
		out = append(out, rawChunk{typ, payload, off})
		off = next
	}
}

func newTestWriter(t *testing.T, opts CreateOptions) (*Writer, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.gmc")
	w, err := Create(path, opts)
	if err != nil {
		t.Fatal(err)
	}
	return w, path
}

func TestCreateWritesHeaderAndTagsArea(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{Private: []byte("manifest"), TagsAreaSize: 1024})
	defer w.Close()

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	hdr, hlen, err := decodeFileHeader(f)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(hdr.private, []byte("manifest")) || hdr.tagsAreaLen != 1024 {
		t.Fatalf("hdr = %+v", hdr)
	}
	if w.streamStart != hlen+1024 || w.committed.Load() != w.streamStart {
		t.Fatalf("streamStart=%d committed=%d", w.streamStart, w.committed.Load())
	}
}

func TestCreateRefusesExistingFile(t *testing.T) {
	_, path := newTestWriter(t, CreateOptions{})
	if _, err := Create(path, CreateOptions{}); err == nil {
		t.Fatal("duplicate Create must fail")
	}
}

func TestWriteFrameAppendsChunks(t *testing.T) {
	// 체크포인트가 끼어들지 않도록 트리거를 사실상 꺼 둔다 (청크 수를 단정하므로)
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	video, err := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf0")}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p1")}); err != nil {
		t.Fatal(err)
	}

	chunks := scanChunks(t, path, w.streamStart, w.committed.Load())
	if len(chunks) != 3 || chunks[0].typ != chunkTrackInfo || chunks[1].typ != chunkData {
		t.Fatalf("chunks = %+v", chunks)
	}
	id, flags, pts, err := decodeDataHeader(chunks[1].payload)
	if err != nil || id != video || flags != flagKeyframe || pts != 0 {
		t.Fatalf("frame0: id=%d flags=%d pts=%d err=%v", id, flags, pts, err)
	}
	if !bytes.Equal(chunks[2].payload[dataHeaderSize:], []byte("p1")) {
		t.Fatal("frame1 body mismatch")
	}
	// keyframe indexed at its chunk offset
	off, ok := w.idx.seek(video, 0)
	if !ok || off != chunks[1].off {
		t.Fatalf("index off=%d ok=%v want %d", off, ok, chunks[1].off)
	}
	w.Close()
}

func TestWriteFrameValidation(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})

	if err := w.WriteFrame(99, Frame{}); !errors.Is(err, ErrUnknownTrack) {
		t.Fatalf("unknown track: err = %v", err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 100}); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 99}); !errors.Is(err, ErrNonMonotonicPTS) {
		t.Fatalf("pts regression: err = %v", err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 100}); err != nil {
		t.Fatalf("equal pts must be allowed: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.WriteFrame(video, Frame{PTS: 200}); !errors.Is(err, ErrClosed) {
		t.Fatalf("after close: err = %v", err)
	}
	if err := w.Close(); !errors.Is(err, ErrClosed) {
		t.Fatalf("double close: err = %v", err)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run "TestCreate|TestWriteFrame" -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/writer.go`

```go
package gmc

import (
	"errors"
	"os"
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
	return nil
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run "TestCreate|TestWriteFrame" -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/writer.go gmc/writer_test.go
git commit -m "구현: Writer 기본 경로 (Create·AddTrack·WriteFrame·Sync·Close, committedSize·pts 검증)"
```

---

### 태스크 8: Writer Tags — SetTag / SetStartTime

**Files:**
- Modify: `gmc/writer.go` (메서드 추가)
- Test: `gmc/writer_tags_test.go`

**Interfaces:**
- Consumes: `encodeTagsSlot`/`pickTagsSlot` (태스크 3), Writer 필드 `tagsOff`/`slotSize`/`tagsSeq`/`nextSlot`/`tags` (태스크 7)
- Produces (공개): `(w *Writer) SetTag(key string, value []byte) error`, `(w *Writer) SetStartTime(t time.Time) error`
- Produces (내부): `(w *Writer) tagsSnapshot() map[string][]byte` — 태스크 13에서 라이브 Reader가 사용

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/writer_tags_test.go`

```go
package gmc

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"
)

func readTagsArea(t *testing.T, path string, w *Writer) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	area := make([]byte, w.slotSize*2)
	if _, err := f.ReadAt(area, w.tagsOff); err != nil {
		t.Fatal(err)
	}
	tags, _, _ := pickTagsSlot(area)
	return tags
}

func TestSetTagWritesSlots(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{TagsAreaSize: 1024})
	defer w.Close()

	if err := w.SetTag(TagLocation, []byte("seoul")); err != nil {
		t.Fatal(err)
	}
	if got := readTagsArea(t, path, w); !bytes.Equal(got[TagLocation], []byte("seoul")) {
		t.Fatalf("tags = %v", got)
	}

	// update flips to the other slot and newer seq wins
	if err := w.SetTag(TagLocation, []byte("busan")); err != nil {
		t.Fatal(err)
	}
	if err := w.SetTag("camera.id", []byte("cam-03")); err != nil {
		t.Fatal(err)
	}
	got := readTagsArea(t, path, w)
	if !bytes.Equal(got[TagLocation], []byte("busan")) || !bytes.Equal(got["camera.id"], []byte("cam-03")) {
		t.Fatalf("tags = %v", got)
	}
}

func TestSetStartTime(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	defer w.Close()
	now := time.Unix(1751500000, 123456789)
	if err := w.SetStartTime(now); err != nil {
		t.Fatal(err)
	}
	got := readTagsArea(t, path, w)
	if len(got[TagStartTime]) != 8 {
		t.Fatalf("start tag = %v", got[TagStartTime])
	}
}

func TestSetTagTooLarge(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{TagsAreaSize: 256}) // slot = 128 bytes
	defer w.Close()
	if err := w.SetTag("k", make([]byte, 200)); !errors.Is(err, ErrTagsTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// failed SetTag must not leave partial state
	if len(w.tagsSnapshot()) != 0 {
		t.Fatal("tags map mutated by failed SetTag")
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run "TestSetTag|TestSetStartTime" -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/writer.go`에 추가

```go
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
```

(`writer.go` import에 `"encoding/binary"` 추가)

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run "TestSetTag|TestSetStartTime" -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/writer.go gmc/writer_tags_test.go
git commit -m "구현: Writer Tags 갱신 (더블 슬롯 제자리 기록, SetStartTime 편의 API)"
```

---

### 태스크 9: 체크포인트 기록 정책

**Files:**
- Modify: `gmc/writer.go` (`maybeCheckpointLocked` 추가, `WriteFrame` 마지막에 호출)
- Test: `gmc/writer_checkpoint_test.go`

**Interfaces:**
- Consumes: `encodeCheckpoint`/`decodeCheckpoint` (태스크 5), Writer 필드 `pending`/`prevCPOff`/`lastCPEnd`/`lastCPTime`/`cpBytes`/`cpInterval` (태스크 7)
- Produces: `(w *Writer) maybeCheckpointLocked() error` — 바이트/시간 트리거 시 pending 엔트리로 IndexCheckpoint 청크 기록, prev chain 유지, 오디오 샘플링 플래그 리셋. `WriteFrame`이 프레임 기록 후 호출하도록 수정

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/writer_checkpoint_test.go`

```go
package gmc

import (
	"testing"
	"time"
)

// 바이트 트리거: cpBytes=1이면 sync point가 쌓일 때마다 체크포인트가 기록된다.
func TestCheckpointByteTrigger(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{CheckpointBytes: 1, CheckpointInterval: time.Hour})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})

	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf0")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})
	w.WriteFrame(video, Frame{PTS: 90000, Keyframe: true, Data: []byte("kf1")})

	var cps []rawChunk
	for _, c := range scanChunks(t, path, w.streamStart, w.committed.Load()) {
		if c.typ == chunkCheckpoint {
			cps = append(cps, c)
		}
	}
	if len(cps) != 2 {
		t.Fatalf("checkpoints = %d, want 2", len(cps))
	}
	prev0, e0, err := decodeCheckpoint(cps[0].payload)
	if err != nil || prev0 != 0 || len(e0) != 1 || e0[0].pts != 0 {
		t.Fatalf("cp0: prev=%d entries=%v err=%v", prev0, e0, err)
	}
	prev1, e1, err := decodeCheckpoint(cps[1].payload)
	if err != nil || prev1 != cps[0].off || len(e1) != 1 || e1[0].pts != 90000 {
		t.Fatalf("cp1: prev=%d (want %d) entries=%v err=%v", prev1, cps[0].off, e1, err)
	}
	w.Close()
}

// 오디오 샘플링: 한 체크포인트 구간에서 트랙당 첫 sync point 1개만 인덱싱.
func TestCheckpointAudioSampling(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1 << 30, CheckpointInterval: time.Hour})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})

	for i := 0; i < 10; i++ {
		w.WriteFrame(audio, Frame{PTS: uint64(i * 960), Keyframe: true, Data: make([]byte, 100)})
	}
	if len(w.pending) != 1 || w.pending[0].pts != 0 {
		t.Fatalf("pending = %v, want single entry at pts 0", w.pending)
	}
	w.Close()
}

// 체크포인트 이후 샘플링 플래그가 리셋되어 다음 구간의 첫 오디오 프레임이 다시 인덱싱된다.
func TestCheckpointResetsSampling(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{CheckpointBytes: 1, CheckpointInterval: time.Hour})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})

	w.WriteFrame(audio, Frame{PTS: 0, Keyframe: true, Data: []byte("a")})    // indexed + cp
	w.WriteFrame(audio, Frame{PTS: 4096, Keyframe: true, Data: []byte("b")}) // new interval -> indexed
	if _, ok := w.idx.seek(audio, 4096); !ok {
		t.Fatal("second interval sync point not indexed")
	}
	off, _ := w.idx.seek(audio, 4096)
	off0, _ := w.idx.seek(audio, 0)
	if off == off0 {
		t.Fatal("expected two distinct index entries")
	}
	w.Close()
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestCheckpointByteTrigger -v`
Expected: `FAIL` (체크포인트 청크가 0개)

- [ ] **Step 3: 구현** — `gmc/writer.go`

`WriteFrame`의 마지막 `return nil`을 다음으로 교체:

```go
	return w.maybeCheckpointLocked()
```

메서드 추가:

```go
// maybeCheckpointLocked writes an IndexCheckpoint chunk when the byte or time
// threshold since the last checkpoint has been reached. Callers hold w.mu.
func (w *Writer) maybeCheckpointLocked() error {
	if w.committed.Load()-w.lastCPEnd < w.cpBytes && time.Since(w.lastCPTime) < w.cpInterval {
		return nil
	}
	w.lastCPTime = time.Now()
	w.lastCPEnd = w.committed.Load()
	for _, ts := range w.tracks {
		ts.indexedThisCP = false
	}
	if len(w.pending) == 0 {
		return nil
	}
	off := w.committed.Load()
	payload := encodeCheckpoint(w.prevCPOff, w.pending)
	if err := w.appendChunkLocked(chunkCheckpoint, payload); err != nil {
		return err
	}
	w.prevCPOff = off
	w.pending = w.pending[:0]
	w.lastCPEnd = w.committed.Load()
	return nil
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestCheckpoint -v` 이후 전체 회귀 `go test ./gmc/ -v`
Expected: `PASS` (기존 테스트 포함 전부)

- [ ] **Step 5: 커밋**

```powershell
git add gmc/writer.go gmc/writer_checkpoint_test.go
git commit -m "구현: 인덱스 체크포인트 기록 정책 (바이트·시간 트리거, backward chain, 오디오 샘플링)"
```

---

### 태스크 10: Finalize — Footer + 트레일러

**Files:**
- Modify: `gmc/writer.go`
- Test: `gmc/writer_finalize_test.go`

**Interfaces:**
- Consumes: `encodeFooter`/`encodeTrailer`/`decodeFooter`/`decodeTrailer` (태스크 5), `fileIndex.dump` (태스크 6)
- Produces (공개): `(w *Writer) Finalize() error` — Footer 청크 + 트레일러 기록, fsync 후 close

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/writer_finalize_test.go`

```go
package gmc

import (
	"errors"
	"os"
	"testing"
)

func TestFinalizeWritesFooterAndTrailer(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "flac", TimebaseNum: 1, TimebaseDen: 48000})
	w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("kf")})
	w.WriteFrame(audio, Frame{PTS: 0, Keyframe: true, Data: []byte("a0")})
	w.WriteFrame(video, Frame{PTS: 3000, Data: []byte("p")})

	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	if err := w.Finalize(); !errors.Is(err, ErrClosed) {
		t.Fatalf("double finalize: err = %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fi, _ := f.Stat()
	size := fi.Size()

	tb := make([]byte, trailerSize)
	if _, err := f.ReadAt(tb, size-trailerSize); err != nil {
		t.Fatal(err)
	}
	footerOff, ok := decodeTrailer(tb)
	if !ok {
		t.Fatal("invalid trailer")
	}
	typ, payload, next, err := readChunkAt(f, footerOff, size-trailerSize)
	if err != nil || typ != chunkFooter || next != size-trailerSize {
		t.Fatalf("footer chunk: typ=%d next=%d err=%v", typ, next, err)
	}
	tracks, sums, entries, err := decodeFooter(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(tracks) != 2 || tracks[0].ID != video || tracks[1].Codec != "flac" {
		t.Fatalf("tracks = %+v", tracks)
	}
	if len(sums) != 2 || sums[0].frames != 2 || sums[0].lastPTS != 3000 || sums[1].frames != 1 {
		t.Fatalf("sums = %+v", sums)
	}
	if len(entries) != 2 { // video kf + audio first sync
		t.Fatalf("entries = %+v", entries)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestFinalize -v`
Expected: `FAIL ... [build failed]` (undefined: (*Writer).Finalize)

- [ ] **Step 3: 구현** — `gmc/writer.go`에 추가 (import에 `"sort"` 추가)

```go
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestFinalize -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/writer.go gmc/writer_finalize_test.go
git commit -m "구현: Finalize (Footer 통합 인덱스·트랙 사본·요약 + 트레일러 기록)"
```

---

### 태스크 11: Open — 완성 파일 (트레일러→Footer 경로)

**Files:**
- Create: `gmc/reader.go`
- Test: `gmc/reader_test.go`

**Interfaces:**
- Consumes: 태스크 1~5 코덱, `fileIndex` (태스크 6)
- Produces (공개): `Open(path string) (*Reader, error)`, `(r *Reader) Tracks() []TrackInfo` (ID 오름차순), `(r *Reader) FilePrivate() []byte`, `(r *Reader) Tags() map[string][]byte` (복사본), `(r *Reader) StartTime() (time.Time, bool)`, `(r *Reader) Close() error`
- Produces (내부): `Reader` 필드 — `f *os.File`, `w *Writer`(라이브면 non-nil, 이 태스크에서는 항상 nil), `idx *fileIndex`, `committed *atomic.Int64`, `streamStart int64`, `private []byte`, `tags map[string][]byte`, `tracks map[TrackID]TrackInfo`; `(r *Reader) loadFooter(size int64) error`, `(r *Reader) hasTrack(id TrackID) bool`, `(r *Reader) trackIDs() []TrackID`
- 참고: 스캔 복구(`scan`)는 태스크 12. 이 태스크의 Open은 트레일러가 무효면 임시로 `ErrCorrupt`를 반환하고, 태스크 12에서 스캔 폴백으로 교체한다.

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/reader_test.go`

```go
package gmc

import (
	"bytes"
	"path/filepath"
	"testing"
	"time"
)

// buildFinalizedFile creates a finalized two-track file and returns its path.
func buildFinalizedFile(t *testing.T) (string, TrackID, TrackID) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "done.gmc")
	w, err := Create(path, CreateOptions{Private: []byte("manifest")})
	if err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})
	w.SetStartTime(time.Unix(1751500000, 0))
	w.SetTag(TagLocation, []byte("seoul"))
	for i := 0; i < 30; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)}})
		w.WriteFrame(audio, Frame{PTS: uint64(i * 1600), Keyframe: true, Data: []byte{0xA0, byte(i)}})
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path, video, audio
}

func TestOpenFinalized(t *testing.T) {
	path, video, audio := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	if r.w != nil {
		t.Fatal("opened reader must not be live")
	}
	tracks := r.Tracks()
	if len(tracks) != 2 || tracks[0].ID != video || tracks[1].ID != audio || tracks[1].Codec != "pcm_s16le" {
		t.Fatalf("tracks = %+v", tracks)
	}
	if !bytes.Equal(r.FilePrivate(), []byte("manifest")) {
		t.Fatalf("private = %q", r.FilePrivate())
	}
	if !bytes.Equal(r.Tags()[TagLocation], []byte("seoul")) {
		t.Fatalf("tags = %v", r.Tags())
	}
	start, ok := r.StartTime()
	if !ok || start.Unix() != 1751500000 {
		t.Fatalf("start = %v ok=%v", start, ok)
	}
	// index loaded from footer: video keyframes at pts 0, 30000, 60000
	if _, ok := r.idx.seek(video, 30000); !ok {
		t.Fatal("footer index missing video keyframe")
	}
	if r.committed.Load() <= r.streamStart {
		t.Fatalf("committed = %d", r.committed.Load())
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestOpenFinalized -v`
Expected: `FAIL ... [build failed]` (undefined: Open)

- [ ] **Step 3: 구현** — `gmc/reader.go`

```go
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
		// 태스크 12에서 r.scan(size) 폴백으로 교체한다.
		f.Close()
		return nil, err
	}
	return r, nil
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
		out[k] = v
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

// Close closes the reader's file handle.
func (r *Reader) Close() error {
	return r.f.Close()
}
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestOpenFinalized -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/reader.go gmc/reader_test.go
git commit -m "구현: Open 완성 파일 경로 (트레일러 검증 후 Footer 일괄 로드, Reader 접근자)"
```

---

### 태스크 12: Open — 미완성/크래시 파일 스캔 복구

**Files:**
- Modify: `gmc/reader.go` (`scan` 추가, Open의 loadFooter 실패 시 폴백)
- Test: `gmc/reader_recover_test.go`

**Interfaces:**
- Consumes: 태스크 11의 Reader, `readChunkAt`, 코덱 전부
- Produces: `(r *Reader) scan(size int64)` — CRC 전수 검증 순차 스캔, 트랙 등록, 체크포인트 병합, 꼬리 sync point 보충, 논리적 EOF 확정. `Open`은 `loadFooter` 실패 시 `scan`으로 폴백하도록 수정

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/reader_recover_test.go`

```go
package gmc

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildUnfinalized writes frames without Finalize and returns the raw bytes.
// cpBytes로 체크포인트 밀도를 제어한다 (1<<30이면 체크포인트 없음 — 청크 구성이
// TrackInfo + Data×20으로 결정적이 되어 절단 테스트의 기대값이 흔들리지 않는다).
func buildUnfinalized(t *testing.T, cpBytes int64) (data []byte, video TrackID, lastCommitted int64, streamStart int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "crash.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: cpBytes, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	video, _ = w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	for i := 0; i < 20; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%5 == 0, Data: make([]byte, 50)})
	}
	lastCommitted = w.committed.Load()
	streamStart = w.streamStart
	if err := w.Close(); err != nil { // Close without Finalize
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data, video, lastCommitted, streamStart
}

func writeTemp(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "case.gmc")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func countFrames(t *testing.T, r *Reader) int {
	t.Helper()
	n := 0
	off := r.streamStart
	for {
		typ, _, next, err := readChunkAt(r.f, off, r.committed.Load())
		if err != nil {
			return n
		}
		if typ == chunkData {
			n++
		}
		off = next
	}
}

func TestOpenUnfinalizedFull(t *testing.T) {
	data, video, lastCommitted, _ := buildUnfinalized(t, 200) // 체크포인트 여러 개 포함
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() != lastCommitted {
		t.Fatalf("committed = %d, want %d", r.committed.Load(), lastCommitted)
	}
	if len(r.Tracks()) != 1 || r.Tracks()[0].ID != video {
		t.Fatalf("tracks = %+v", r.Tracks())
	}
	// keyframes at pts 0,15000,30000,45000 must be seekable (checkpoint + tail)
	if _, ok := r.idx.seek(video, 57000); !ok {
		t.Fatal("sync point missing from recovered index")
	}
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames = %d, want 20", got)
	}
}

func TestOpenTornTail(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30) // 체크포인트 없음 → 마지막 청크는 항상 Data
	// cut mid-chunk: drop 3 bytes from the tail
	r, err := Open(writeTemp(t, data[:len(data)-3]))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19 (last torn chunk dropped)", got)
	}
}

func TestOpenCorruptMidChunkStopsThere(t *testing.T) {
	data, _, lastCommitted, streamStart := buildUnfinalized(t, 1<<30)
	// corrupt one byte inside the last chunk's payload
	data[lastCommitted-10] ^= 0xFF
	r, err := Open(writeTemp(t, data))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() >= lastCommitted || r.committed.Load() <= streamStart {
		t.Fatalf("committed = %d, want < %d", r.committed.Load(), lastCommitted)
	}
	if got := countFrames(t, r); got != 19 {
		t.Fatalf("frames = %d, want 19", got)
	}
}

func TestOpenSkipsUnknownChunkType(t *testing.T) {
	data, _, _, _ := buildUnfinalized(t, 1<<30)
	unknown := appendChunk(nil, 0x7F, []byte("future extension"))
	r, err := Open(writeTemp(t, append(data, unknown...)))
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if got := countFrames(t, r); got != 20 {
		t.Fatalf("frames = %d, want 20 (unknown chunk skipped)", got)
	}
}

func TestOpenEmptyStream(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.gmc")
	w, err := Create(path, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	streamStart := w.streamStart
	w.Close()
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.committed.Load() != streamStart || len(r.Tracks()) != 0 {
		t.Fatalf("committed=%d tracks=%d", r.committed.Load(), len(r.Tracks()))
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestOpenUnfinalized -v`
Expected: `FAIL` (Open이 ErrCorrupt 반환 — 스캔 폴백 미구현)

- [ ] **Step 3: 구현** — `gmc/reader.go`

`Open`의 loadFooter 부분을 다음으로 교체:

```go
	if err := r.loadFooter(size); err != nil {
		r.scan(size)
	}
	return r, nil
```

메서드 추가:

```go
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
			if id, flags, pts, derr := decodeDataHeader(payload); derr == nil && flags&flagKeyframe != 0 {
				tail = append(tail, cpEntry{id, pts, off})
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestOpen -v` 이후 전체 회귀 `go test ./gmc/ -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/reader.go gmc/reader_recover_test.go
git commit -m "구현: 크래시/미완성 파일 스캔 복구 (CRC 전수 검증, 꼬리 절단, 미지 타입 skip)"
```

---

### 태스크 13: 같은 프로세스 Reader — NewReader / SeekPTS / Iterator

**Files:**
- Create: `gmc/iterator.go`
- Modify: `gmc/writer.go` (`NewReader` 추가)
- Test: `gmc/iterator_test.go`

**Interfaces:**
- Consumes: Reader/Writer 내부 (태스크 7·11), `readChunkAt`, `decodeDataHeader`, `fileIndex.seek`
- Produces (공개): `(w *Writer) NewReader() (*Reader, error)` — 자체 read-only fd, 인덱스·committed 공유. `(r *Reader) SeekPTS(id TrackID, pts uint64) (*Iterator, error)` — 목표 pts 이하 마지막 sync point부터 해당 트랙 프레임 순회(디코딩 워밍업을 위해 target 이전 프레임 포함). `type Iterator struct{...}` — `Next() bool`, `Frame() Frame`, `Track() TrackID`, `Err() error`

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/iterator_test.go`

```go
package gmc

import (
	"bytes"
	"testing"
)

// GOP 패턴: 키프레임 주기 10, pts 간격 3000.
func writeGOPs(t *testing.T, w *Writer, video TrackID, frames int) {
	t.Helper()
	for i := 0; i < frames; i++ {
		err := w.WriteFrame(video, Frame{
			PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeekPTSLiveReader(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	writeGOPs(t, w, video, 30)

	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// target pts 45000 (frame 15) -> starts at keyframe pts 30000 (frame 10)
	it, err := r.SeekPTS(video, 45000)
	if err != nil {
		t.Fatal(err)
	}
	var ptss []uint64
	for it.Next() {
		ptss = append(ptss, it.Frame().PTS)
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}
	if len(ptss) != 20 || ptss[0] != 30000 || ptss[len(ptss)-1] != 29*3000 {
		t.Fatalf("ptss = %v", ptss)
	}
	if !bytes.Equal(mustFrameAt(t, r, video, 30000).Data, []byte{10}) {
		t.Fatal("frame body mismatch")
	}

	// seek to pts 0 lands exactly on the first keyframe
	it2, err := r.SeekPTS(video, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !it2.Next() || it2.Frame().PTS != 0 {
		t.Fatalf("first frame pts = %d", it2.Frame().PTS)
	}

	if _, err := r.SeekPTS(99, 0); err == nil {
		t.Fatal("unknown track must error")
	}
	w.Close()
}

func mustFrameAt(t *testing.T, r *Reader, id TrackID, pts uint64) Frame {
	t.Helper()
	it, err := r.SeekPTS(id, pts)
	if err != nil {
		t.Fatal(err)
	}
	for it.Next() {
		if it.Frame().PTS == pts {
			return it.Frame()
		}
	}
	t.Fatalf("frame pts=%d not found", pts)
	return Frame{}
}

func TestSeekPTSOpenedFile(t *testing.T) {
	path, video, _ := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	it, err := r.SeekPTS(video, 45000) // keyframes at 0,30000,60000
	if err != nil {
		t.Fatal(err)
	}
	if !it.Next() || it.Frame().PTS != 30000 || !it.Frame().Keyframe {
		t.Fatalf("first = %+v", it.Frame())
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestSeekPTS -v`
Expected: `FAIL ... [build failed]` (undefined: NewReader / SeekPTS)

- [ ] **Step 3: 구현** — `gmc/writer.go`에 추가:

```go
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
```

`gmc/iterator.go`:

```go
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
```

`gmc/reader.go`에 추가:

```go
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestSeekPTS -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/iterator.go gmc/reader.go gmc/writer.go gmc/iterator_test.go
git commit -m "구현: 같은 프로세스 Reader (NewReader), SeekPTS 2단계 시크와 Iterator"
```

---

### 태스크 14: ReadInterleaved — 멀티트랙 통합 읽기

**Files:**
- Modify: `gmc/reader.go`
- Test: `gmc/interleaved_test.go`

**Interfaces:**
- Consumes: `Iterator` (태스크 13), `fileIndex.seek`
- Produces (공개): `(r *Reader) ReadInterleaved(pts uint64, tracks ...TrackID) (*Iterator, error)` — 대상 트랙(빈 인자 = 전체)의 "pts 이하 마지막 sync point" 중 **최소 오프셋**부터 저장 순서로 순회

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/interleaved_test.go`

```go
package gmc

import "testing"

func TestReadInterleaved(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	meta, _ := w.AddTrack(TrackInfo{Kind: KindData, Codec: "json", TimebaseNum: 1, TimebaseDen: 1000})

	// interleave: video frames every 3000 pts (kf every 10), meta events sparse
	for i := 0; i < 30; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)}})
		if i%7 == 0 {
			w.WriteFrame(meta, Frame{PTS: uint64(i * 33), Keyframe: true, Data: []byte("ev")})
		}
	}
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// all tracks: must start at the minimum offset across per-track sync points
	it, err := r.ReadInterleaved(45000)
	if err != nil {
		t.Fatal(err)
	}
	var seq []TrackID
	for it.Next() {
		seq = append(seq, it.Track())
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}
	hasVideo, hasMeta := false, false
	for _, id := range seq {
		if id == video {
			hasVideo = true
		}
		if id == meta {
			hasMeta = true
		}
	}
	if !hasVideo || !hasMeta {
		t.Fatalf("seq = %v", seq)
	}

	// track filter: only meta frames
	it2, err := r.ReadInterleaved(0, meta)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for it2.Next() {
		if it2.Track() != meta {
			t.Fatalf("unexpected track %d", it2.Track())
		}
		n++
	}
	if n != 5 { // i = 0,7,14,21,28
		t.Fatalf("meta frames = %d, want 5", n)
	}

	if _, err := r.ReadInterleaved(0, TrackID(99)); err == nil {
		t.Fatal("unknown track must error")
	}
	w.Close()
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestReadInterleaved -v`
Expected: `FAIL ... [build failed]`

- [ ] **Step 3: 구현** — `gmc/reader.go`에 추가:

```go
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
```

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestReadInterleaved -v`
Expected: `PASS`

- [ ] **Step 5: 커밋**

```powershell
git add gmc/reader.go gmc/interleaved_test.go
git commit -m "구현: ReadInterleaved 멀티트랙 통합 읽기 (트랙별 sync point 최소 오프셋 규칙)"
```

---

### 태스크 15: Follow — 라이브 테일

**Files:**
- Modify: `gmc/iterator.go` (Follow 구현), `gmc/reader.go` 불필요
- Test: `gmc/follow_test.go`

**Interfaces:**
- Consumes: Writer의 `cond`/`closed`/`mu` (태스크 7), `readChunkAt`, `decodeDataHeader`
- Produces (공개): `type TrackFrame struct { Track TrackID; Frame Frame }`, `(r *Reader) Follow(ctx context.Context, tracks ...TrackID) <-chan TrackFrame` — 호출 시점의 committed부터 시작해 커밋되는 프레임을 순서대로 전달. writer가 Finalize/Close 하면 잔여 소진 후 채널 close, ctx 취소로도 종료. Open()된(라이브 아닌) 리더에서는 남은 데이터 소진 후 즉시 close

- [ ] **Step 1: 실패하는 테스트 작성** — `gmc/follow_test.go`

```go
package gmc

import (
	"context"
	"testing"
	"time"
)

func TestFollowReceivesFramesAndClosesOnFinalize(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ch := r.Follow(context.Background(), video)
	done := make(chan []uint64)
	go func() {
		var got []uint64
		for tf := range ch {
			got = append(got, tf.Frame.PTS)
		}
		done <- got
	}()

	for i := 0; i < 10; i++ {
		if err := w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if len(got) != 10 || got[0] != 0 || got[9] != 27000 {
			t.Fatalf("got = %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower did not finish after Finalize")
	}
}

func TestFollowCancel(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := r.Follow(ctx, video)
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed after cancel
			}
		case <-deadline:
			t.Fatal("follow channel did not close after cancel")
		}
	}
}

func TestFollowOnOpenedFileDrainsAndCloses(t *testing.T) {
	path, video, _ := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for range r.Follow(context.Background(), video) {
		n++
	}
	if n != 30 {
		t.Fatalf("frames = %d, want 30", n)
	}
}
```

- [ ] **Step 2: 실패 확인**

Run: `go test ./gmc/ -run TestFollow -v`
Expected: `FAIL ... [build failed]` (undefined: Follow)

- [ ] **Step 3: 구현** — `gmc/iterator.go`에 추가 (import에 `"context"` 추가):

```go
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
```

주의: Follow의 시작점 — 라이브 리더는 "호출 시점의 committed"(진짜 테일), Open()된 리더는 스트림 처음부터 소진.

- [ ] **Step 4: 통과 확인**

Run: `go test ./gmc/ -run TestFollow -v` 그리고 `go test -race ./gmc/ -run TestFollow -v`
Expected: `PASS` (race 포함)

- [ ] **Step 5: 커밋**

```powershell
git add gmc/iterator.go gmc/follow_test.go
git commit -m "구현: Follow 라이브 테일 (sync.Cond 대기, Finalize/Close 시 잔여 소진 후 EOF, ctx 취소)"
```

---

### 태스크 16: 동시성 스트레스 테스트 (-race)

**Files:**
- Test: `gmc/concurrency_test.go`

**Interfaces:**
- Consumes: 전체 공개 API

- [ ] **Step 1: 스트레스 테스트 작성** — `gmc/concurrency_test.go`

```go
package gmc

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// writer 1 + seeker N + follower M이 동시에 동작해도 데이터 경합과 손상이
// 없어야 한다. 반드시 -race로 실행한다.
func TestConcurrentWriteSeekFollow(t *testing.T) {
	const frames = 2000
	path := filepath.Join(t.TempDir(), "stress.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: 4096, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})

	var wg sync.WaitGroup
	followTotal := make([]int, 2)

	// followers (start before writing so they see everything)
	for m := 0; m < 2; m++ {
		r, err := w.NewReader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		ch := r.Follow(context.Background())
		wg.Add(1)
		go func(m int) {
			defer wg.Done()
			for range ch {
				followTotal[m]++
			}
		}(m)
	}

	// seekers
	stop := make(chan struct{})
	for s := 0; s < 4; s++ {
		r, err := w.NewReader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				it, err := r.SeekPTS(video, 3000*uint64(frames/2))
				if err != nil {
					t.Error(err)
					return
				}
				for i := 0; i < 5 && it.Next(); i++ {
					_ = it.Frame()
				}
				if it.Err() != nil {
					t.Error(it.Err())
					return
				}
			}
		}()
	}

	// writer
	for i := 0; i < frames; i++ {
		if err := w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%30 == 0, Data: make([]byte, 64)}); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteFrame(audio, Frame{PTS: uint64(i * 1600), Keyframe: true, Data: make([]byte, 32)}); err != nil {
			t.Fatal(err)
		}
		if i%100 == 0 {
			if err := w.SetTag("progress", []byte{byte(i / 100)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	for m, n := range followTotal {
		if n != frames*2 {
			t.Fatalf("follower %d received %d frames, want %d", m, n, frames*2)
		}
	}

	// the finalized file reopens identically
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for range r.Follow(context.Background()) {
		n++
	}
	if n != frames*2 {
		t.Fatalf("reopened frames = %d, want %d", n, frames*2)
	}
}
```

- [ ] **Step 2: race 포함 실행**

Run: `go test -race ./gmc/ -run TestConcurrentWriteSeekFollow -v -timeout 120s`
Expected: `PASS`, `WARNING: DATA RACE` 없음

- [ ] **Step 3: 전체 회귀 (race 포함)**

Run: `go test -race ./gmc/ -v -timeout 300s`
Expected: 전체 `PASS`

- [ ] **Step 4: go vet**

Run: `go vet ./gmc/`
Expected: 출력 없음

- [ ] **Step 5: 커밋**

```powershell
git add gmc/concurrency_test.go
git commit -m "테스트: writer+seeker+follower 동시성 스트레스 (-race) 및 재오픈 일치 검증"
```

---

## 태스크 완료 후

모든 태스크 통과 시 superpowers:requesting-code-review 스킬로 브랜치 전체 최종 리뷰 1회 (CLAUDE.md 규칙: Opus 모델 서브 에이전트로 위임).

## 스펙 커버리지 매핑

| 설계 문서 | 태스크 |
|---|---|
| §3.2 File Header | 2, 7 |
| §3.3 Tags 영역 (더블 슬롯·torn read·초기 상태) | 3, 8, 11 |
| §3.4 청크 프레이밍 (CRC·상한·미지 타입) | 1, 12 |
| §3.5 TrackInfo/Data/Checkpoint/Footer/트레일러 | 4, 5, 9, 10 |
| §4 동시성 모델·§4.1 불변식 | 7, 13, 15, 16 |
| §5.1 라이브 시크/테일 | 13, 15 |
| §5.2 완성 파일 오픈 | 11 |
| §5.3 스캔 복구 (CRC 전수·꼬리 보충) | 12 |
| §6 내구성 (torn write 절단) | 12 |
| §7 API (pts 강제, Close/Finalize, ReadInterleaved, Follow EOF) | 7, 10, 13, 14, 15 |
