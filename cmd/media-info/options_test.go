package main

import (
	"errors"
	"testing"
)

func TestParseArgsDefaults(t *testing.T) {
	cfg, err := parseArgs([]string{"a.gmc"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Header || !cfg.Media || !cfg.Tag {
		t.Fatalf("default sections off: %+v", cfg)
	}
	if cfg.Index {
		t.Fatal("index should default off")
	}
	if cfg.File != "a.gmc" || cfg.Output != "" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsInfoAll(t *testing.T) {
	cfg, err := parseArgs([]string{"--info-all", "--info-header", "no", "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Header || !cfg.Media || !cfg.Tag || !cfg.Index {
		t.Fatalf("info-all must force all on: %+v", cfg)
	}
}

func TestParseArgsExplicitNo(t *testing.T) {
	cfg, err := parseArgs([]string{"--info-media", "no", "--info-index", "yes", "--output", "o.json", "a.mkv"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Media || !cfg.Index || cfg.Output != "o.json" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func TestParseArgsHelp(t *testing.T) {
	if _, err := parseArgs([]string{"--help"}); !errors.Is(err, errHelp) {
		t.Fatalf("err = %v, want errHelp", err)
	}
}

func TestParseArgsBadValue(t *testing.T) {
	if _, err := parseArgs([]string{"--info-header", "maybe", "a.gmc"}); err == nil {
		t.Fatal("expected error for non yes/no value")
	}
}

func TestParseArgsArgCount(t *testing.T) {
	if _, err := parseArgs([]string{}); err == nil {
		t.Fatal("expected error for zero files")
	}
	if _, err := parseArgs([]string{"a.gmc", "b.mkv"}); err == nil {
		t.Fatal("expected error for two files")
	}
}
