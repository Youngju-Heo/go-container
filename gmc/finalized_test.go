package gmc

import "testing"

func TestFinalized(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err := w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("f")}); err != nil {
		t.Fatal(err)
	}

	// live reader: always false
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	if r.Finalized() {
		t.Fatal("live reader must not be finalized")
	}
	r.Close()

	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if !r2.Finalized() {
		t.Fatal("finalized file must report true")
	}
	r2.Close()
}

func TestFinalizedFalseOnCrashedFile(t *testing.T) {
	w, path := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	if err := w.WriteFrame(video, Frame{PTS: 0, Keyframe: true, Data: []byte("f")}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil { // no footer
		t.Fatal(err)
	}
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.Finalized() {
		t.Fatal("unfinalized file must report false")
	}
}
