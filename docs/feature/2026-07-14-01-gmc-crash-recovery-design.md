# GMC 크래시 세그먼트 복구 — 분석 및 `Repair` API 제안 (N5)

- 날짜: 2026-07-14
- 대상: 녹화 중 프로세스 크래시/강제 종료로 finalize(footer 기록)되지 못한 부분 `.gmc` 세그먼트를 **버리지 않고 복구해 재생·목록에 이용**
- 관련 백로그: N5(녹화 크래시 잔해 `.gmc` 청소) — 본 제안은 "청소" 대신 "복구"로 방향 전환
- 두 리포에 걸침:
  - **gmc 라이브러리**: `github.com/Youngju-Heo/go-container` (현재 의존 버전 `v0.0.0-20260705021753-f08db6014cb7`), 하위 패키지 `gmc`
  - **media-recorder**: 본 리포 (`internal/recorder`, `internal/store`)

---

## 1. 배경 — 현재 동작

세그먼트 저장은 2단계다.

1. **저장 시작 시** 카탈로그에 `status="recording"`으로 먼저 INSERT (`internal/recorder/recorder.go:228-238`). 이때 `started_at==ended_at`(0폭), `duration_ms=0`, `size_bytes=0`. 목적은 진행 중 서빙/라이브 조회.
2. **저장 완료 시** 실제 `ended_at`·`duration_ms`·`size_bytes`·`status="complete"`로 UPDATE (`recorder.go:256-261` 부근). mux 완료 후 `WriteGMC`가 footer를 기록해 정상 마감.

**크래시 시 문제:** 진행 중 세그먼트는 `recording` 행 + 부분 `.gmc` 파일(footer 없음)만 남고 프로세스가 죽는다. 다음 기동 때 `cleanupRecording`(`recorder.go:114-125`, 호출부 `recorder.go:37`)이 **고아 `recording` 행을 삭제**한다. 그러나:

- `st.DeleteSegment`는 **카탈로그 행만 지우고 파일은 남긴다** → 부분 `.gmc`가 디스크에 고아로 잔존(용량 점유, retention은 카탈로그 기반이라 회수 안 함).
- 부분 파일 안의 실제 녹화분(크래시 직전까지)은 **복구되지 않고 버려진다.**

`WriteGMC` 주석(`internal/mux/gmc.go:72-73`)도 이미 이 전제를 명시한다: *"정상 완료 시 Finalize(footer 기록)하고, 기록 중 에러 발생 시 파일을 마감 없이 Close 한다(그래도 복구 가능한 상태로 남는다)."* — 즉 **포맷은 처음부터 복구를 염두에 두고 설계**돼 있다.

---

## 2. 실현 가능성 — GMC는 이미 크래시 파일을 읽는다

핵심 발견: **미완결(footer 없는) GMC 파일은 이미 읽을 수 있다.**

- `gmc.Open(path)` (`reader.go:32`)은 유효한 footer가 있으면 한 번에 읽고, **없으면 전체 CRC 스캔으로 복구**한다(주석 `reader.go:31`).
- 복구 후에도 `Tracks()`·`StartTime()`·`LastTime()`/`LastPTS()`·`SeekTime()`가 동작해 재생/seek 가능.
- `(*Reader).Finalized()` (`reader.go:360`)로 정상 마감 여부 판별.
- 라이브 서빙도 사실상 "미완결 파일 읽기"를 이미 상시 수행한다(진행 중 writer를 Reader로 Follow — `internal/liveseg`).

즉 **재생만 필요하면 추가 개발이 거의 없다.** 문제는 두 가지다.

1. 카탈로그에 `complete` 행으로 **재등록**해야 목록/일반 재생 경로에 뜬다.
2. 미완결 파일은 열 때마다 CRC 스캔 비용을 치른다(VOD seek·HLS 반복 open마다). → 한 번 **재마감(footer 기록)**해 두면 이후 open이 빨라진다.

---

## 3. 복구 스캔은 이미 footer 재료의 대부분을 만든다

`(*Reader).scan(size)` (`reader.go:85`)이 크래시 파일을 훑으며 복원하는 상태:

| 복원 항목 | 스캔 결과 |
|---|---|
| `r.tracks` | 트랙 정보(TrackInfo) |
| `r.idx` | 인덱스(키프레임/체크포인트 엔트리) |
| `r.maxPTS[id]` | 트랙별 **마지막** PTS |
| `r.committed` | **마지막 유효 청크 끝 오프셋**(= 논리적 EOF). 크래시로 잘린 뒷부분(불완전 청크) 직전 경계 |

`(*Writer).Finalize()` (`writer.go:344`)가 footer를 쓸 때 필요로 하는 입력:

- `tracks`, `sums`(트랙별 `firstPTS`/`lastPTS`/`frames`), `idx.dump()` → `encodeFooter(...)` (`footer.go:19`)
- `committed` 위치에 `encodeTrailer(footerOff)` (`footer.go:74`, 트레일러 16바이트 = `format.go:16` `trailerSize`)

