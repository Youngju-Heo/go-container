package main

import (
	"encoding/json"
	"testing"
)

func TestCollectMKVSample(t *testing.T) {
	const path = "../../sample/test-clip.mkv"
	cfg := Config{Header: true, Media: true, Tag: true, Index: true, File: path}
	rep, err := collectMKV(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != "mkv" {
		t.Fatalf("format = %s", rep.Format)
	}
	if rep.File.Name != "test-clip.mkv" || rep.File.Size <= 0 {
		t.Fatalf("file = %+v", rep.File)
	}
	// index is metadata-only for mkv: explicit null when selected.
	if string(rep.Index) != "null" {
		t.Fatalf("index = %s, want null", rep.Index)
	}
	// media must contain at least one track with a codecID.
	var media struct {
		Tracks []struct {
			Number  int    `json:"number"`
			Type    string `json:"type"`
			CodecID string `json:"codecID"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rep.Media, &media); err != nil {
		t.Fatal(err)
	}
	if len(media.Tracks) == 0 || media.Tracks[0].CodecID == "" {
		t.Fatalf("media = %+v", media)
	}
	// header must carry timestampScale.
	var hdr struct {
		TimestampScale uint64 `json:"timestampScale"`
	}
	if err := json.Unmarshal(rep.Header, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.TimestampScale == 0 {
		t.Fatalf("header = %+v", hdr)
	}
}

func TestCollectMKVOmitsUnselected(t *testing.T) {
	const path = "../../sample/test-clip.mkv"
	cfg := Config{Media: true, File: path} // header/tag/index off
	rep, err := collectMKV(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Header != nil || rep.Tags != nil || rep.Index != nil {
		t.Fatalf("unselected not nil: header=%s tags=%s index=%s", rep.Header, rep.Tags, rep.Index)
	}
}
