# GMC (Go Media Container) 포맷 설계

- 작성일: 2026-07-03
- 상태: 구현 완료 (v1, 2026-07-03 병합 93b190e) — 이후 v1.1 확장은 [2026-07-04-01](2026-07-04-01-gmc-core-v11-design.md) 참조
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
| 세션 메타정보 저장소 | 녹화 시작 절대시간, 녹화 위치 등 부가정보를 key-value로 저장. 녹화 도중 추가/갱신 가능. 파일 앞쪽 고정 영역이라 스캔 없이 즉시 조회 |
| 동시접근 시나리오 | ① 라이브 테일 추적(쓰는 중 최신 데이터 실시간 읽기) ② 쓰는 중 과거 구간 타임스탬프 시크 |
| 동시성 범위 | **같은 Go 프로세스 내 고루틴** (writer 1 + reader N) |
| 내구성 | 크래시/전원 손실 시 **마지막 유효 청크까지 복구** — 별도 복구 도구 없이 리더가 그대로 열 수 있어야 함 |
| 호환성 | 순수 커스텀 포맷 + Go SDK. (MP4/MKV export는 향후 별도 도구) |

### 비목표 (Non-goals)

- 프로세스 간 / 네트워크 파일시스템 동시접근 보장 (포맷은 방해하지 않으나 SDK가 보장하지 않음)
- 동일 파일 다중 writer 방지 — 프로세스 내에서는 SDK가 같은 경로의 중복 Create를
  막지만, 프로세스 간 잠금은 제공하지 않는다 (운영 책임)
- ffmpeg 등 기존 도구의 직접 재생
- 기록된 데이터의 수정/삭제 (append-only)
- 코덱 처리 — 컨테이너는 압축된 프레임을 바이트로만 취급

## 2. 설계 원칙

1. **Append-only 청크 스트림** — 파일의 어떤 시점 스냅샷도 그 자체로 유효한 파일이다.
   "완성" 단계(Finalize)는 성능 최적화(통합 인덱스)일 뿐, 필수가 아니다.
   (유일한 예외: 앞쪽 Tags 영역의 제자리 갱신 — 더블 슬롯으로 보호, §3.3)
2. **자기서술(self-delimiting) 청크** — 각 청크는 길이·타입·CRC를 가져 앞에서부터
   순차 스캔만으로 전체 구조를 복원할 수 있다.
3. **인덱스는 데이터를 따라간다** — MKV처럼 마지막에 몰아 쓰지 않고, 주기적
   체크포인트 청크로 스트림 중간에 박아 넣는다. 덕분에 미완성 파일에서도 시크가
   가능하다. (체크포인트끼리는 backward chain으로도 연결해 두어 향후 역방향
   스캔 최적화 여지를 남긴다 — v1 리더는 전방 스캔만 사용, §5.3)
4. **단순함 우선** — EBML 같은 범용 가변 구조 대신 고정 레이아웃 + 소수의 청크 타입.

## 3. 온디스크 포맷

바이트 오더는 리틀 엔디언. CRC는 CRC-32C (Castagnoli, `hash/crc32`).

### 3.1 전체 레이아웃

```
+--------------------+
| File Header (고정)  |  magic, version, 파일 레벨 private data
+--------------------+
| Tags 영역 (고정)     |  세션 key-value 메타정보. 예약 공간 + 더블 슬롯, 제자리 갱신
+--------------------+
| Chunk #0           |  TrackInfo, Data, IndexCheckpoint, ... 가 순서대로 이어짐
| Chunk #1           |
| ...                |
+--------------------+
| Footer (선택)       |  Finalize 시에만 존재: 통합 인덱스 + 트레일러
+--------------------+
```

파일은 **Tags 영역을 제외하면 append-only**다. Tags 영역만 유일하게 제자리
갱신되며, 더블 슬롯으로 찢어진 쓰기(torn write)에서 보호된다(§3.3).

### 3.2 File Header

