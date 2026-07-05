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

// rawElem is a decoded EBML element (id + raw payload) used to inspect the
// SeekHead structure directly, independent of the demuxer's own logic.
type rawElem struct {
	id      uint32
	payload []byte
}

// decodeElemID reads a leading EBML element ID (with marker bits) from b,
// mirroring the ID-length detection in ebmlReader.readElement.
func decodeElemID(b []byte) (id uint32, idLen int) {
	if len(b) == 0 {
		return 0, 0
	}
	idLen = 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		idLen++
	}
	for i := 0; i < idLen; i++ {
		id = id<<8 | uint32(b[i])
	}
	return id, idLen
}

// parseElems splits a flat EBML payload into its immediate child elements.
func parseElems(b []byte) []rawElem {
	var out []rawElem
	for len(b) > 0 {
		id, idLen := decodeElemID(b)
		sz, szLen := readVintAt(b[idLen:])
		start := idLen + szLen
		end := start + int(sz)
		out = append(out, rawElem{id: id, payload: b[start:end]})
		b = b[end:]
	}
	return out
}

// TestMuxerAudioOnlyNoCuesSeekEntry covers an audio-only export (GMC has no
// mandatory video track): Finalize must not emit a SeekHead entry pointing
// at Cues when no cues were ever written (m.cues stays empty because cue
// points are only recorded for video keyframes).
func TestMuxerAudioOnlyNoCuesSeekEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audio.mkv")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	tracks := []TrackEntry{
		{Number: 1, Type: trackTypeAudio, CodecID: "A_FLAC", SamplingFrequency: 48000, Channels: 2},
	}
	m := NewMuxer(f, 1000000)
	if err := m.WriteHeader(FileInfo{}, tracks, nil); err != nil {
		t.Fatal(err)
	}
	pkts := []Packet{
		{Track: 1, Timestamp: 0, Keyframe: true, Data: []byte("a0")},
		{Track: 1, Timestamp: 20, Data: []byte("a1")},
		{Track: 1, Timestamp: 40, Data: []byte("a2")},
	}
	for _, p := range pkts {
		if err := m.WritePacket(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := m.Finalize(40); err != nil {
		t.Fatal(err)
	}
	f.Close()

	rf, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer rf.Close()
	fi, err := rf.Stat()
	if err != nil {
		t.Fatal(err)
	}

	// Walk the file's own EBML structure to find the SeekHead directly,
	// independent of the demuxer (which never reads SeekHead itself).
	er := newEBMLReader(rf, fi.Size())
	id, sz, unknown, err := er.readElement() // EBML header
	if err != nil || id != idEBML || unknown {
		t.Fatalf("expected EBML header, got id=%x unknown=%v err=%v", id, unknown, err)
	}
	er.skip(sz)
	id, segSz, unknown, err := er.readElement()
	if err != nil || id != idSegment || unknown {
		t.Fatalf("expected known-size Segment, got id=%x unknown=%v err=%v", id, unknown, err)
	}
	segEnd := er.pos() + segSz

	var seekEntries []rawElem
	for er.pos() < segEnd {
		cid, csz, cunknown, err := er.readElement()
		if err != nil {
			t.Fatal(err)
		}
		if cunknown {
			t.Fatalf("unexpected unknown-size top-level element %x", cid)
		}
		payload, err := er.readBytes(csz)
		if err != nil {
			t.Fatal(err)
		}
		if cid == idSeekHead {
			seekEntries = parseElems(payload)
			break
		}
	}
	if len(seekEntries) == 0 {
		t.Fatal("SeekHead not found or empty")
	}

	var sawInfo, sawTracks, sawCues bool
	for _, se := range seekEntries {
		if se.id != idSeek {
			t.Fatalf("unexpected child of SeekHead: %x", se.id)
		}
		for _, sub := range parseElems(se.payload) {
			if sub.id != idSeekID {
				continue
			}
			targetID, _ := decodeElemID(sub.payload)
			switch targetID {
			case idInfo:
				sawInfo = true
			case idTracks:
				sawTracks = true
			case idCues:
				sawCues = true
			}
		}
	}
	if !sawInfo || !sawTracks {
		t.Fatalf("expected SeekHead entries for Info and Tracks, got info=%v tracks=%v", sawInfo, sawTracks)
	}
	if sawCues {
		t.Fatal("SeekHead must not reference Cues when no cues were written (audio-only export)")
	}

	// Regression: the file must still demux cleanly (no Void/patch layout
	// breakage from a conditional SeekHead).
	if _, err := rf.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	d, err := NewDemuxer(rf, fi.Size())
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Tracks()) != 1 {
		t.Fatalf("tracks = %d, want 1", len(d.Tracks()))
	}
	var gotPkts []*Packet
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		gotPkts = append(gotPkts, p)
	}
	if len(gotPkts) != len(pkts) {
		t.Fatalf("packets = %d, want %d", len(gotPkts), len(pkts))
	}
}
