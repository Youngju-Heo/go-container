# GMC 코어 v1.1 확장 설계 — DTS 확장점·Reordered 모드·엣지 조회·절대시간 시크·완결 판정

- 작성일: 2026-07-04
- 상태: 승인 (구현 대기)
- 대상: `github.com/Youngju-Heo/go-container/gmc`
- 출처: 소비자(media-recorder) 측 제안 문서 「GMC 컨테이너 추가 기능 제안」(2026-07-04)을
  검토·채택하고 보완한 결과. 원 제안의 A·B·E·D를 수용, G(중간 손상 resync)는 연기.
- 관련: [GMC 포맷 설계](2026-07-03-01-gmc-container-design.md),
  [코덱 규약 + MKV 변환 설계](2026-07-04-02-gmc-mkv-design.md) — 본 문서가 그 선행 단계(Phase 0)

## 1. 배경과 범위

media-recorder 통합 검토에서 컨테이너 몫으로 판정된 기능 제안을 받았다. 동시에
진행 중인 MKV import/export 기획이 B-frame 지원을 즉시 현실 요구로 만들었으므로,
두 흐름을 하나의 코어 확장(v1.1)으로 통합한다.

| # | 기능 | 온디스크 변경 | 결정 |
|---|---|---|---|
| A | B-frame: DTS 확장점 + Reordered 쓰기 모드 | 예 (추가적 flag) | 수용 + 보완 (§3) |
| B | 라이브 엣지 조회 `LastPTS`/`LastTime` | 아니오 | 수용, maxPTS 의미론 보정 (§4) |
| E | 절대시간 시크 `SeekTime` | 아니오 | 수용 (§5) |
| D | 완결 여부 `Finalized` | 아니오 | 수용 (§6) |
| G | 중간 손상 resync 복구 | 아니오 | **연기** — 아카이브 내구성이 이슈가 될 때 재론 |

원 제안의 분류 원칙(코덱/타 포맷 인지 금지, public API로 가능한 것은 응용 몫,
포맷 바이트 변경은 컨테이너 몫)을 그대로 승계한다. 컨테이너는 여전히
codec-agnostic·순수 표준 라이브러리를 유지한다.

## 2. 호환성 원칙

- 유일한 온디스크 변경은 Data payload의 **추가적(additive) flags 비트**다.
  DTS를 쓰지 않는 파일은 기존 v1과 **바이트 단위로 동일**하다.
- 기존 v1 파일은 새 리더가 100% 읽는다. 헤더 `version`은 1을 유지한다
  (출시 전이며, 지금 비트를 정의해 두면 앞으로 배포되는 모든 리더가 이해한다 —
  원 제안의 "동결 전 예약" 논리를 구현까지 포함해 수용).

## 3. A — DTS 확장점 + Reordered 쓰기 모드

### 3.1 문제

- 현재 `WriteFrame`은 트랙 내 pts 단조 비감소를 강제한다. B-frame이 있는
  H.264/HEVC는 디코드 순서로 넣으면 pts가 비단조라 기록 자체가 거부된다.
- 원 제안은 "단조성 검사를 DTS 기준으로, DTS 없으면 PTS를 DTS로 간주"를 제시했다.
  그러나 **Matroska는 DTS를 저장하지 않는다** — MKV import 시 우리 손에는 디코드
  순서의 비단조 pts만 있다. 원 제안 규칙만으로는 B-frame MKV가 여전히 거부된다.
  DTS 합성은 코덱 인지(재정렬 깊이 추정)가 필요해 리트머스 1 위반이므로 기각.

### 3.2 온디스크 포맷 (원 제안 그대로)

```
flags 비트 1 = flagHasDTS (0x02)

Data payload:
  trackID u16
  flags   u8
  pts     u64
  dts     u64    ← flagHasDTS가 설 때만 존재
  data    나머지
```

- 헤더 크기: dts 없으면 11(기존과 동일), 있으면 19.
- 저장 순서 = 디코드 순서 정의는 불변.