```
offset  size  field
0       4     magic          "GMC1"
4       2     version        u16 (=1)
6       2     flags          u16 (예약, 0)
8       4     tagsAreaLen    u32 (뒤따르는 Tags 영역 전체 크기)
12      4     privateLen     u32
16      4     headerCRC      CRC-32C(magic..privateLen + private)
20      n     private        파일 레벨 사용자 정의 데이터 (privateLen 바이트)
```

헤더는 파일 생성 시 한 번 쓰고 이후 불변. private data는 생성 시점에 확정한다.
(생성 후 변경 가능한 메타데이터는 Tags 영역 사용 — §3.3)

### 3.3 Tags 영역 — 세션 key-value 메타정보

녹화 시작 절대시간, 녹화 위치 같은 세션 부가정보를 담는 **고정 크기 예약 영역**.
헤더 직후에 위치하므로 파일 앞부분만 읽으면 — 녹화 중이든 크래시 파일이든 —
스캔 없이 즉시 조회할 수 있다.

```
+----------------+----------------+
|    Slot A      |    Slot B      |   각 슬롯 크기 = tagsAreaLen / 2
+----------------+----------------+

슬롯 내부:
seq          u64   갱신 시퀀스 번호 (단조 증가)
entryCount   u16
entries[]:
  keyLen     u16 + key (UTF-8 문자열)
  valueLen   u32 + value (bytes)
crc          u32   CRC-32C(seq..entries)
```

- **더블 슬롯(ping-pong) 갱신**: 항상 현재 비활성 슬롯에 전체 스냅샷을 다시 쓴다.
  리더는 CRC가 유효한 슬롯 중 seq가 큰 쪽을 채택한다. 갱신 도중 크래시가 나면
  쓰던 슬롯의 CRC가 깨지므로 자동으로 직전 값으로 폴백 — 찢어진 쓰기에 안전하다.
- **용량은 생성 시 고정** (기본 8 KiB = 4 KiB 슬롯 × 2, `CreateOptions`로 조정).
  직렬화 크기가 슬롯을 넘으면 `ErrTagsTooLarge` — 세션 속성은 소량을 가정한다.
- **초기 상태**: 생성 시 영역 전체를 zero-fill. 유효한 슬롯이 하나도 없으면
  "태그 없음"으로 해석한다(에러 아님). seq는 1부터 시작.
- Well-known key는 `gmc.` 접두사로 예약한다:

| key | value | 의미 |
|---|---|---|
| `gmc.start_time_unix_ns` | u64 (LE) | pts 0에 대응하는 절대시각 (Unix nanoseconds, UTC) |
| `gmc.location` | UTF-8 자유 형식 | 녹화 위치 (좌표, 장소명 등) |

  그 외 key는 애플리케이션 자유. (`gmc.` 접두사만 예약)
- **Tags vs private data vs 메타데이터 트랙 사용 구분**:
  - 헤더/트랙 private data — 생성 시 확정되는 불투명 blob (코덱 설정 등)
  - Tags — 타임라인과 무관한 세션 속성. 도중 갱신 가능, 조회는 최신 스냅샷
  - 메타데이터 트랙 — pts를 가진 시변(time-varying) 데이터 (GPS 궤적, 이벤트 로그 등)

### 3.4 청크 공통 프레이밍

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
- **payloadLen 상한 = 256 MiB.** 상한 초과 또는 파일 범위를 벗어나는 길이는
  손상으로 간주하고 CRC 불일치와 동일하게 처리한다 (스캔 폭주 방어).
- **미지 타입은 건너뛴다 (forward-compat)**: CRC가 유효하면 모르는 type의 청크는
  skip한다. 향후 버전이 새 청크 타입을 추가해도 구버전 리더가 파일을 읽을 수 있다.
  (기존 청크의 의미 변경은 헤더 version으로만 한다)

### 3.5 청크 타입

