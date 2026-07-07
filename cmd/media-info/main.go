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
