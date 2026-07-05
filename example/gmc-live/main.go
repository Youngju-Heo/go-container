// Command gmc-live demonstrates reading a gmc file while it is still being written: a writer goroutine appends events every 100ms while the main goroutine follows them live.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
)

func main() {
	path := "live.gmc"
	flag.Parse()
	if flag.NArg() > 0 {
		path = flag.Arg(0)
	}

	w, err := gmc.Create(path, gmc.CreateOptions{})
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	events, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindData, Codec: "json",
		TimebaseNum: 1, TimebaseDen: 1000, // ms
	})
	if err != nil {
		log.Fatalf("add track: %v", err)
	}

	start := time.Now()
	w.SetStartTime(start)

	r, err := w.NewReader()
	if err != nil {
		log.Fatalf("new reader: %v", err)
	}
	defer r.Close()

	// Writer goroutine: one event every 100ms for 1 second, then finalize.
	go func() {
		for i := 0; i < 10; i++ {
			time.Sleep(100 * time.Millisecond)
			data, _ := json.Marshal(map[string]int{"seq": i})
			pts := uint64((i + 1) * 100)
			if err := w.WriteFrame(events, gmc.Frame{PTS: pts, Keyframe: true, Data: data}); err != nil {
				log.Fatalf("write frame: %v", err)
			}
		}
		if err := w.Finalize(); err != nil {
			log.Fatalf("finalize: %v", err)
		}
	}()

	// Consumer goroutine: prints events as they are committed, until the
	// channel closes (writer finalized/closed and all data delivered).
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for tf := range r.Follow(context.Background(), events) {
			fmt.Printf("event pts=%d data=%s\n", tf.Frame.PTS, tf.Frame.Data)
		}
		fmt.Println("follow channel closed")
	}()

	time.Sleep(1050 * time.Millisecond)
	if pts, ok := r.LastPTS(events); ok {
		fmt.Printf("last pts: %d\n", pts)
	}
	if lt, ok := r.LastTime(events); ok {
		fmt.Printf("last time: %s\n", lt.Format(time.RFC3339Nano))
	}

	wg.Wait()
}