| type | 이름 | 용도 |
|---|---|---|
| 0x01 | TrackInfo | 트랙 등록. 해당 트랙의 첫 Data 청크 이전이면 스트림 어디서든 추가 가능 |
| 0x02 | Data | 프레임/샘플 1개 |
| 0x03 | IndexCheckpoint | 주기적 인덱스 체크포인트 |
| 0x04 | Footer | Finalize 시 통합 인덱스 (파일 마지막) |

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
- **공통 시간 원점**: 모든 트랙은 timebase가 달라도 **pts 0이 같은 시점(세션 원점)**을
  가리킨다. 프레임의 절대시각 = `gmc.start_time_unix_ns` + pts × num/den (§3.3).
  이 규약이 트랙 간 동기화와 절대시간 시크의 근거다.
- 컨테이너는 코덱 불가지(codec-agnostic)다 — 프레임 해석에 필요한 정보
  (예: PCM의 sampleRate/channels, FLAC의 STREAMINFO, H.264의 SPS/PPS)는
  애플리케이션이 codec 문자열과 트랙 private data로 약속한다.

**Data payload**

```
trackID      u16
flags        u8    (bit0: keyframe)
pts          u64   (트랙 timebase 단위)
data         나머지 전부 (payloadLen - 11 바이트)
```

- **트랙 내 pts는 단조 비감소** (같은 값 허용 — 동일 시각 다중 이벤트).
  `WriteFrame`은 위반 시 `ErrNonMonotonicPTS`를 반환한다 — 타임스탬프 정리는
  호출자 책임. 인덱스와 시크의 정확성은 이 불변식 위에 성립한다.
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

**Footer payload** (Finalize 시에만)

```
전 트랙 TrackInfo 사본 (Footer만 읽고도 트랙 구성·코덱·private data를 알 수 있게)
전 트랙 통합 인덱스 (IndexCheckpoint entries와 동일 형식의 전체 목록)
트랙별 요약: firstPTS, lastPTS, 프레임 수   (duration은 파생값)
```

(Tags는 Footer에 넣지 않는다 — 앞쪽 Tags 영역(§3.3)이 항상 최신 값의 유일한 위치)

Footer 청크 뒤에 고정 16바이트 트레일러를 붙인다:

```
footerOffset  u64
trailerCRC    u32
endMagic      "GMCE" (4)
```

리더는 파일 끝 16바이트를 먼저 확인해 트레일러가 유효하면 **한 번의 시크로 전체
인덱스를 로드**하고, 없으면(미완성/크래시 파일) 순차 스캔으로 폴백한다.
트레일러 채택 조건: endMagic·trailerCRC 일치 + footerOffset이 파일 범위 내 +
그 위치의 Footer 청크 CRC까지 유효. 하나라도 어긋나면 스캔 폴백.
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
  **종료 의미론**: writer가 Finalize/Close 하면 팔로워는 남은 커밋분을 소진한 뒤
  EOF로 종료를 통지받는다. reader 쪽 ctx 취소로도 종료된다.
- **디스크 반영(fsync)**: 기본은 OS에 위임(페이지 캐시), `Sync()` 수동 호출 제공
  (주기적 fsync가 필요하면 호출자가 타이머로 Sync 호출). §6의 복구 보장은
  "fsync된 지점까지"가 아니라 "디스크에 실제 도달한 지점까지"이다.

### 4.1 Shared Read 안전 불변식

쓰기 도중 읽기가 안전하기 위해 구현이 반드시 지켜야 하는 규칙:

1. **committedSize는 write가 OS에 전달된 뒤에만 전진한다.**
   버퍼링(bufio 등)을 쓰는 경우 flush 완료 후에만 전진. 리더는 committedSize
   이전 영역만 읽으므로 쓰다 만(torn) 청크를 절대 보지 않는다.
   같은 프로세스·같은 페이지 캐시이므로 write() 반환 즉시 ReadAt에 보이며,
   Go 메모리 모델상 atomic Store→Load가 happens-before를 보장한다.
2. **읽기는 `os.File.ReadAt`만 사용한다.** pread 계열이라 파일 오프셋을 공유하지
   않아 고루틴 동시 호출 및 writer의 append와 간섭이 없다. 리더는 자체 read-only
   fd를 연다 — 같은 페이지 캐시를 보므로 가시성은 동일하고, writer가 닫힌 뒤에도
   리더 수명이 독립적이다.
