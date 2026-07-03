# GMC (Go Media Container) 포맷 설계

- 작성일: 2026-07-03
- 상태: 초안 (검토 대기)
- 작업명(working name): `gmc` — 확정 전 변경 가능

## 1. 배경과 목표

기존 컨테이너(MKV/EBML, MP4 등)는 구조가 복잡하거나, 인덱스가 파일 마지막에 기록되어
**쓰기가 끝나기 전에는 읽기 접근에 제약**이 크다. 본 프로젝트는 Go 기반으로,
쓰기 도중에도 읽을 수 있는 단순한 멀티미디어 저장 컨테이너를 만든다.

### 확정된 요구사항

| 항목 | 결정 |
|---|---|
| 멀티트랙 | 영상/오디오/메타데이터 등 N개 트랙을 하나의 파일에 인터리브 저장 |
| 트랙 구성 자유 | **필수 트랙 없음.** 영상 없이 오디오+메타데이터만, 오디오만, 메타데이터 트랙 하나만 있는 파일도 모두 유효 |
| Private 데이터 | 파일 레벨 + 트랙 레벨 사용자 정의 바이너리 데이터 저장 |
| 세션 메타정보 저장소 | 녹화 시작 절대시간, 녹화 위치 등 부가정보를 key-value로 저장. 녹화 도중 추가/갱신 가능 |
| 동시접근 시나리오 | ① 라이브 테일 추적(쓰는 중 최신 데이터 실시간 읽기) ② 쓰는 중 과거 구간 타임스탬프 시크 |
| 동시성 범위 | **같은 Go 프로세스 내 고루틴** (writer 1 + reader N) |
| 내구성 | 크래시/전원 손실 시 **마지막 유효 청크까지 복구** — 별도 복구 도구 없이 리더가 그대로 열 수 있어야 함 |
| 호환성 | 순수 커스텀 포맷 + Go SDK. (MP4/MKV export는 향후 별도 도구) |

### 비목표 (Non-goals)

- 프로세스 간 / 네트워크 파일시스템 동시접근 보장 (포맷은 방해하지 않으나 SDK가 보장하지 않음)
- ffmpeg 등 기존 도구의 직접 재생
- 기록된 데이터의 수정/삭제 (append-only)
- 코덱 처리 — 컨테이너는 압축된 프레임을 바이트로만 취급

## 2. 설계 원칙

1. **Append-only 청크 스트림** — 파일의 어떤 시점 스냅샷도 그 자체로 유효한 파일이다.
   "완성" 단계(Finalize)는 성능 최적화(통합 인덱스)일 뿐, 필수가 아니다.
2. **자기서술(self-delimiting) 청크** — 각 청크는 길이·타입·CRC를 가져 앞에서부터
   순차 스캔만으로 전체 구조를 복원할 수 있다.
3. **인덱스는 데이터를 따라간다** — MKV처럼 마지막에 몰아 쓰지 않고, 주기적
   체크포인트 청크로 스트림 중간에 박아 넣는다. 뒤로 연결 리스트(backward chain)를
   이뤄 미완성 파일에서도 시크가 가능하다.
4. **단순함 우선** — EBML 같은 범용 가변 구조 대신 고정 레이아웃 + 소수의 청크 타입.

## 3. 온디스크 포맷

바이트 오더는 리틀 엔디언. CRC는 CRC-32C (Castagnoli, `hash/crc32`).

### 3.1 전체 레이아웃

```
+--------------------+
| File Header (고정)  |  magic, version, 파일 레벨 private data
+--------------------+
| Chunk #0           |  TrackInfo, Data, IndexCheckpoint, ... 가 순서대로 이어짐
| Chunk #1           |
| ...                |
+--------------------+
| Footer (선택)       |  Finalize 시에만 존재: 통합 인덱스 + 트레일러
+--------------------+
```

### 3.2 File Header

```
offset  size  field
0       4     magic          "GMC1"
4       2     version        u16 (=1)
6       2     flags          u16 (예약, 0)
8       4     privateLen     u32
12      4     headerCRC      CRC-32C(magic..privateLen + private)
16      n     private        파일 레벨 사용자 정의 데이터 (privateLen 바이트)
```

헤더는 파일 생성 시 한 번 쓰고 이후 불변. private data는 생성 시점에 확정한다.
(생성 후 변경 가능한 메타데이터가 필요하면 메타데이터 트랙을 사용한다 — §3.4)

### 3.3 청크 공통 프레이밍

```
offset  size  field
0       4     payloadLen     u32 (payload만의 길이)
4       1     type           u8
5       n     payload
5+n     4     crc            CRC-32C(type + payload)
```

