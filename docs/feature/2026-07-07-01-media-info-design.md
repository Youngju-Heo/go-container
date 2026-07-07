# media-info 설계 문서

- 작성일: 2026-07-07
- 브랜치: `media-info`
- 상태: 설계 확정

## 1. 목적

`gmc` 및 `mkv` 파일의 저장 상태를 요약해 주는 CLI 유틸리티. 파일을 인자로 받아
타이틀·헤더·미디어·태그·인덱스 정보를 well-formed JSON으로 출력한다.

## 2. 실행 형태

```
media-info [options ...] <filename.gmc | filename.mkv>
```

- 배치 위치: `cmd/media-info/main.go`, 실행 파일명 `media-info`.
- 인자는 정확히 1개의 파일 경로. 0개 또는 2개 이상이면 오류.
- 포맷 감지: 확장자(`.gmc` / `.mkv`)로 1차 판별 후 매직바이트로 검증.
  확장자와 실제 내용이 불일치하면 명시적 오류.

## 3. 옵션

| 옵션 | 값 | 기본값 | 설명 |
|------|----|--------|------|
| `--info-all` | (flag) | off | header/media/tag/index 를 모두 yes 로 강제 (최우선) |
| `--info-header` | yes\|no | yes | 헤더 섹션 포함 여부 |
| `--info-media` | yes\|no | yes | 미디어(트랙) 섹션 포함 여부 |
| `--info-tag` | yes\|no | yes | 태그 섹션 포함 여부 |
| `--info-index` | yes\|no | no | 인덱스 요약 섹션 포함 여부 |
| `--output` | filename | (없음) | 지정 시 해당 파일로 출력, 없으면 stdout |
| `--help` | (flag) | - | 전체 옵션·인자 설명 출력 (한국어) |

규칙:

- `--info-all` 이 켜지면 나머지 `--info-*` 값과 무관하게 4개 섹션 모두 포함.
- `--info-*` 값이 `yes`/`no` 외이면 오류.
- 오류·진단 메시지는 stderr + 비정상 종료코드로 출력. stdout 에는 JSON 만 출력.
- JSON 은 2-space indent 로 정렬하여 출력(well-formed).

## 4. 라이브러리 변경 (요구 충족에 필요한 최소 범위)

작업 중인 코드에 대한 표적 개선으로, 요구사항과 직접 연결되는 범위만 수정한다.

1. **mkv**: `FileInfo` 에 `Title string` 필드 추가 + `parseInfo` 에 `idTitle`(0x7BA9)
   케이스 추가. (Segment Info 의 Title 요소 파싱)
2. **gmc**: `Reader` 에 공개 접근자 추가 — footer 의 per-track 요약
   (track 별 firstPTS / lastPTS / frames) 과 sync-point 수를 반환.
   - finalized 파일: footer 의 `trackSummary` 로 완전한 값 제공.
   - non-finalized 파일: footer 가 없으므로 frames 등 일부는 null(best-effort).

그 외 라이브러리 변경 없음.

## 5. JSON 스키마

공통 봉투(최상위) + 포맷별 섹션 내용. 선택되지 않은 섹션 키는 **생략**하고,
값이 없는 경우는 `null` 로 둔다.

### 공통 최상위

- `file`: `{ "path": string, "name": string, "size": number }`
- `format`: `"gmc"` | `"mkv"`
- `title`: mkv = Segment Info Title / gmc = `title` 태그 값 / 없으면 `null`

### gmc 예시

```json
{
  "file": {"path":"a.gmc","name":"a.gmc","size":123456},
  "format": "gmc",
  "title": null,
  "header": {
    "version": 1,
    "finalized": true,
    "startTime": "2026-07-07T00:00:00Z",
    "privateLen": 0,
    "trackCount": 2
  },
  "media": {
    "tracks": [
      {"id":1,"kind":"video","codec":"V_MPEGH/ISO/HEVC",
       "timebase":{"num":1,"den":90000},"reordered":true,
       "privateLen":24,"lastPTS":900000}
    ]
  },
  "tags": {"encoder":"..."},
  "index": {
    "syncPoints": 42,
    "tracks": [{"id":1,"firstPTS":0,"lastPTS":900000,"frames":300}]
  }
}
```

### mkv 예시

```json
{
  "file": {"path":"a.mkv","name":"a.mkv","size":123456},
  "format": "mkv",
  "title": "My Clip",
  "header": {
    "timestampScale": 1000000,
    "duration": 10.0,
    "dateUTC": "2026-07-07T00:00:00Z"
  },
  "media": {
    "tracks": [
      {"number":1,"type":"video","codecID":"V_MPEGH/ISO/HEVC",
       "pixelWidth":1920,"pixelHeight":1080,
       "defaultDuration":33333333,"codecPrivateLen":24},
      {"number":2,"type":"audio","codecID":"A_PCM/INT/LIT",
       "samplingFrequency":48000,"channels":2,"bitDepth":16}
    ]
  },
  "tags": {"TITLE":"..."},
  "index": null
}
```

### 세부 규칙

- **선택 섹션 생략**: `--info-*` 가 no 인 섹션의 키는 JSON 에서 생략한다.
- **tag 값 인코딩**: gmc 태그 값은 `[]byte` 이므로 유효 UTF-8 이면 문자열로,
  아니면 hex 인코딩 문자열로 출력하고 마커를 부여한다. mkv 태그 값은 이미 문자열.
- **mkv index**: 메타데이터-only 정책(추가 패킷 스캔 없음)상 인덱스 요약 통계를
  산출할 수 없다. 따라서 `index` 는 `null`. gmc 는 footer 로부터 스캔 없이 요약
  가능하므로 값이 채워진다. 이 비대칭은 의도된 설계다.
- **날짜/시각**: gmc `startTime`, mkv `dateUTC` 는 RFC3339(UTC) 문자열, 없으면 `null`.

## 6. 검증 (성공 기준)

1. 샘플 gmc/mkv 에 대해 출력이 well-formed JSON → `json.Valid` / 파싱 통과.
2. 각 옵션 조합(default, `--info-all`, 개별 `no`)이 올바른 섹션 포함/생략을 만든다.
3. `--output` 지정 시 파일 작성, 미지정 시 stdout 출력.
4. 잘못된 확장자 / `yes|no` 외 값 / 인자 개수 → stderr 오류 + 비정상 종료코드.
5. non-finalized gmc, mkv Title 유무 등 경계 케이스 처리.

## 7. 컴포넌트 경계

- **옵션 파서**: argv → 설정 struct(각 섹션 on/off, output 경로, 대상 파일). 검증 포함.
- **포맷 디스패처**: 확장자·매직바이트 → gmc/mkv 수집기 선택.
- **gmc 수집기**: `gmc.Reader` → 공통 봉투 struct 채움.
- **mkv 수집기**: `mkv.Demuxer` → 공통 봉투 struct 채움.
- **직렬화/출력**: 봉투 struct → indent JSON → stdout 또는 파일.

각 단위는 독립적으로 테스트 가능하며, 라이브러리(`gmc`/`mkv`)의 공개 API 만 의존한다.