3. **Tags 영역의 동시 읽기는 더블 슬롯이 해결한다.** 갱신 중인 슬롯을 리더가
   읽으면 CRC 불일치 → 반대쪽(직전 유효) 슬롯 채택. 항상 비활성 슬롯에만 쓰므로
   반대쪽은 언제나 완전하다. 최악의 경우 한 세대 이전 스냅샷을 볼 뿐,
   깨진 값은 절대 보지 않는다. (크래시 보호와 동일한 메커니즘)
   같은 프로세스 리더는 in-memory 태그 맵(RWMutex)을 읽으므로 디스크 경로는
   외부 오픈/복구 전용이다.
4. **공유 인덱스는 RWMutex, 테일 대기는 sync.Cond** — writer만 쓰고 리더는 읽기만.

참고: 1·3의 메커니즘(CRC 꼬리 절단, 더블 슬롯)은 디스크 상태만으로 성립하므로,
보장 범위 밖이지만 **다른 프로세스가 녹화 중 파일을 열어도 스냅샷 수준으로는
안전하게 읽힌다** (라이브 테일 알림·인메모리 인덱스 공유만 프로세스 내 전용).
검증: 모든 동시성 테스트는 `-race`로 실행하고, writer/reader 동시 스트레스 테스트와
torn write 주입(무작위 지점 절단) 테스트를 필수로 포함한다.

## 5. 읽기 경로 3가지

어떤 경로로 얻든 인덱스의 실체는 **트랙별 pts 오름차순 (pts, offset) 배열**이고,
시크는 2단계로 동작한다:

```
① 이진 탐색 — 목표 pts 이하의 마지막 sync point 엔트리를 찾는다
② 전방 스캔 — 그 오프셋부터 청크를 순회하며 정확한 프레임까지 이동
   (다른 트랙 청크는 헤더만 읽고 payloadLen으로 skip.
    스캔 거리 ≈ 영상은 GOP 1개, 오디오/메타데이터는 체크포인트 주기)
```

영상 시크가 키프레임에서 시작하는 것은 디코딩상으로도 필수다(중간 프레임은
키프레임 없이 디코드 불가). 멀티트랙 통합 시크는 각 대상 트랙의 ① 결과 중
**최소 오프셋**에서 ②를 시작한다 — 모든 트랙이 자기 sync point를 지나치지 않는다.

### 5.1 쓰는 중 — 같은 프로세스 (주 시나리오)

`writer.NewReader()` 로 획득. in-memory 인덱스 + committedSize 공유.
시크·테일 모두 파일 스캔 없이 즉시.

### 5.2 완성된 파일

`gmc.Open(path)` → 트레일러 확인 → Footer 하나로 트랙 구성 + 전체 인덱스 +
요약을 한 번에 로드. 스트림 스캔 없음.

### 5.3 미완성/크래시 파일

`gmc.Open(path)` → 트레일러 없음 → 앞에서부터 청크 헤더만 순차 스캔하며
인덱스 재구성, CRC 실패 지점 직전을 논리적 EOF로 확정. 복구 도구 불필요.

- TrackInfo → 트랙 등록, IndexCheckpoint → 엔트리를 인덱스에 병합,
  Data → 인덱스 대상 아니면 스킵 (keyframe 플래그가 선 것은 꼬리 보충 후보로 수집)
- 마지막 체크포인트 이후 꼬리 구간(및 체크포인트가 아예 없는 파일)은 스캔 중
  만난 Data 청크의 keyframe 플래그로 인덱스를 보충한다.
- **복구 스캔은 모든 청크의 CRC를 전수 검증한다** (payload 포함 순차 읽기).
  페이지 캐시의 디스크 플러시 순서는 보장되지 않으므로, 청크 헤더만 보고
  payload의 유효성을 신뢰할 수 없기 때문이다. 순차 읽기라 GB급 파일도 수 초
  수준이며, 더 빠른 오픈이 필요해지면 backward chain 최적화를 그때 추가한다
  — v1은 전방 스캔만.

## 6. 내구성과 복구

