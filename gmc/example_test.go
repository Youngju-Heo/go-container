package gmc_test

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
)

// Example demonstrates the full lifecycle: create a file, register tracks,
// write frames, attach session tags, finalize, then reopen and seek.
func Example() {
	path := filepath.Join(os.TempDir(), "gmc_example.gmc")
	defer os.Remove(path)

	// Write side.
	w, err := gmc.Create(path, gmc.CreateOptions{Private: []byte("manifest")})
	if err != nil {
		panic(err)
	}

	video, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindVideo, Codec: "h264",
		TimebaseNum: 1, TimebaseDen: 90000,
	})
	if err != nil {
		panic(err)
	}

	// Session metadata, written to the fixed tags area at the front of the file.
	w.SetStartTime(time.Unix(0, 0).UTC())
	w.SetTag(gmc.TagLocation, []byte("37.5665,126.9780"))

	// A short GOP: keyframe every 3rd frame, pts stepping by 3000.
	for i := 0; i < 9; i++ {
		err := w.WriteFrame(video, gmc.Frame{
			PTS: uint64(i * 3000), Keyframe: i%3 == 0, Data: []byte{byte(i)},
		})
		if err != nil {
			panic(err)
		}
	}

	// Finalize writes a footer + trailer so the file reopens without a scan.
	if err := w.Finalize(); err != nil {
		panic(err)
	}

	// Read side: reopen the finalized file.
	r, err := gmc.Open(path)
	if err != nil {
		panic(err)
	}
	defer r.Close()

	loc := r.Tags()[gmc.TagLocation]
	fmt.Printf("location: %s\n", loc)

	// Seek to pts 15000 (frame 5): lands on the previous keyframe (frame 3, pts 9000).
	it, err := r.SeekPTS(video, 15000)
	if err != nil {
		panic(err)
	}
	var ptss []uint64
	for it.Next() {
		ptss = append(ptss, it.Frame().PTS)
	}
	if it.Err() != nil {
		panic(it.Err())
	}
	fmt.Printf("seek(15000) starts at pts: %d\n", ptss[0])
	fmt.Printf("frames from there: %d\n", len(ptss))

	// Output:
	// location: 37.5665,126.9780
	// seek(15000) starts at pts: 9000
	// frames from there: 6
}