### 3.3 API

```go
type Frame struct {
    PTS      uint64
    DTS      uint64 // 디코드 순서 타임스탬프. HasDTS일 때만 유효
    HasDTS   bool
    Keyframe bool
    Data     []byte
}

type TrackInfo struct {
    // ...기존 필드 불변...

    // Reordered는 쓰기 검증 모드다. 직렬화되지 않으며(ID처럼 읽기 결과에서는
    // 무의미), true면 이 트랙을 재정렬(B-frame) 스트림으로 취급한다.
    Reordered bool
}
```

### 3.4 WriteFrame 검증 규칙

| 트랙 모드 | 규칙 | 비고 |
|---|---|---|
| 기본 (Reordered=false) | 유효 디코드 ts(= DTS, 없으면 PTS)가 단조 비감소 | DTS 미사용 시 **기존 동작과 완전 동일** |
| Reordered=true | **Keyframe 프레임의 pts만** 직전 키프레임 pts 이상 강제. 비키프레임 pts는 검사하지 않음 | 인덱스 정렬성(시크 정확성)에 필요한 최소 불변식. open-GOP leading frame 수용 |

- 위반 시 에러는 기존 `ErrNonMonotonicPTS`를 재사용한다.
- 가이드: 호출자가 DTS를 확보할 수 있으면 기본 모드 + `HasDTS`를 권장.
  `Reordered`는 DTS가 없는 재정렬 스트림(예: MKV import) 전용.
- 인덱스는 지금처럼 키프레임(sync point)에만 걸린다. Reordered에서도 키프레임
  pts 단조가 보장되므로 인덱스는 정렬 상태를 유지하고, 시크 의미론
  ("목표 pts 이하 마지막 sync point에서 전방 스캔")은 무변경이다.
  B-frame 재정렬은 스캔 구간(GOP 1개) 안에서 흡수된다.

### 3.5 파생 보정 — lastPTS → maxPTS

재정렬 스트림에서는 "마지막에 쓴 프레임의 pts"가 최신 재생 시각이 아니다.

- `trackState`에 `maxPTS`(커밋된 최대 pts)를 추가하고 매 프레임 갱신한다.
- Footer의 `trackSummary.lastPTS` 필드에는 maxPTS를 기록한다
  (인코딩 불변 — 담는 값의 의미만 명확화. 비재정렬 스트림에서는 기존 값과 동일).
- 스캔 복구 경로도 트랙별 max pts를 추적한다 (§4에서 사용).
- `firstPTS`는 기존 의미(첫 기록 프레임의 pts)를 유지한다.

### 3.6 비목표 (원 제안 승계)

- 컨테이너는 B-frame 재정렬을 수행하지 않는다 — 호출자가 준 (pts[, dts])를
  저장하고 순서 불변식만 검증한다. 재정렬은 디코더/먹서 책임.
- DTS 합성 없음.

## 4. B — 라이브 엣지 조회

```go
// LastPTS는 해당 트랙에 커밋된 최대 pts를 반환한다.
func (r *Reader) LastPTS(id TrackID) (uint64, bool)

// LastTime은 LastPTS의 절대시각 = StartTime + LastPTS×timebase.
// StartTime 미설정이거나 프레임이 없으면 ok=false.
func (r *Reader) LastTime(id TrackID) (time.Time, bool)
```

경로별 소스:

| 리더 경로 | maxPTS 소스 |
|---|---|
| 라이브 (`w.NewReader()`) | `w.tracks[id].maxPTS` (w.mu 아래 조회) |
| 완성 파일 (Footer) | `loadFooter`가 현재 버리는 trackSummary를 **보존**하도록 수정 |
| 크래시/미완성 (스캔) | 스캔 중 트랙별 max pts 기록 (스캔은 CRC 전수 검증으로 payload를 이미 읽으므로 헤더 파싱 추가 비용 무시 가능) |

## 5. E — 절대시간 시크