- 리더는 `payloadLen` 으로 다음 청크 위치를 계산해 O(청크 수)로 파일을 훑을 수 있다
  (payload는 건너뛰므로 실제 읽기량은 헤더 몇 바이트씩).
- CRC 불일치 = 거기서부터 찢어진(torn) 쓰기 → **그 직전이 논리적 EOF**.
  append-only + 로컬 FS 특성상 손상은 꼬리에서만 발생한다고 가정한다(§6).

### 3.4 청크 타입

| type | 이름 | 용도 |
|---|---|---|
| 0x01 | TrackInfo | 트랙 등록. 데이터가 나오기 전 어느 시점이든 추가 가능 |
| 0x02 | Data | 프레임/샘플 1개 |
| 0x03 | IndexCheckpoint | 주기적 인덱스 체크포인트 |
| 0x04 | Footer | Finalize 시 통합 인덱스 (파일 마지막) |
| 0x05 | Tags | 파일/세션 레벨 key-value 메타정보 (시작 절대시간, 녹화 위치 등) |

**TrackInfo payload**

```
trackID      u16   (파일 내 유일, 등록 순 증가)
kind         u8    (0=video, 1=audio, 2=data/metadata)
timebaseNum  u32   \  타임스탬프 단위 = num/den 초
timebaseDen  u32   /  (예: 1/90000)
codecLen     u16 + bytes   코덱 식별 문자열 ("h264", "opus", "json" 등 자유)
privateLen   u32 + bytes   트랙 레벨 private data (SPS/PPS, 스키마 등)
```

- `kind`는 분류 힌트일 뿐, 컨테이너 동작은 kind에 의존하지 않는다.
  트랙 조합에 제약이 없다 — 오디오+메타데이터만, 메타데이터 단일 트랙 파일도 유효.

**Data payload**

```
trackID      u16
flags        u8    (bit0: keyframe)
pts          u64   (트랙 timebase 단위)
data         나머지 전부 (payloadLen - 11 바이트)
```

- 저장 순서 = 디코드 순서로 정의한다. B-frame 재정렬(pts≠dts)이 필요해지면
  flags 비트 + 선택적 dts 필드로 v2에서 확장한다 (현 단계 비목표).
- 메타데이터 트랙도 동일한 Data 청크를 쓴다 — pts를 가진 JSON/바이너리 이벤트 스트림.

**IndexCheckpoint payload**

```
prevCheckpointOffset  u64   (직전 체크포인트의 파일 오프셋, 첫 번째면 0)
entryCount            u32
entries[]:
  trackID   u16
  pts       u64
  offset    u64   (해당 Data 청크의 파일 오프셋)
```

- 직전 체크포인트 이후 각 트랙의 **sync point**(keyframe 플래그가 선 Data 청크) 위치를 기록한다.
  - 영상: 코덱 키프레임에만 플래그를 세움 (GOP당 1개 수준 — 전부 기록)
  - 오디오: 모든 프레임이 독립 디코딩 가능하므로 전부 플래그가 서는 게 보통 —
    체크포인트 비대를 막기 위해 writer가 **트랙당 구간 내 대표 엔트리만 샘플링** 기록
    (기본: 트랙당 체크포인트 구간별 첫 sync point 1개)
  - 메타데이터: 저빈도 가정, 전부 기록 (고빈도면 오디오와 같은 샘플링 적용)
- **인덱스 엔트리는 힌트다.** 시크는 목표 pts 이하의 가장 가까운 엔트리에서 시작해
  앞으로 스캔하며 정확한 위치를 찾는다. 따라서 샘플링 정책은 시크 정밀도가 아니라
  시크 후 스캔 거리(≈체크포인트 주기)에만 영향을 주며, 포맷 규격을 바꾸지 않는다.
- 기록 주기: 기본 "T초 또는 N MiB 중 먼저 도달" (설정 가능, 기본 1초/8MiB 수준).
  영상 유무와 무관하게 동작한다 — 오디오/메타데이터 전용 파일에서도 동일.
- backward chain 덕분에 미완성 파일에서도 "마지막 체크포인트부터 거꾸로" 인덱스를
  복원할 수 있으나, 실제로는 열 때 전체를 앞으로 훑는 편이 단순하다(§5.3).

**Tags payload**

```
entryCount   u16
entries[]:
  keyLen     u16 + key (UTF-8 문자열)
  valueLen   u32 + value (bytes)
```

- 스트림 어느 위치에나 기록 가능. 같은 key가 다시 나오면 **나중 값이 우선(latest wins)**
  — append-only를 유지하면서 녹화 도중 메타정보 추가/갱신이 가능하다.
