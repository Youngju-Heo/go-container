package gmc

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// A writer, N seekers, and M followers running concurrently must not race
// or corrupt data. This test must be run with -race.
func TestConcurrentWriteSeekFollow(t *testing.T) {
	const frames = 2000
	path := filepath.Join(t.TempDir(), "stress.gmc")
	w, err := Create(path, CreateOptions{CheckpointBytes: 4096, CheckpointInterval: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	video, _ := w.AddTrack(TrackInfo{Kind: KindVideo, Codec: "h264", TimebaseNum: 1, TimebaseDen: 90000})
	audio, _ := w.AddTrack(TrackInfo{Kind: KindAudio, Codec: "pcm_s16le", TimebaseNum: 1, TimebaseDen: 48000})

	var wg sync.WaitGroup
	followTotal := make([]int, 2)

	// followers (start before writing so they see everything)
	for m := 0; m < 2; m++ {
		r, err := w.NewReader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		ch := r.Follow(context.Background())
		wg.Add(1)
		go func(m int) {
			defer wg.Done()
			for range ch {
				followTotal[m]++
			}
		}(m)
	}

	// seekers
	stop := make(chan struct{})
	for s := 0; s < 4; s++ {
		r, err := w.NewReader()
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				it, err := r.SeekPTS(video, 3000*uint64(frames/2))
				if err != nil {
					t.Error(err)
					return
				}
				for i := 0; i < 5 && it.Next(); i++ {
					_ = it.Frame()
				}
				if it.Err() != nil {
					t.Error(it.Err())
					return
				}
			}
		}()
	}

	// writer
	for i := 0; i < frames; i++ {
		if err := w.WriteFrame(video, Frame{PTS: uint64(i * 3000), Keyframe: i%30 == 0, Data: make([]byte, 64)}); err != nil {
			t.Fatal(err)
		}
		if err := w.WriteFrame(audio, Frame{PTS: uint64(i * 1600), Keyframe: true, Data: make([]byte, 32)}); err != nil {
			t.Fatal(err)
		}
		if i%100 == 0 {
			if err := w.SetTag("progress", []byte{byte(i / 100)}); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Finalize(); err != nil {
		t.Fatal(err)
	}
	close(stop)
	wg.Wait()

	for m, n := range followTotal {
		if n != frames*2 {
			t.Fatalf("follower %d received %d frames, want %d", m, n, frames*2)
		}
	}

	// the finalized file reopens identically
	r, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	n := 0
	for range r.Follow(context.Background()) {
		n++
	}
	if n != frames*2 {
		t.Fatalf("reopened frames = %d, want %d", n, frames*2)
	}
}