```go
// SeekTime은 절대 벽시계 t를 각 트랙 timebase의 pts로 변환해
// ReadInterleaved와 동일한 의미론(각 트랙 sync point 중 최소 오프셋)으로
// 이터레이터를 배치한다. 트랙 미지정 시 전 트랙.
// gmc.start_time_unix_ns 태그가 없으면 ErrNoStartTime.
func (r *Reader) SeekTime(t time.Time, tracks ...TrackID) (*Iterator, error)

var ErrNoStartTime = errors.New("gmc: start time tag not set")
```

- 변환식: `pts = Δns × den / (num × 1e9)`, `Δns = t.UnixNano() − startNs`.
- `t`가 start 이전이면 pts 0으로 클램프(스트림 처음).
- **오버플로 처리**: `Δns × den`은 장시간 녹화에서 u64를 넘는다.
  `math/bits.Mul64`/`Div64`로 128비트 중간값을 사용한다. 제수는
  `num×1e9 ≤ 2^32×10^9 < 2^64`라 u64로 안전. 몫이 u64를 넘으면(사실상 파일 끝
  너머) MaxUint64로 클램프. 정밀도를 잃는 `Δns/1e9 × den` 형태는 금지.
- 역변환(`LastTime`용) `ns = pts × num × 1e9 / den`도 동일한 128비트 헬퍼를 공유한다.
- `SeekPTS`(트랙-로컬 pts 시크)는 저수준 프리미티브로 그대로 유지.

## 6. D — 완결 여부 판정

```go
// Finalized는 파일이 footer/trailer로 정상 종료됐는지 보고한다.
// 라이브 리더는 (아직 쓰는 중이므로) 항상 false.
func (r *Reader) Finalized() bool
```

- `Reader`에 `finalized bool` 추가, `Open`의 footer 로드 성공 경로에서만 true.

## 7. 테스트 전략

- **A**: dts 인코딩/디코딩 라운드트립(11/19바이트 두 형태), 기본 모드에서
  HasDTS 단조 검증, Reordered 트랙에 IBBP형 pts 패턴(비단조) 기록 후
  SeekPTS/ReadInterleaved 정확성, 키프레임 pts 역행 거부.
- **B**: LastPTS 3경로(라이브·footer 재오픈·크래시 스캔) 일치 검증,
  재정렬 스트림에서 maxPTS > 마지막 기록 pts인 케이스.
- **E**: 변환 정확성(90kHz·48kHz·1kHz), start 이전 클램프, 수일 길이 ×
  den 90000 오버플로 경계, ErrNoStartTime.
- **D**: 3경로(라이브 false / Finalize 후 true / Close-미완성 false).
- 기존 전체 회귀 + v1 파일(dts 없는) 바이트 호환 확인.
- 동시성 영향 없음(잠금 모델 불변)이나 전체 테스트는 `-race` 지원 플랫폼에서 재실행.

## 8. 대안 검토

| 대안 | 기각 사유 |
|---|---|
| DTS 필드 없이 pts 규칙 완화만 | DTS를 가진 호출자(향후 MP4 remux 등)의 정보를 버림. 원 제안의 확장점 요구 미충족 |
| DTS 기준 검사만 (원 제안 원안) | DTS가 없는 재정렬 스트림(MKV import)이 여전히 거부됨 |
| import 시 DTS 합성 | 코덱 인지 필요 — 컨테이너 순수성(리트머스 1) 위반 |
| TrackInfo에 Reordered 직렬화 | 읽기 경로에 소비자가 없음(시크·인덱스 무변경) — YAGNI. 필요해지면 추가적 확장 가능 |

## 9. 다음 단계

1. 본 설계 확정 → 상세 개발 계획(`2026-07-04-01-gmc-core-v11-plan.md`)
2. 구현(TDD) 후, 본 확장 위에서 [코덱 규약 + MKV 변환](2026-07-04-02-gmc-mkv-design.md) 진행