- Well-known key는 `gmc.` 접두사로 예약한다:

| key | value | 의미 |
|---|---|---|
| `gmc.start_time_unix_ns` | u64 (LE) | pts 0에 대응하는 절대시각 (Unix nanoseconds, UTC) |
| `gmc.location` | UTF-8 자유 형식 | 녹화 위치 (좌표, 장소명 등) |

  그 외 key는 애플리케이션 자유. (`gmc.` 접두사만 예약)
- **Tags vs private data vs 메타데이터 트랙 사용 구분**:
  - 헤더/트랙 private data — 생성 시 확정되는 불투명 blob (코덱 설정 등)
  - Tags — 타임라인과 무관한 세션 속성. 도중 갱신 가능, 조회는 병합된 최신 스냅샷
  - 메타데이터 트랙 — pts를 가진 시변(time-varying) 데이터 (GPS 궤적, 이벤트 로그 등)

**Footer payload** (Finalize 시에만)

```
전 트랙 통합 인덱스 (IndexCheckpoint entries와 동일 형식의 전체 목록)
최종 병합된 Tags 스냅샷
duration 등 요약 정보
```

Footer 청크 뒤에 고정 16바이트 트레일러를 붙인다:

```
footerOffset  u64
trailerCRC    u32
endMagic      "GMCE" (4)
```

리더는 파일 끝 16바이트를 먼저 확인해 트레일러가 유효하면 **한 번의 시크로 전체
인덱스를 로드**하고, 없으면(미완성/크래시 파일) 순차 스캔으로 폴백한다.
**Footer는 편의 캐시일 뿐이며 Footer 없이도 모든 정보가 스트림 안에 있다.**

## 4. 동시성 모델 (프로세스 내)

```
                    ┌────────────────────────────────┐
 WriteFrame ──────► │ Writer (mutex 직렬화)            │
                    │  - 청크 직렬화 후 파일 append     │
                    │  - in-memory index 갱신          │
                    │  - committedSize (atomic) 전진   │
                    │  - cond broadcast (tail 깨우기)  │
                    └────────────────────────────────┘
                         │ 공유 (같은 프로세스)
                         ▼
                    ┌────────────────────────────────┐
 Reader 1..N ─────► │ - ReadAt(fd, off) — 동시 안전    │
                    │  - committedSize 이전만 읽음      │
                    │  - index는 RWMutex로 공유 조회    │
                    │  - tail follow: cond 대기        │
                    └────────────────────────────────┘
```

- **Writer는 1개** (내부 mutex로 직렬화 — 여러 고루틴이 WriteFrame을 불러도 안전).
- **committedSize**: 청크 하나가 파일에 완전히 쓰인 뒤에만 원자적으로 전진.
  리더는 이 값 이전 영역만 읽으므로 찢어진 청크를 볼 일이 없다.
- **읽기**: `os.File.ReadAt`은 고루틴 동시 호출에 안전하므로 fd 하나를 공유한다.
  같은 프로세스의 페이지 캐시를 통하므로 fsync 없이도 쓰기 즉시 읽힌다.
- **인덱스**: writer가 유지하는 in-memory 키프레임 인덱스(트랙별 pts→offset 정렬 슬라이스)를
  RWMutex로 공유. 시크는 파일 스캔 없이 메모리에서 해결된다.
  온디스크 체크포인트는 **크래시 후/별도 오픈** 경로 전용.
- **라이브 테일**: reader가 `sync.Cond`(또는 채널)로 새 청크 커밋을 대기 — 폴링 없음.
- **디스크 반영(fsync)**: 기본은 OS에 위임(페이지 캐시), 옵션으로 주기적 fsync 제공.
  §6의 복구 보장은 "fsync된 지점까지"가 아니라 "디스크에 실제 도달한 지점까지"이다.

## 5. 읽기 경로 3가지

### 5.1 쓰는 중 — 같은 프로세스 (주 시나리오)

`writer.NewReader()` 로 획득. in-memory 인덱스 + committedSize 공유.
시크·테일 모두 파일 스캔 없이 즉시.

### 5.2 완성된 파일

`gmc.Open(path)` → 트레일러 확인 → Footer 인덱스 한 번에 로드.

### 5.3 미완성/크래시 파일

`gmc.Open(path)` → 트레일러 없음 → 앞에서부터 청크 헤더만 순차 스캔하며
인덱스 재구성, CRC 실패 지점 직전을 논리적 EOF로 확정. 복구 도구 불필요.
(대용량 파일 최적화가 필요해지면 backward chain 활용은 그때 추가한다.)

## 6. 내구성과 복구