**대조 결과:** 스캔이 이미 `tracks`·`idx`·`committed`·`lastPTS(=maxPTS)`를 갖고 있다. footer를 만들기 위해 **추가로 스캔에서 집계할 값은 딱 두 개**다 — 트랙별 **`firstPTS`**(첫 data 청크 PTS)와 **`frames`**(트랙별 data 청크 수). 둘 다 스캔 루프(`reader.go:96`의 `chunkData` 케이스)에서 한 줄씩 더 세면 된다.

---

## 4. 제안 API — `gmc.Repair(path)`

> **검토 반영(2026-07-14):** 아래는 실제 gmc 코드와 대조·수정된 확정 설계다.
> 주요 정정 두 가지 — (1) 벽시계 시작 시각의 근거는 **파일 헤더 `createdAt`이 아니라 `TagStartTime` 태그**다(파일 헤더에는 시각 필드가 없다, `header.go`). (2) 그 태그가 없을 수 있으므로 `RepairResult`에 **PTS 기반 트랙 요약**(`Summaries`)을 함께 담아 태그 유무와 무관하게 duration 산출이 가능하도록 한다. 부가 API `RepairFile(f)`는 YAGNI로 제외한다.

크래시 파일을 **제자리에서 정상(footer 있는) 파일로 만드는** 단일 함수. 프레임 재복사(re-mux) 없이 스캔+footer append만 한다.

```go
// Repair 는 크래시로 마감(footer)되지 못한 GMC 파일을 제자리에서 정상 파일로 만든다.
// - 이미 finalized 거나 유효 프레임이 0이면 파일을 건드리지 않고 Repaired=false 로 반환한다.
// - 아니면 복구 스캔으로 마지막 유효 프레임 경계(committed)를 찾고, 그 뒤 잔여(불완전 청크)를 잘라낸 뒤,
//   스캔이 재구성한 인덱스로 footer+trailer 를 append 한다.
// 멱등: 두 번 호출해도 결과 동일. 프레임 데이터는 건드리지 않는다(무손실).
func Repair(path string) (RepairResult, error)

type RepairResult struct {
    Repaired  bool           // false = 이미 정상 / 0프레임 (파일 변경 없음)
    Tracks    []TrackInfo    // ID 순 정렬. timebase 포함 → 호출자가 PTS→시간 변환 가능
    Summaries []TrackSummary // 트랙별 firstPTS/lastPTS/frames. PTS 기반이라 태그 무관하게 항상 정확
    Frames    int64          // 복구된 총 프레임 수 (= Summaries 의 frames 합)
    Size      int64          // 마감 후 파일 크기(바이트)
    StartTime time.Time      // TagStartTime 태그 기준 벽시계. 태그 없으면 zero
    LastTime  time.Time      // 마지막 유효 프레임 벽시계. StartTime 없으면 zero
}
```

- `TrackSummary`는 **기존 exported 타입**(`reader.go` `Summaries()` 반환 타입: `Track`/`FirstPTS`/`LastPTS`/`Frames`)을 그대로 재사용한다.
- 호출자(레코더)는 `Summaries`+`Tracks`(timebase)로 태그 없이도 duration을 계산할 수 있고, 태그가 있으면 `StartTime`/`LastTime` 벽시계를 바로 쓴다.

### 동작 순서

1. `os.OpenFile(O_RDWR)`로 파일 열기.
2. **공통 셋업 헬퍼로 Reader 구성** — 헤더 디코드 + 태그 읽기 + (유효 footer면 `loadFooter`, 아니면 `scan`). `Open`과 공유한다.
3. **finalized면** → no-op. footer 기준 메타(Tracks/Summaries/Frames/Size, 태그 있으면 Start/Last)로 `Repaired=false` 반환.
4. **유효 프레임이 0이면** → 파일 변경 없이 `Repaired=false, Frames=0` 반환(호출자가 삭제 판단).
5. `committed`로 파일 **truncate**(크래시 잔여 바이트 제거).
6. `committed`부터 `encodeFooter(tracks, sums, idx.dump())` chunk append → 이어서 `encodeTrailer(committed)` 기록.
7. `f.Sync()` 후 반환. `Repaired=true`.

### 왜 얇은가

- 2·5·6단계는 각각 기존 `Open` 셋업·파일 `Truncate`·`encodeFooter`/`encodeTrailer`의 재사용이다.
- 순증 로직은 "스캔에 트랙별 firstPTS/frames 두 값 집계 추가(Reader 비공개 맵 2개)" + "truncate 후 footer/trailer append" 정도다. `Reader.Summaries()`의 기존 공개 동작은 불변으로 둔다.
- `Writer.Finalize()`가 `w.committed`에 footer를 append하고 trailer를 쓰는 것과 **동일한 절차**를, 살아있는 Writer 대신 복구된 스캔 상태로 수행하는 것뿐이다.
- footer의 트랙별 `sums`는 `Finalize`와 동일하게 **모든 트랙**(프레임 0인 트랙 포함, 기본값 0)을 ID 순으로 담는다.

### 대안 API 형태(참고)

