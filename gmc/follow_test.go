package gmc

import (
	"context"
	"testing"
	"time"
)

func TestFollowReceivesFramesAndClosesOnFinalize(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	ch := r.Follow(context.Background(), video)
	done := make(chan []uint64)
	go func() {
		var got []uint64
		for tf := range ch {
			got = append(got, tf.Frame.PTS)
		}
		done <- got
	}()

	for i := 0; i < 10; i++ {
		if err := w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i == 0, Data: []byte{byte(i)}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}

	select {
	case got := <-done:
		if len(got) != 10 || got[0] != 0 || got[9] != 27000 {
			t.Fatalf("got = %v", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("follower did not finish after Finalize")
	}
}

func TestFollowCancel(t *testing.T) {
	w, _ := newTestWriter(t, CreateOptions{})
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	r, err := w.NewReader()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	ctx, cancel := context.WithCancel(context.Background())
	ch := r.Follow(ctx, video)
	cancel()

	deadline := time.After(5 * time.Second)
	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return // channel closed after cancel
			}
		case <-deadline:
			t.Fatal("follow channel did not close after cancel")
		}
	}
}

func TestFollowOnOpenedFileDrainsAndCloses(t *testing.T) {
	path, video, _ := buildFinalizedFile(t)
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for range r.Follow(context.Background(), video) {
		n++
	}
	if n != 30 {
		t.Fatalf("frames = %d, want 30", n)
	}
}
