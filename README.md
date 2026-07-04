# GMC — Go Media Container

`gmc`는 **쓰는 도중에도 읽을 수 있는** 단순한 멀티미디어 저장 컨테이너다.
순수 Go 표준 라이브러리만으로 구현되며, 외부 의존성이 없다.

```
import "github.com/Youngju-Heo/go-container/gmc"
```

MKV/MP4가 인덱스를 파일 마지막에 몰아 써 완성 전에는 시크가 어려운 문제를,
인덱스를 스트림 중간에 주기적으로 박아 넣는(append-only + 체크포인트) 방식으로 해결한다.

## 핵심 특징

- **멀티트랙, 필수 트랙 없음** — 영상/오디오/메타데이터를 자유롭게 조합. 오디오만, 메타데이터 트랙 하나만 있는 파일도 유효.
- **쓰는 중 읽기** — 같은 프로세스 안에서 writer 1 + reader N. 라이브 테일 추적과 과거 구간 pts 시크를 파일 스캔 없이 즉시 처리.
- **크래시 복구** — append-only + 청크별 CRC-32C. 전원 손실 시 별도 도구 없이 마지막 유효 청크까지 그대로 열린다.
- **세션 메타정보(Tags)** — 녹화 시작 절대시각, 위치 등을 파일 앞쪽 고정 영역에 key-value로 저장. 녹화 도중 갱신 가능, 스캔 없이 즉시 조회(더블 슬롯으로 torn write 보호).
- **빠른 오픈** — `Finalize` 시 Footer+트레일러를 붙여 한 번의 시크로 전체 인덱스 로드. 미완성/크래시 파일은 순차 스캔으로 자동 폴백.

## 빠른 시작

```go
// 쓰기
w, _ := gmc.Create("rec.gmc", gmc.CreateOptions{Private: manifest})
video, _ := w.AddTrack(gmc.TrackInfo{
    Kind: gmc.KindVideo, Codec: "h264",
    TimebaseNum: 1, TimebaseDen: 90000,
    Private: avcConfig,
})

w.SetStartTime(time.Now())                          // gmc.start_time_unix_ns
w.SetTag(gmc.TagLocation, []byte("37.5665,126.9780"))

w.WriteFrame(video, gmc.Frame{PTS: pts, Keyframe: true, Data: nal})
// 트랙 내 pts 역행 시 ErrNonMonotonicPTS

w.Finalize()   // Footer+트레일러 기록 후 close. 또는 w.Close()로 미완성 파일로 남김

// 쓰는 중 읽기 (같은 프로세스)
r, _ := w.NewReader()
it, _ := r.SeekPTS(video, targetPTS)                // 단일 트랙 과거 시크
mux, _ := r.ReadInterleaved(targetPTS, video, audio) // 멀티트랙 통합 순회
for it.Next() {
    frame := it.Frame()
    _ = frame
}
for tf := range r.Follow(ctx, video) {              // 라이브 테일. Finalize/Close 시 EOF
    _ = tf.Frame
}

// 완성/크래시 파일 열기
r, _ := gmc.Open("rec.gmc")                         // Footer 있으면 즉시, 없으면 스캔 복구
tags := r.Tags()
start, ok := r.StartTime()
```

전체 흐름을 검증하는 실행 가능한 예제는 [`gmc/example_test.go`](gmc/example_test.go)의 `Example` 참조.

## 읽기 경로 3가지

| 시나리오 | 진입점 | 동작 |
|---|---|---|
| 쓰는 중, 같은 프로세스 (주 시나리오) | `Writer.NewReader()` | in-memory 인덱스 + committedSize 공유. 시크·테일 즉시 |
| 완성된 파일 | `gmc.Open()` | 트레일러 확인 → Footer 하나로 트랙·인덱스·요약 일괄 로드 |
| 미완성/크래시 파일 | `gmc.Open()` | 트레일러 없음 → 앞에서부터 CRC 전수 검증 스캔으로 인덱스 재구성 |

시크는 2단계다: ① 트랙별 pts 인덱스 이진 탐색으로 목표 이하 마지막 sync point를 찾고,
② 그 오프셋부터 전방 스캔으로 정확한 프레임까지 이동한다.

## 동시성 모델

- **Writer는 1개** (내부 mutex로 직렬화 — 여러 고루틴이 `WriteFrame`을 불러도 안전).
- **Reader는 N개** — `os.File.ReadAt`(pread)로 fd 공유, committedSize 이전만 읽어 torn 청크를 보지 않는다.
- 인덱스는 RWMutex, 라이브 테일 대기는 `sync.Cond`로 공유.
- 보장 범위는 **같은 Go 프로세스 내부**. 프로세스 간/네트워크 FS 동시접근은 포맷이 방해하진 않으나 SDK가 보장하지 않는다.

## 비목표 (v1)

- 기록된 데이터의 수정/삭제 (append-only)
- 코덱 처리 — 컨테이너는 압축된 프레임을 바이트로만 취급
- ffmpeg 등 기존 도구의 직접 재생, MP4/MKV export
- B-frame(pts≠dts) 재정렬, 크래시 파일 이어쓰기 — 향후 확장 여지만 확보

## 문서

- 포맷 설계: [`docs/feature/2026-07-03-01-gmc-container-design.md`](docs/feature/2026-07-03-01-gmc-container-design.md)
- 구현 계획: [`docs/feature/2026-07-03-01-gmc-container-plan.md`](docs/feature/2026-07-03-01-gmc-container-plan.md)

## 요구사항

Go 1.22+ (표준 라이브러리만). 테스트: `go test ./gmc/` (동시성 검증은 `-race` 지원 플랫폼에서 `go test -race ./gmc/`).

## 서브패키지

- `gmc/codec` — Matroska 관례를 따르는 코덱 규약 계층: CodecID 상수(H.264/HEVC/PCM/Opus/AAC/FLAC/UTF-8 텍스트), Private envelope, Annex-B 변환 헬퍼.
- `mkv` — MKV(Matroska) import/export. 전체 또는 구간(`Range`) 변환, 순수 표준 라이브러리 EBML 구현.

```go
res, err := mkv.Import("in.mkv", "out.gmc", mkv.ImportOptions{})
res, err = mkv.Export("out.gmc", "again.mkv", mkv.ExportOptions{
    Range: mkv.Range{From: 10 * time.Second, To: 20 * time.Second},
})
```

## 라이선스

Apache License 2.0 — [`LICENSE`](LICENSE) 참조.
