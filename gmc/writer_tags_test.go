package gmc

import (
	"bytes"
	"errors"
	"os"
	"testing"
	"time"
)

func readTagsArea(t *testing.T, path string, w *Writer) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	area := make([]byte, w.slotSize*2)
	if _, err := f.ReadAt(area, w.tagsOff); err != nil {
		t.Fatal(err)
	}
	tags, _, _ := pickTagsSlot(area)
	return tags
}

func TestSetTagWritesSlots(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{TagsAreaSize: 1024})
	defer w.Close()

	if err := w.SetTag(TagLocation, []byte("seoul")); err != nil {
		t.Fatal(err)
	}
	if got := readTagsArea(t, path, w); !bytes.Equal(got[TagLocation], []byte("seoul")) {
		t.Fatalf("tags = %v", got)
	}

	// update flips to the other slot and newer seq wins
	if err := w.SetTag(TagLocation, []byte("busan")); err != nil {
		t.Fatal(err)
	}
	if err := w.SetTag("camera.id", []byte("cam-03")); err != nil {
		t.Fatal(err)
	}
	got := readTagsArea(t, path, w)
	if !bytes.Equal(got[TagLocation], []byte("busan")) || !bytes.Equal(got["camera.id"], []byte("cam-03")) {
		t.Fatalf("tags = %v", got)
	}
}

func TestSetStartTime(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	defer w.Close()
	now := time.Unix(1751500000, 123456789)
	if err := w.SetStartTime(now); err != nil {
		t.Fatal(err)
	}
	got := readTagsArea(t, path, w)
	if len(got[TagStartTime]) != 8 {
		t.Fatalf("start tag = %v", got[TagStartTime])
	}
}

// TestTagsSnapshotIsolation pins that mutating a slice returned by
// tagsSnapshot cannot corrupt the writer's in-memory tag or leak into a
// later persisted value.
func TestTagsSnapshotIsolation(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	defer w.Close()

	if err := w.SetTag(TagLocation, []byte("seoul")); err != nil {
		t.Fatal(err)
	}
	snap := w.tagsSnapshot()
	snap[TagLocation][0] = 'X'

	if err := w.SetTag("camera.id", []byte("cam-03")); err != nil {
		t.Fatal(err)
	}
	got := readTagsArea(t, path, w)
	if !bytes.Equal(got[TagLocation], []byte("seoul")) {
		t.Fatalf("tags = %v, want location unchanged by snapshot mutation", got)
	}
}

func TestSetTagTooLarge(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{TagsAreaSize: 256}) // slot = 128 bytes
	defer w.Close()
	if err := w.SetTag("k", make([]byte, 200)); !errors.Is(err, ErrTagsTooLarge) {
		t.Fatalf("err = %v", err)
	}
	// failed SetTag must not leave partial state
	if len(w.tagsSnapshot()) != 0 {
		t.Fatal("tags map mutated by failed SetTag")
	}
}