- append-only이므로 손상은 "마지막 쓰기가 중간에 끊긴" 꼬리 부분에 국한된다.
- 복구 = 순차 스캔 중 첫 CRC 불일치(또는 파일 끝 초과 길이) 지점 직전까지 인정.
- 스캔 중 발견한 완전한 청크는 전부 유효 — 체크포인트가 없어도 Data 청크 자체로
  인덱스를 재구성할 수 있다 (체크포인트는 스캔을 빠르게 할 뿐).
- 크래시 파일을 이어쓰기(append 재개)하는 기능은 v1 비목표 — 열어서 읽고,
  새 파일로 이어 녹화하는 것을 기본 패턴으로 한다.

## 7. Go SDK API 스케치

```go
// 쓰기
w, err := gmc.Create("rec.gmc", gmc.CreateOptions{
    Private: manifestBytes,              // 파일 레벨 private data
})
video, err := w.AddTrack(gmc.TrackInfo{
    Kind: gmc.KindVideo, Codec: "h264",
    TimebaseNum: 1, TimebaseDen: 90000,
    Private: avcConfig,                  // 트랙 레벨 private data
})
err = w.WriteFrame(video, gmc.Frame{PTS: pts, Keyframe: true, Data: nal})

// 세션 메타정보 (녹화 도중 언제든 추가/갱신 가능)
err = w.SetStartTime(time.Now())         // gmc.start_time_unix_ns 편의 API
err = w.SetTag("gmc.location", []byte("37.5665,126.9780"))
err = w.SetTag("camera.id", []byte("cam-03"))

err = w.Finalize()                       // Footer 기록 후 close

// 쓰는 중 읽기 (같은 프로세스)
r := w.NewReader()
it, err := r.SeekPTS(video, targetPTS)   // 과거 구간 랜덤 시크
for it.Next() { frame := it.Frame(); ... }
tail, err := r.Follow(ctx, video)        // 라이브 테일 (커밋될 때마다 수신)

// 완성/크래시 파일 열기
r, err := gmc.Open("rec.gmc")            // Footer 있으면 즉시, 없으면 스캔 복구
info := r.FilePrivate()                  // 헤더 private data
tags := r.Tags()                         // 병합된 최신 key-value 스냅샷
start, ok := r.StartTime()               // gmc.start_time_unix_ns 편의 API
```

## 8. 대안 검토와 트레이드오프

| 대안 | 기각 사유 |
|---|---|
| MKV/EBML | 요구사항의 출발점 — 인덱스(Cues)가 마지막에 기록되고 EBML 가변 구조가 복잡. 미완성 파일 시크 불가 |
| fMP4 (fragmented MP4) | 스트리밍 가능하지만 box 구조·moof/mdat 규약이 복잡하고 트랙/타임스탬프 규칙에 제약. 호환성이 비목표이므로 이득이 작음 |
| MPEG-TS | 완전한 스트리밍성은 있으나 188바이트 패킷화 오버헤드, 인덱스 부재(시크는 비트레이트 추정), PSI 테이블 복잡성 |
| SQLite에 프레임 저장 | 동시성·인덱스는 공짜지만 대용량 연속 미디어 쓰기 성능·파일 크기 오버헤드, "단순한 포맷" 목표와 충돌 |

본 설계의 트레이드오프:

- **체크포인트 오버헤드**: 키프레임당 18바이트 수준의 인덱스가 주기적으로 중복 기록됨 — 미디어 데이터 대비 무시 가능.
- **저장 순서 = 디코드 순서 고정**: B-frame pts/dts 분리가 필요한 코덱 설정은 v1에서 미지원 (확장 여지는 flags에 확보).
- **헤더 private data 불변**: 헤더를 가변으로 만들면 append-only 불변식이 깨지므로 의도된 제약. 생성 후 변경이 필요한 메타정보는 Tags(세션 속성) 또는 메타데이터 트랙(시변 데이터)을 사용한다.
- **Tags 갱신 = 재기록**: latest-wins 방식이라 갱신할 때마다 청크가 추가됨. 세션 속성은 저빈도 갱신을 가정 — 고빈도로 변하는 값은 메타데이터 트랙이 올바른 자리.

## 9. 다음 단계

1. 본 설계 검토·확정
2. 상세 개발 계획 작성 (`docs/feature/2026-07-03-01-gmc-container-plan.md`)
   - 패키지 구조, 태스크 분해, 테스트 전략 (동시성 테스트: `-race`, 크래시 주입 테스트 포함)
3. 구현 (TDD): 포맷 인코딩/디코딩 → Writer → Reader(시크/테일) → 복구 → 동시성
