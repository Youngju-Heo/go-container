package mkv

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestMuxerRoundtripViaDemuxer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []TrackEntry{
		{Number: 1, Type: trackTypeVideo, CodecID: "V_MPEG4/ISO/AVC", CodecPrivate: []byte{1, 2}, PixelWidth: 640, PixelHeight: 480},
		{Number: 2, Type: trackTypeAudio, CodecID: "A_FLAC", SamplingFrequency: 48000, Channels: 2},
		{Number: 3, Type: trackTypeSubtitle, CodecID: "S_TEXT/UTF8"},
	}
	m := NewMuxer(f, 1000000)
	info := FileInfo{DateUTC: 789 * 1_000_000_000, HasDate: true}
	if err := m.WriteHeader(info, tracks, map[string]string{"TITLE": "demo"}); err != nil {
		t.Fatal(err)
	}
	pkts := []Packet{
		{Track: 1, Timestamp: 0, Keyframe: true, Data: []byte("kf0")},
		{Track: 2, Timestamp: 0, Keyframe: true, Data: []byte("a0")},
		{Track: 3, Timestamp: 5, Keyframe: true, Duration: 1500, Data: []byte("sub")},
		{Track: 1, Timestamp: 40, Data: []byte("p1")},
		{Track: 1, Timestamp: 80, Keyframe: true, Data: []byte("kf1")}, // new cluster
		{Track: 2, Timestamp: 90, Keyframe: true, Data: []byte("a1")},
	}
	for _, p := range pkts {
		if err := m.WritePacket(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Finalize(90); err != nil {
		t.Fatal(err)
	}
	f.Close()

	// reopen through our own demuxer
	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	fi, _ := rf.Stat()
	d, err := NewDemuxer(rf, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	if d.Info().TimestampScale != 1000000 || !d.Info().HasDate || d.Info().DateUTC != 789*1_000_000_000 {
		t.Fatalf("info = %+v", d.Info())
	}
	if d.Info().Duration != 90 {
		t.Fatalf("duration = %v", d.Info().Duration)
	}
	trs := d.Tracks()
	if len(trs) != 3 || trs[0].PixelWidth != 640 || trs[1].SamplingFrequency != 48000 || trs[2].CodecID != "S_TEXT/UTF8" {
		t.Fatalf("tracks = %+v", trs)
	}
	if d.Tags()["TITLE"] != "demo" {
		t.Fatalf("tags = %v", d.Tags())
	}
	var got []*Packet
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, p)
	}
	if len(got) != len(pkts) {
		t.Fatalf("packets = %d, want %d", len(got), len(pkts))
	}
	for i, want := range pkts {
		g := got[i]
		if g.Track != want.Track || g.Timestamp != want.Timestamp || g.Keyframe != want.Keyframe || !bytes.Equal(g.Data, want.Data) {
			t.Fatalf("pkt %d = %+v, want %+v", i, g, want)
		}
	}
	if got[2].Duration != 1500 {
		t.Fatalf("subtitle duration = %d", got[2].Duration)
	}
}
