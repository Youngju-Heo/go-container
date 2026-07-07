package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDetectFormatByMagic(t *testing.T) {
	if f, err := detectFormat("../../sample/test-clip.mkv"); err != nil || f != "mkv" {
		t.Fatalf("mkv detect = %q, %v", f, err)
	}
	if f, err := detectFormat("../../sample/test-clip-000.gmc"); err != nil || f != "gmc" {
		t.Fatalf("gmc detect = %q, %v", f, err)
	}
}

func TestDetectFormatMismatch(t *testing.T) {
	// .gmc extension but MKV content -> error.
	bad := filepath.Join(t.TempDir(), "fake.gmc")
	src, err := os.ReadFile("../../sample/test-clip.mkv")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, src, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := detectFormat(bad); err == nil {
		t.Fatal("expected magic mismatch error")
	}
}

func TestDetectFormatUnknownExt(t *testing.T) {
	if _, err := detectFormat("movie.avi"); err == nil {
		t.Fatal("expected unknown extension error")
	}
}

func TestRunMKVToStdout(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"../../sample/test-clip.mkv"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if !json.Valid(out.Bytes()) {
		t.Fatalf("invalid json: %s", out.String())
	}
}

func TestRunOutputFile(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "o.json")
	var out, errb bytes.Buffer
	code := run([]string{"--output", dst, "../../sample/test-clip-000.gmc"}, &out, &errb)
	if code != 0 {
		t.Fatalf("code = %d, stderr = %s", code, errb.String())
	}
	if out.Len() != 0 {
		t.Fatalf("stdout should be empty when --output given: %s", out.String())
	}
	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid json file: %s", data)
	}
}

func TestRunHelp(t *testing.T) {
	var out, errb bytes.Buffer
	code := run([]string{"--help"}, &out, &errb)
	if code != 0 {
		t.Fatalf("help code = %d", code)
	}
	if !strings.Contains(out.String()+errb.String(), "media-info") {
		t.Fatal("usage text missing")
	}
}

func TestRunBadArgs(t *testing.T) {
	var out, errb bytes.Buffer
	if code := run([]string{}, &out, &errb); code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}
