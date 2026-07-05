# GMC 코덱 규약(gmc/codec) + MKV import/export(mkv) 설계

- 작성일: 2026-07-04
- 상태: 구현 완료 (2026-07-05 병합 739aa9d — 태스크 9개 전부 리뷰 승인, Opus 최종 브랜치 리뷰 통과, §4.3 샘플 확장 완료)
- 대상: `github.com/Youngju-Heo/go-container` — 신규 패키지 `gmc/codec`, `mkv`
- 선행: [GMC 코어 v1.1 확장](2026-07-04-01-gmc-core-v11-design.md) (Reordered 모드·DTS 확장점)
- 관련: [GMC 포맷 설계](2026-07-03-01-gmc-container-design.md)

## 1. 배경과 목표

GMC v1은 codec-agnostic 컨테이너로 완성됐다. 이제 두 기능을 추가한다:

1. **코덱 스트림 저장 규약** — H.264/HEVC, PCM/Opus/AAC/FLAC, UTF-8 텍스트를
   MKV(Matroska)에서 사용하는 방법과 동일하게 GMC에 담는 규약과 헬퍼.
2. **MKV import/export** — 전체 또는 구간(시간 범위) 양방향 변환.

### 확정된 요구사항·결정

| 항목 | 결정 |
|---|---|
| 규약 방식 | **접근 A: Matroska 관례 그대로 채택** — CodecID·CodecPrivate·블록 페이로드를 MKV와 동일하게. 변환은 무손실 재패키징 |
| B-frame | 코어 v1.1의 Reordered 모드로 수용 (DTS 합성 없음 — Matroska도 DTS를 저장하지 않음) |
| 의존성 | 순수 표준 라이브러리 — EBML/Matroska 파서·뮤서 직접 구현 |
| 모듈 구조 | 같은 모듈 내 하위 패키지 (`gmc/codec`, `mkv`) |
| 구간 시작 경계 | 이전 키프레임으로 스냅 (mkvmerge와 동일, GMC SeekPTS 의미론과 동일) |
| 검증 | 자체 라운드트립 테스트 + ffmpeg/VLC 수동 절차 + 샘플 확장 |

### 비목표

- 재인코딩·트랜스코딩 (컨테이너 계층은 프레임 바이트를 그대로 옮긴다)
- WebM 방언, Chapters, Attachments, 다중 Segment
- 쓰기 중(라이브) GMC 파일의 export — export 입력은 `gmc.Open` 가능한 파일
- MKV 레이싱 **쓰기** (읽기는 3종 모두 지원, 쓰기는 미사용 — 유효한 표준형)
- 나열된 7개 코덱 외의 코덱 (미지원 트랙은 skip + 보고)

## 2. gmc/codec — Matroska 관례 코덱 규약

