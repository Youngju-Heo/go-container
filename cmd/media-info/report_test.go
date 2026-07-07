package main

import (
	"encoding/json"
	"testing"
)

func TestReportMarshalOmitsAndNulls(t *testing.T) {
	title := "Clip"
	r := &Report{
		File:   FileRef{Path: "a.mkv", Name: "a.mkv", Size: 42},
		Format: "mkv",
		Title:  &title,
		Header: json.RawMessage(`{"timestampScale":1000000}`),
		Index:  jsonNull, // selected but no value
		// Media and Tags left nil -> omitted
	}
	data, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(data) {
		t.Fatalf("invalid json: %s", data)
	}

	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if _, ok := m["media"]; ok {
		t.Fatal("media should be omitted")
	}
	if _, ok := m["tags"]; ok {
		t.Fatal("tags should be omitted")
	}
	if v, ok := m["index"]; !ok || string(v) != "null" {
		t.Fatalf("index = %s (ok=%v), want explicit null", v, ok)
	}
	if v, ok := m["title"]; !ok || string(v) != `"Clip"` {
		t.Fatalf("title = %s", v)
	}
}

func TestReportMarshalNullTitle(t *testing.T) {
	r := &Report{File: FileRef{Path: "a.gmc", Name: "a.gmc", Size: 1}, Format: "gmc"}
	data, err := r.marshal()
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if v, ok := m["title"]; !ok || string(v) != "null" {
		t.Fatalf("title = %s (ok=%v), want null", v, ok)
	}
}
