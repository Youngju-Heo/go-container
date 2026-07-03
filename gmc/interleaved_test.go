package gmc

import "testing"

func TestReadInterleaved(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	meta, _ := w.AddTrack(TrackInfo{Kind: KindData, Codec: "json", TimebaseNum: 1, TimebaseDen: 1000})

	// interleave: video frames every 3000 pts (kf every 10), meta events sparse
	for i := 0; i < 30; i++ {
		w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)}})
		if i%7 == 0 {
			w.WriteFrame(meta, Frame{PTS: uint64(i * 33), Keyframe: true, Data: []byte("ev")})
		}
	}
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// all tracks: must start at the minimum offset across per-track sync points
	it, err := r.ReadInterleaved(45000)
	if err != nil {
		t.Fatal(err)
	}
	var seq []TrackID
	for it.Next() {
		seq = append(seq, it.Track())
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}
	hasVideo, hasMeta := false, false
	for _, id := range seq {
		if id == video {
			hasVideo = true
		}
		if id == meta {
			hasMeta = true
		}
	}
	if !hasVideo || !hasMeta {
		t.Fatalf("seq = %v", seq)
	}

	// track filter: only meta frames
	it2, err := r.ReadInterleaved(0, meta)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for it2.Next() {
		if it2.Track() != meta {
			t.Fatalf("unexpected track %d", it2.Track())
		}
		n++
	}
	if n != 5 { // i = 0,7,14,21,28
		t.Fatalf("meta frames = %d, want 5", n)
	}

	if _, err := r.ReadInterleaved(0, TrackID(99)); err == nil {
		t.Fatal("unknown track must error")
	}
	w.Close()
}
