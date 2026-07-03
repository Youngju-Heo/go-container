package gmc

import (
	"bytes"
	"testing"
)

// GOP pattern: keyframe period 10, pts step 3000.
func writeGOPs(t *testing.T, w *Writer, video TrackID, frames int) {
	t.Helper()
	for i := 0; i < frames; i++ {
		err := w.WriteFrame(video, Frame{
			PTS: uint64(i * 3000), Keyframe: i%10 == 0, Data: []byte{byte(i)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestSeekPTSLiveReader(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	writeGOPs(t, w, video, 30)

	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	// target pts 45000 (frame 15) -> starts at keyframe pts 30000 (frame 10)
	it, err := r.SeekPTS(video, 45000)
	if err != nil {
		t.Fatal(err)
	}
	var ptss []uint64
	for it.Next() {
		ptss = append(ptss, it.Frame().PTS)
	}
	if it.Err() != nil {
		t.Fatal(it.Err())
	}
	if len(ptss) != 20 || ptss[0] != 30000 || ptss[len(ptss)-1] != 29*3000 {
		t.Fatalf("ptss = %v", ptss)
	}
	if !bytes.Equal(mustFrameAt(t, r, video, 30000).Data, []byte{10}) {
		t.Fatal("frame body mismatch")
	}

	// seek to pts 0 lands exactly on the first keyframe
	it2, err := r.SeekPTS(video, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !it2.Next() || it2.Frame().PTS != 0 {
		t.Fatalf("first frame pts = %d", it2.Frame().PTS)
	}

	if _, err := r.SeekPTS(99, 0); err == nil {
		t.Fatal("unknown track must error")
	}
	w.Close()
}

func mustFrameAt(t *testing.T, r *Reader, id TrackID, pts uint64) Frame {
	t.Helper()
	it, err := r.SeekPTS(id, pts)
	if err != nil {
		t.Fatal(err)
	}
	for it.Next() {
		if it.Frame().PTS == pts {
			return it.Frame()
		}
	}
	t.Fatalf("frame pts=%d not found", pts)
	return Frame{}
}

func TestSeekPTSOpenedFile(t *testing.T) {
	path, video, _ := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	it, err := r.SeekPTS(video, 45000) // keyframes at 0,30000,60000
	if err != nil {
		t.Fatal(err)
	}
	if !it.Next() || it.Frame().PTS != 30000 || !it.Frame().Keyframe {
		t.Fatalf("first = %+v", it.Frame())
	}
}
