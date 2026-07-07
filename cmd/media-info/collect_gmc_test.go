package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Youngju-Heo/go-container/gmc"
)

func buildGMC(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "c.gmc")
	w, err := gmc.Create(path, gmc.CreateOptions{Private: []byte("mf")})
	if err != nil {
		t.Fatal(err)
	}
	id, err := w.AddTrack(gmc.TrackInfo{Kind: gmc.KindVideo, Codec: "V_TEST", TimebaseNum: 1, TimebaseDen: 1000})
	if err != nil {
		t.Fatal(err)
	}
	if err := w.SetTag("title", []byte("My GMC")); err != nil {
		t.Fatal(err)
	}
	for i, pts := range []uint64{0, 10, 20} {
		if err := w.WriteFrame(id, gmc.Frame{PTS: pts, Keyframe: i%2 == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCollectGMCAllSections(t *testing.T) {
	path := buildGMC(t)
	cfg := Config{Header: true, Media: true, Tag: true, Index: true, File: path}
	rep, err := collectGMC(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Format != "gmc" {
		t.Fatalf("format = %s", rep.Format)
	}
	if rep.Title == nil || *rep.Title != "My GMC" {
		t.Fatalf("title = %v", rep.Title)
	}
	fi, _ := os.Stat(path)
	if rep.File.Size != fi.Size() || rep.File.Name != filepath.Base(path) {
		t.Fatalf("file = %+v", rep.File)
	}
	// index frames should be present and equal to 3 for the single track.
	var idx struct {
		SyncPoints int `json:"syncPoints"`
		Tracks     []struct {
			Frames *uint64 `json:"frames"`
		} `json:"tracks"`
	}
	if err := json.Unmarshal(rep.Index, &idx); err != nil {
		t.Fatalf("index unmarshal: %v (%s)", err, rep.Index)
	}
	if len(idx.Tracks) != 1 || idx.Tracks[0].Frames == nil || *idx.Tracks[0].Frames != 3 {
		t.Fatalf("index = %+v", idx)
	}
	// header must carry version and trackCount.
	var hdr struct {
		Version    int  `json:"version"`
		Finalized  bool `json:"finalized"`
		TrackCount int  `json:"trackCount"`
	}
	if err := json.Unmarshal(rep.Header, &hdr); err != nil {
		t.Fatal(err)
	}
	if hdr.Version != 1 || !hdr.Finalized || hdr.TrackCount != 1 {
		t.Fatalf("header = %+v", hdr)
	}
}

func TestCollectGMCOmitsUnselected(t *testing.T) {
	path := buildGMC(t)
	cfg := Config{Header: true, File: path} // media/tag/index off
	rep, err := collectGMC(path, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Media != nil || rep.Tags != nil || rep.Index != nil {
		t.Fatalf("unselected sections not nil: media=%s tags=%s index=%s", rep.Media, rep.Tags, rep.Index)
	}
	if rep.Header == nil {
		t.Fatal("header should be present")
	}
}
