package mkv

import (
	"bytes"
	"errors"
	"testing"
)

// buildTestMKV assembles a minimal in-memory MKV using the task-3 writer
// primitives: video (V_MPEG4/ISO/AVC 1280x720) + audio (A_FLAC 48kHz/2ch),
// one tag, and two clusters written by the caller-provided payload.
func buildTestMKV(t *testing.T, clusters []byte) []byte {
	t.Helper()
	var ebml []byte
	ebml = appendUintElement(ebml, idEBMLVersion, 1)
	ebml = appendUintElement(ebml, idEBMLReadVersion, 1)
	ebml = appendUintElement(ebml, idEBMLMaxIDLength, 4)
	ebml = appendUintElement(ebml, idEBMLMaxSizeLength, 8)
	ebml = appendStringElement(ebml, idDocType, "matroska")
	ebml = appendUintElement(ebml, idDocTypeVersion, 4)
	ebml = appendUintElement(ebml, idDocTypeReadVer, 2)

	var info []byte
	info = appendUintElement(info, idTimestampScale, 1000000)
	info = appendFloatElement(info, idDuration, 5000) // 5s in scale units
	info = appendUintElement(info, idDateUTC, 0)      // placeholder; parsed as signed
	info = appendStringElement(info, idTitle, "demo-title")

	var video []byte
	video = appendUintElement(video, idPixelWidth, 1280)
	video = appendUintElement(video, idPixelHeight, 720)
	var te1 []byte
	te1 = appendUintElement(te1, idTrackNumber, 1)
	te1 = appendUintElement(te1, idTrackType, trackTypeVideo)
	te1 = appendStringElement(te1, idCodecID, "V_MPEG4/ISO/AVC")
	te1 = appendElement(te1, idCodecPrivate, []byte{1, 0x64, 0, 31})
	te1 = appendElement(te1, idVideo, video)

	var audio []byte
	audio = appendFloatElement(audio, idSamplingFreq, 48000)
	audio = appendUintElement(audio, idChannels, 2)
	var te2 []byte
	te2 = appendUintElement(te2, idTrackNumber, 2)
	te2 = appendUintElement(te2, idTrackType, trackTypeAudio)
	te2 = appendStringElement(te2, idCodecID, "A_FLAC")
	te2 = appendUintElement(te2, idDefaultDuration, 20000000) // 20ms in ns
	te2 = appendElement(te2, idAudio, audio)

	var te3 []byte
	te3 = appendUintElement(te3, idTrackNumber, 3)
	te3 = appendUintElement(te3, idTrackType, trackTypeSubtitle)
	te3 = appendStringElement(te3, idCodecID, "S_TEXT/UTF8")

	var tracks []byte
	tracks = appendElement(tracks, idTrackEntry, te1)
	tracks = appendElement(tracks, idTrackEntry, te2)
	tracks = appendElement(tracks, idTrackEntry, te3)

	var st []byte
	st = appendStringElement(st, idTagName, "TITLE")
	st = appendStringElement(st, idTagString, "demo")
	var tag []byte
	tag = appendElement(tag, idTargets, nil)
	tag = appendElement(tag, idSimpleTag, st)
	var tags []byte
	tags = appendElement(tags, idTag, tag)

	var seg []byte
	seg = appendElement(seg, idInfo, info)
	seg = appendElement(seg, idTracks, tracks)
	seg = appendElement(seg, idTags, tags)
	seg = append(seg, clusters...)

	var out []byte
	out = appendElement(out, idEBML, ebml)
	out = appendElement(out, idSegment, seg)
	return out
}

func TestDemuxerHeaderInfoTracks(t *testing.T) {
	data := buildTestMKV(t, nil)
	d, err := NewDemuxer(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if d.Info().TimestampScale != 1000000 || d.Info().Duration != 5000 || !d.Info().HasDate {
		t.Fatalf("info = %+v", d.Info())
	}
	if d.Info().Title != "demo-title" {
		t.Fatalf("title = %q", d.Info().Title)
	}
	trs := d.Tracks()
	if len(trs) != 3 {
		t.Fatalf("tracks = %d", len(trs))
	}
	v := trs[0]
	if v.Number != 1 || v.Type != trackTypeVideo || v.CodecID != "V_MPEG4/ISO/AVC" ||
		v.PixelWidth != 1280 || v.PixelHeight != 720 || !bytes.Equal(v.CodecPrivate, []byte{1, 0x64, 0, 31}) {
		t.Fatalf("video = %+v", v)
	}
	a := trs[1]
	if a.Number != 2 || a.SamplingFrequency != 48000 || a.Channels != 2 || a.DefaultDuration != 20000000 {
		t.Fatalf("audio = %+v", a)
	}
	s := trs[2]
	if s.Number != 3 || s.Type != trackTypeSubtitle || s.CodecID != "S_TEXT/UTF8" {
		t.Fatalf("subtitle = %+v", s)
	}
	if d.Tags()["TITLE"] != "demo" {
		t.Fatalf("tags = %v", d.Tags())
	}
}

func TestDemuxerRejectsNonMatroska(t *testing.T) {
	var ebml []byte
	ebml = appendStringElement(ebml, idDocType, "webm")
	var out []byte
	out = appendElement(out, idEBML, ebml)
	out = appendElement(out, idSegment, nil)
	if _, err := NewDemuxer(bytes.NewReader(out), int64(len(out))); !errors.Is(err, ErrNotMatroska) {
		t.Fatalf("err = %v", err)
	}
	if _, err := NewDemuxer(bytes.NewReader([]byte{1, 2, 3, 4}), 4); err == nil {
		t.Fatal("garbage accepted")
	}
}