gmc 코어는 계속 Private·Data를 불투명 blob으로 취급한다. 본 패키지는 그 blob의
**해석 규약**을 정의하는 계층이다. (원 소비자 문서 §6의 "2번째 독립 소비자가
생기면 gmc/codec 승격" 규칙의 이행 — MKV 변환기가 2번째 소비자다.)

### 2.1 코덱 ID와 프레임 페이로드 규약

`TrackInfo.Codec`에는 Matroska CodecID 문자열을 그대로 사용한다.
`Frame.Data`는 **MKV 블록 페이로드와 동일한 바이트**다.

| 상수 | 값 | Kind | Frame.Data 규약 |
|---|---|---|---|
| `CodecAVC` | `V_MPEG4/ISO/AVC` | Video | length-prefixed NAL 나열 (길이 필드 크기는 avcC의 lengthSizeMinusOne), 디코드 순서 |
| `CodecHEVC` | `V_MPEGH/ISO/HEVC` | Video | 상동 (hvcC) |
| `CodecPCM` | `A_PCM/INT/LIT` | Audio | 인터리브 리틀 엔디언 정수 샘플 블록 |
| `CodecOpus` | `A_OPUS` | Audio | Opus 패킷 1개 |
| `CodecAAC` | `A_AAC` | Audio | raw AAC 프레임 (ADTS 헤더 없음) |
| `CodecFLAC` | `A_FLAC` | Audio | FLAC 프레임 1개 |
| `CodecTextUTF8` | `S_TEXT/UTF8` | Data | `duration u64(트랙 timebase, LE)` + UTF-8 본문 |

- **프레임 = MKV 블록 1개** (레이싱 해제 후). 오디오도 코덱 프레임 단위.
- **Keyframe 플래그**: 영상은 코덱 키프레임(IDR 등)에만. 오디오·텍스트는 모든
  프레임 (MKV SimpleBlock keyframe 비트와 동일 의미).
- **텍스트 duration 접두**: MKV는 자막 지속시간을 블록 밖(BlockDuration)에 두지만
  GMC Frame에는 그 자리가 없으므로 페이로드 앞 8바이트로 규약화한다. 지속시간
  미상이면 0.

### 2.2 Private envelope

MKV는 샘플레이트·채널·해상도를 CodecPrivate가 아닌 TrackEntry의 Audio/Video
요소에 둔다(예: PCM은 CodecPrivate가 빈 값). GMC TrackInfo에는 대응 필드가
없으므로, `TrackInfo.Private` 안에 소형 envelope를 규약으로 정의한다.
MKV TrackEntry 구조의 거울상이라 변환이 순수 구조 복사가 된다(비트스트림
파싱 불필요, PCM 파라미터 무손실).

```
공통 선두: version u8 (=1)
Kind별 파라미터 (TrackInfo.Kind로 해석 분기):
  Video: width u32, height u32
  Audio: sampleRate u32, outputSampleRate u32(0=동일; HE-AAC용),
         channels u8, bitDepth u8(0=미지정)
  Data(텍스트): (파라미터 없음)
그 뒤: privateLen u32 + CodecPrivate 원본
       (avcC / hvcC / OpusHead / AudioSpecificConfig / FLAC 헤더블록)
```

모든 정수는 리틀 엔디언. MKV SamplingFrequency(float)는 정수 Hz로 반올림해
담는다(실사용 값은 모두 정수).

### 2.3 API 스케치

```go
package codec

const (
    CodecAVC      = "V_MPEG4/ISO/AVC"
    CodecHEVC     = "V_MPEGH/ISO/HEVC"
    CodecPCM      = "A_PCM/INT/LIT"
    CodecOpus     = "A_OPUS"
    CodecAAC      = "A_AAC"
    CodecFLAC     = "A_FLAC"
    CodecTextUTF8 = "S_TEXT/UTF8"
)

type VideoParams struct{ Width, Height uint32 }
type AudioParams struct {
    SampleRate, OutputSampleRate uint32
    Channels, BitDepth           uint8
}

// envelope 코덱
func EncodeVideoPrivate(p VideoParams, codecPrivate []byte) []byte
func DecodeVideoPrivate(b []byte) (VideoParams, []byte, error)
func EncodeAudioPrivate(p AudioParams, codecPrivate []byte) []byte
func DecodeAudioPrivate(b []byte) (AudioParams, []byte, error)
func EncodeTextPrivate(codecPrivate []byte) []byte
func DecodeTextPrivate(b []byte) ([]byte, error)

// 텍스트 프레임 페이로드
func EncodeTextFrame(duration uint64, text string) []byte
func DecodeTextFrame(b []byte) (duration uint64, text string, err error)

// 라이브 인코더(Annex-B 출력, 예: RTSP) → 본 규약 변환 헬퍼
func AnnexBToLengthPrefixed(dst, annexb []byte, lengthSize int) ([]byte, error)
func BuildAVCC(sps, pps [][]byte) ([]byte, error)
func BuildHVCC(vps, sps, pps [][]byte) ([]byte, error)
```

- Annex-B 헬퍼는 MKV 변환에는 불필요(MKV가 이미 length-prefixed)하나,
  이 규약으로 직접 녹화하는 응용(media-recorder 등)의 인제스트 경로에 필요하다.
- `BuildHVCC`는 주어진 파라미터셋 나열 중심의 최소 조립(프로파일 필드는 SPS에서
  필요한 최소만 추출) — 상세는 계획 단계에서 확정.

### 2.4 Timebase 규약

- 모든 트랙의 pts 0 = 세션 원점(GMC 기존 규약). 절대시각은
  `gmc.start_time_unix_ns` 태그.
- **Import**: 전 트랙 timebase = MKV TimestampScale (기본 1ms →
  TimebaseNum/Den = 1/1000). pts = 클러스터 ts + 블록 상대 ts. 무손실.
- **Export**: 기본 TimestampScale 1,000,000ns(1ms). 트랙 pts → ns → scale 나눗셈을
  128비트 중간값(코어 v1.1의 시간 변환 헬퍼와 동일 방식)으로 수행. 트랙
  timebase가 scale과 정합하지 않으면(예: 1/90000) 반올림이 발생한다 — MKV 포맷
  고유 특성으로 문서화하고 `ExportOptions.TimestampScale`로 조정 가능하게 한다.
  MKV에서 import한 파일의 재export는 정합하므로 무손실.

## 3. mkv — import/export 패키지

### 3.1 구조

```
mkv/
  ebml.go     — EBML 프리미티브: vint(ID·크기) 리더/라이터, 요소 순회,
                unknown-size 처리, 쓰기 시 크기 패치
  demux.go    — 읽기: EBMLHeader 검증(DocType=matroska), Info·Tracks 파싱,
                Cluster 순회(SimpleBlock/BlockGroup), 레이싱 해제(Xiph/fixed/EBML)
  mux.go      — 쓰기: EBMLHeader + Segment{SeekHead, Info, Tracks,
                Cluster*, Cues, Tags} 최소 표준형
  import.go   — MKV → GMC
  export.go   — GMC → MKV
```

- **읽기는 방어적**: 미지 요소는 크기로 skip, 크기 상한 검증(스캔 폭주 방어),
  unknown-size Segment/Cluster 수용, 레이싱 3종 해제. 손상 시 명확한 에러
  (부분 복구는 비목표 — GMC 쪽 G항목과 동일하게 연기).
- **쓰기는 최소 표준형**: 레이싱 미사용. 기본은 SimpleBlock이고, duration이
  필요한 자막 블록만 BlockGroup+BlockDuration으로 기록. 클러스터는 영상 키프레임
  경계·상대 ts i16 한계·크기(~5MiB) 중 먼저 도달 시 분할, Cues는 영상
  키프레임(영상 없으면 트랙 1 기준 주기적). SeekHead·Duration은 예약 공간
  (Void) 선기록 후 finalize 시 패치 — 출력은 seek 가능한 파일이므로 안전.

### 3.2 공개 API

```go
package mkv

// Range는 스트림 상대시간 구간. 제로값 = 전체.
// From은 키프레임 스냅(이전 sync point로 당김), To의 경계 규칙은 §3.4.
type Range struct{ From, To time.Duration }

type ImportOptions struct {
    Range Range
}

type ExportOptions struct {
    Range          Range
    TimestampScale uint64 // 0이면 1_000_000 (1ms)
}

type SkippedTrack struct {
    Number  uint64 // MKV 트랙 번호
    CodecID string
    Reason  string
}

type Result struct {
    Tracks        int
    Frames        int
    SkippedTracks []SkippedTrack
}

func Import(mkvPath, gmcPath string, opts ImportOptions) (*Result, error)
func Export(gmcPath, mkvPath string, opts ExportOptions) (*Result, error)
```

### 3.3 매핑 규칙

| MKV | GMC | 방향·비고 |
|---|---|---|
| TrackEntry.CodecID | TrackInfo.Codec | 양방향 그대로 |
| TrackEntry.CodecPrivate + Audio/Video 요소 | TrackInfo.Private (codec envelope §2.2) | 양방향 구조 복사 |
| TrackType 1(video)/2(audio)/17(subtitle) | KindVideo/KindAudio/KindData | 양방향 |
| 블록 페이로드 (레이싱 해제 후) | Frame.Data | 양방향 무변환. 자막은 duration 접두 부착/제거 |
| SimpleBlock keyframe 비트, BlockGroup의 ReferenceBlock 부재 | Frame.Keyframe | 양방향 |
| Info.TimestampScale | 트랙 timebase (§2.4) | 양방향 |
| Info.DateUTC (2001 epoch ns) | `gmc.start_time_unix_ns` 태그 (Unix epoch ns) | 양방향, epoch 변환 |
| Tags/SimpleTag (이름, UTF-8 값) | GMC Tags (동일 이름) | 양방향. `gmc.*` 예약 키는 DateUTC 대응 외 export 제외 |
| Info.MuxingApp/WritingApp | — | export 시 "gmc-go" 기록, import 시 무시 |
| — | GMC 파일 Private | MKV에 대응 없음 — export 시 탈락(문서화), import 시 빈 값 |

**import 정책:**
- 표 §2.1의 7개 코덱 외 트랙은 **skip하고 Result.SkippedTracks에 보고**
  (한 트랙 때문에 파일 전체를 실패시키지 않는다).
- 영상 트랙은 `Reordered` 모드로 AddTrack (B-frame 유무를 컨테이너가 판정하지
  않으므로 항상 안전한 쪽). 오디오·텍스트는 기본 모드.
- 레이싱 해제 시 개별 프레임 pts: TrackEntry.DefaultDuration 있으면 보간,
  없으면 동일 pts(GMC는 동일 pts 허용) — 문서화된 제약.
- 완성 전 MKV(unknown-size 꼬리)도 읽는 데까지 import.

### 3.4 구간 변환 의미론

- **From (시작)**:
  - 영상 트랙: "From 이하 마지막 sync point(키프레임)"로 스냅 — 결과물이
    요청보다 최대 GOP 하나만큼 앞에서 시작하며 모든 프레임이 디코드 가능.
  - 오디오·텍스트 트랙: 정확히 `pts ≥ From` (To 규칙과 대칭, ffmpeg/mkvmerge
    관례). 모든 프레임이 독립 디코딩 가능하므로 스냅이 불필요하고, GMC의
    오디오 인덱스는 샘플링되므로 인덱스 기반 스냅은 시작점을 임의로 멀리
    당길 수 있어 채택하지 않는다.
- **To (끝)**:
  - 영상(재정렬 가능) 트랙: **GOP 단위** — "pts ≥ To인 첫 키프레임" 직전까지
    포함. 디코드 순서상 참조는 항상 뒤→앞이므로, 이 규칙이 잘린 꼬리에서
    참조 깨짐 없이 자를 수 있는 가장 단순한 경계다.
  - 오디오·텍스트 트랙: 정확히 `pts < To`.
- Import의 Range는 MKV 스트림 상대시간, Export의 Range는 GMC pts 시간축
  (둘 다 pts 0 기준) — 동일한 Range 타입을 공유한다.

### 3.5 에러·경계 처리

- EBML 파싱: 크기 필드가 파일 범위를 벗어나면 즉시 에러 (GMC 쪽 maxPayloadLen
  방어와 동일 사상). CRC-32 요소는 v1에서 검증 생략(선택 요소, ffmpeg 기본
  미기록) — 문서화.
- Export 시 GMC 트랙 Codec이 §2.1 목록 밖이면 해당 트랙 skip + 보고
  (import와 대칭).
- Export 시 envelope 디코드 실패(규약 미준수 GMC 파일)는 그 트랙 skip + 보고.

## 4. 검증 계획

### 4.1 자동 (go test)

1. **단위**: vint·요소 라운드트립, 레이싱 3종 해제, envelope 코덱,
   텍스트 프레임 코덱, Annex-B 변환 헬퍼.
2. **통합 라운드트립** (`sample/video-clip.mkv` = H.264+FLAC+S_TEXT/UTF8 활용):
   - MKV→GMC→MKV→재demux: 프레임 바이트·pts·keyframe·트랙 매핑이 원본 demux
     결과와 완전 일치 (파일 바이트 비교가 아닌 논리 비교 — 뮤서 레이아웃은
     ffmpeg과 다를 수 있음).
   - GMC(규약 준수 생성)→MKV→GMC: 프레임 바이트·pts 완전 일치.
   - 구간: 10~20s 요청 → 시작이 10s 이하 키프레임인지, 끝 규칙(§3.4) 준수 검증.
3. **회귀**: gmc 코어 전체 테스트 무손상.

### 4.2 수동 절차 (별도 환경, 릴리스 전 1회)

```
ffprobe -show_streams out.mkv     # 트랙 구성·코덱·해상도·샘플레이트 확인
ffplay/VLC out.mkv                # 재생·시크·A/V 동기·자막 표시
mkvalidator out.mkv               # (선택) Matroska 규격 준수
```

### 4.3 샘플 확장 — 완료 (2026-07-05)

`sample/`에 확보된 픽스처로 §2.1의 7개 코덱 전부가 실샘플 라운드트립으로 검증된다:

| 파일 | 구성 | 검증 포인트 |
|---|---|---|
| `video-clip.mkv` (30s) | H.264 + FLAC + S_TEXT/UTF8 | 기본 3트랙 라운드트립 |
| `test-clip.mkv` (30s) | H.264(**B-frame**) + FLAC + **AAC** + **Opus** + 날짜시각 자막 | 비단조 pts 실검증, 동종 다중 오디오 |
| `test-clip-hevc.mkv` (10s) | **HEVC**(B-frame) + **PCM** | hvcC 통과, PCM envelope 파라미터 보존 |
| `test-clip-000~002.gmc` (각 10s) | 위 test-clip을 GOP 경계로 분할한 GMC 세그먼트 | 스티칭 예제 입력 (각 세그먼트 키프레임 시작) |

생성 명령: test-clip은 `ffmpeg -i video-clip.mkv -c:v libx264 -bf 3 …` (커밋 74931bf),
hevc는 `-c:v libx265 -c:a pcm_s16le -t 10` (커밋 472fccc), GMC 세그먼트는 Demuxer→gmc
Writer 직접 기록·영상은 경계 이후 첫 키프레임에서 전환 (커밋 aa1ca8e).

## 5. 대안 검토

| 대안 | 기각 사유 |
|---|---|
| GMC 고유 규약(Annex-B 등) + 변환 시 매핑 | 변환기가 코덱별 비트스트림 재작성을 수행해야 함 — 복잡도·손실 위험, "MKV와 동일하게" 요구와 불일치 |
| 외부 Matroska 라이브러리 | 프로젝트 무의존 원칙 위반 |
| ffmpeg CLI 위임 | 상동 + 배포 환경 요구 발생 |
| Cues 기반 구간 import 최적화 | v1은 순차 스캔으로 충분(구간 밖 payload는 skip) — 필요 시 후속 최적화 |

## 6. 다음 단계 — 완료 및 잔여 후속

계획(`2026-07-04-02-gmc-mkv-plan.md`) 9개 태스크 전부 구현·리뷰 완료, Opus 최종
브랜치 리뷰 통과 후 main 병합(739aa9d). 리뷰 과정에서 수정된 실결함: EBML peek
부분읽기 절단, BlockGroup 손상 오류 전파, Export 전 트랙 skip 손상 출력 방지,
영상 없는 파일 SeekHead Cues 댕글링 제거.

잔여 후속 (비차단):
- `-race` 검증: amd64/linux 환경 또는 CI (이 개발 머신 windows/arm64 미지원)
- §4.2 수동 재생 검증: VLC·mkvalidator (ffprobe 교차 확인은 샘플 제작 시 수행)
- Opus `CodecDelay`/`SeekPreRoll` 요소 미보존 (OpusHead의 preSkip은 CodecPrivate로
  보존 — 일반 재생 무영향, 갭리스 편집 엄밀성에서만 차이)
- 음수 Range 입력 가드, `splitAnnexB` 다중 선행 zero 방어