- `(*Reader).Finalize()`로 노출하는 방법도 있으나, Reader는 읽기용으로 열리므로 쓰기용 핸들이 필요하다. 호출자 입장에선 **독립 함수 `Repair(path)`**(내부에서 `O_RDWR` open)가 가장 단순하다.
- `RepairFile(f *os.File)` 형태(핸들 주입)는 현재 사용처가 없어 **제외**한다(필요 시 후속 추가).

---

## 5. media-recorder 연동 (gmc `Repair` 추가 후)

기동 시 `cleanupRecording`(현재 "고아 행 삭제")을 **"복구 또는 삭제"**로 교체한다.

```
// 기동 시, 고아 recording 행마다:
for _, o := range orphans {          // status="recording"
    res, err := gmc.Repair(o.Path)
    if err != nil || res.Frames == 0 {
        // 복구 불가/빈 파일 → 기존처럼 정리(행 삭제 + 파일 삭제로 N5 잔해도 함께 해소)
        _ = os.Remove(o.Path)
        _ = st.DeleteSegment(ctx, o.ID)
        continue
    }
    // 복구 성공 → complete 로 승격
    o.EndedAt   = res.LastTime
    o.DurationMS = res.LastTime.Sub(res.StartTime).Milliseconds()
    o.SizeBytes = res.Size
    o.Tracks    = len(res.Tracks)
    o.Status    = "complete"
    _ = st.UpdateSegment(ctx, o)
}
```

부수 효과(모두 이득):

- **N5 해소:** 복구 불가 잔해는 이 경로에서 파일까지 삭제되므로 별도 청소 로직이 필요 없어진다.
- **VOD/HLS 성능:** 복구+재마감된 파일은 footer가 생겨 이후 open이 CRC 스캔 없이 빠르다.
- **갭 처리 정합:** 복구된 세그먼트는 마지막 유효 프레임에서 끝나고, 그 이후~다음 기동 첫 세그먼트 사이는 기존 갭 로직이 자연히 갭으로 기록한다(`prevEnd`는 최신 complete 기준).

---

## 6. 엣지 케이스 / 폴백

| 상황 | 처리 |
|---|---|
| 이미 finalized 파일에 Repair | no-op(`Repaired=false`). 멱등. |
| 유효 프레임 0(헤더만 쓰고 죽음) | `Frames=0` → 레코더가 행+파일 삭제. |
| 파일 자체가 없음(행만 존재) | `Open` 실패 → 레코더가 행 삭제. |
| 트레일러/footer 손상(부분 마감) | 스캔이 마지막 유효 청크까지 복구 → 그 지점으로 재마감. |
| 크래시가 청크 중간 truncate | 마지막 **완전** 청크까지만 유효(스캔이 `committed`에서 멈춤) → 손실은 크래시 직전 미완 프레임뿐. |

---

## 7. 작업 분리 및 순서

- **(A) gmc 라이브러리** — **본 리포**(`github.com/Youngju-Heo/go-container`, `gmc/`)에서 진행. `Repair(path)` + `RepairResult` 추가.
  - `scan`에 트랙별 `firstPTS`/`frames` 집계 추가(Reader 비공개 맵 2개, Open 경로 무해).
  - `Open`/`Repair` 공통 셋업을 `newReaderFromFile(f)` 헬퍼로 추출.
  - `gmc/repair.go`: truncate + `encodeFooter`/`encodeTrailer` append 경로.
  - `gmc/repair_test.go`: 크래시 파일 복구 후 `Finalized()==true`·재생·seek, 멱등, 유효프레임0, 이미-정상 no-op, torn-tail, **footer가 `Finalize` 결과와 일치**(집계 정확성). (기존 `reader_recover_test.go`·`writer_finalize_test.go` 패턴 활용)
  - 버전 태그(사용자 확인 후).
- **(B) media-recorder** — go.mod의 gmc 의존 버전 상향 후 `cleanupRecording` → 복구 로직 교체.
  - 테스트: 부분 파일 시드 → 기동 → 카탈로그 complete 승격·재생, 복구불가 시 정리.

(A) 완료·태깅 후 (B) 착수. 본 문서는 (A) 담당자에게 전달할 수 있도록 자기완결형으로 작성했다.

---

## 8. 참조 (파일:줄)

**gmc 라이브러리** (`github.com/Youngju-Heo/go-container@v0.0.0-20260705021753-f08db6014cb7`, `gmc/`):
- `reader.go:32` `Open`(footer 우선, 없으면 복구 스캔) · `reader.go:85` `scan` · `reader.go:360` `Finalized`
- `writer.go:344` `Finalize`(footer append + trailer)
- `footer.go:19` `encodeFooter` · `footer.go:74` `encodeTrailer` · `format.go:16` `trailerSize=16`

**media-recorder** (본 리포):
- `internal/recorder/recorder.go:37` cleanup 호출 · `:114-125` `cleanupRecording` · `:228-238` recording INSERT · `:245-261` finalize/빈 세그먼트 삭제
- `internal/mux/gmc.go:72-74` WriteGMC(복구 가능 상태로 남긴다는 주석)
- `internal/store` — `UpdateSegment`/`DeleteSegment`(카탈로그 행만, 파일 무관)
