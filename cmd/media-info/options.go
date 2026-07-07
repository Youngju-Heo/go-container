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