- append-only이므로 청크 스트림의 손상은 "마지막 쓰기가 중간에 끊긴" 꼬리 부분에 국한된다.
- 복구 = 순차 스캔 중 첫 CRC 불일치(또는 파일 끝 초과 길이) 지점 직전까지 인정.
- Tags 영역은 유일한 제자리 갱신 지점이지만 더블 슬롯이라 갱신 중 크래시 시
  직전 값으로 폴백된다(§3.3) — 별도 복구 절차 불필요.
- 스캔 중 발견한 완전한 청크는 전부 유효 — 체크포인트가 없어도 Data 청크 자체로
  인덱스를 재구성할 수 있다 (체크포인트는 스캔을 빠르게 할 뿐).
- 크래시 파일을 이어쓰기(append 재개)하는 기능은 v1 비목표 — 열어서 읽고,
  새 파일로 이어 녹화하는 것을 기본 패턴으로 한다.

## 7. Go SDK API 스케치

```go
// 쓰기
w, err := gmc.Create("rec.gmc", gmc.CreateOptions{
    Private:      manifestBytes,         // 파일 레벨 private data
    TagsAreaSize: 8 << 10,               // Tags 예약 영역 (기본 8 KiB, 슬롯 2개)
})
video, err := w.AddTrack(gmc.TrackInfo{
    Kind: gmc.KindVideo, Codec: "h264",
    TimebaseNum: 1, TimebaseDen: 90000,
    Private: avcConfig,                  // 트랙 레벨 private data
})
err = w.WriteFrame(video, gmc.Frame{PTS: pts, Keyframe: true, Data: nal})
                                         // 트랙 내 pts 역행 시 ErrNonMonotonicPTS

// 세션 메타정보 (녹화 도중 언제든 추가/갱신 가능, 앞쪽 Tags 영역에 제자리 기록)
err = w.SetStartTime(time.Now())         // gmc.start_time_unix_ns 편의 API
err = w.SetTag("gmc.location", []byte("37.5665,126.9780"))
err = w.SetTag("camera.id", []byte("cam-03"))  // 슬롯 초과 시 ErrTagsTooLarge

err = w.Finalize()                       // Footer+트레일러 기록 후 close
// 또는 w.Close()                        // Footer 없이 닫기 — 유효한 미완성 파일로
                                         // 남고, 재오픈 시 스캔 경로로 열림

// 쓰는 중 읽기 (같은 프로세스)
r := w.NewReader()
it, err := r.SeekPTS(video, targetPTS)   // 단일 트랙 과거 구간 시크
mux, err := r.ReadInterleaved(targetPTS) // 전 트랙 저장 순서 순회 (가변 인자로 트랙 필터)
for it.Next() { frame := it.Frame(); ... }
tail, err := r.Follow(ctx, video)        // 라이브 테일. 인자 없으면 전 트랙.
                                         // writer Finalize/Close 시 잔여 소진 후 EOF

// 완성/크래시 파일 열기
r, err := gmc.Open("rec.gmc")            // Footer 있으면 즉시, 없으면 스캔 복구
info := r.FilePrivate()                  // 헤더 private data
tags := r.Tags()                         // Tags 영역의 최신 스냅샷 (유효 슬롯 중 seq 큰 쪽)
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
- **Tags 영역은 고정 용량 + 제자리 갱신**: 순수 append-only의 유일한 예외. 대신 파일 앞부분만 읽어 즉시 조회 가능하고, 더블 슬롯으로 크래시에 안전. 용량은 생성 시 확정(기본 8 KiB)이며 초과 시 에러 — 세션 속성은 소량·저빈도 갱신을 가정하고, 고빈도로 변하는 값은 메타데이터 트랙이 올바른 자리.

## 9. 다음 단계

1. 본 설계 검토·확정
2. 상세 개발 계획 작성 (`docs/feature/2026-07-03-01-gmc-container-plan.md`)
   - 패키지 구조, 태스크 분해, 테스트 전략 (동시성 테스트: `-race`, 크래시 주입 테스트 포함)
3. 구현 (TDD): 포맷 인코딩/디코딩 → Writer → Reader(시크/테일) → 복구 → 동시성
