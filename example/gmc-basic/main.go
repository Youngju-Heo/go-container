// Command gmc-basic demonstrates the core gmc write/finalize/reopen/seek flow with a real, playable PCM audio track.
package main

import (
	"encoding/binary"
	"flag"
	"fmt"
	"log"
	"math"
	"time"

	"github.com/Youngju-Heo/go-container/gmc"
	"github.com/Youngju-Heo/go-container/gmc/codec"
)

const (
	sampleRate      = 8000
	samplesPerFrame = 400 // 50ms
	toneHz          = 440.0
	toneSeconds     = 2
)

func main() {
	path := "basic.gmc"
	flag.Parse()
	if flag.NArg() > 0 {
		path = flag.Arg(0)
	}

	// A small checkpoint threshold keeps audio sync points dense even though
	// this whole file is written in well under a second of wall-clock time,
	// so the SeekTime demo below actually lands near the 1s mark.
	w, err := gmc.Create(path, gmc.CreateOptions{CheckpointBytes: 4096})
	if err != nil {
		log.Fatalf("create: %v", err)
	}

	audio, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindAudio, Codec: codec.CodecPCM,
		TimebaseNum: 1, TimebaseDen: sampleRate,
		Private: codec.EncodeAudioPrivate(codec.AudioParams{
			SampleRate: sampleRate, Channels: 1, BitDepth: 16,
		}, nil),
	})
	if err != nil {
		log.Fatalf("add audio track: %v", err)
	}

	subs, err := w.AddTrack(gmc.TrackInfo{
		Kind: gmc.KindData, Codec: codec.CodecTextUTF8,
		TimebaseNum: 1, TimebaseDen: 1000,
		Private: codec.EncodeTextPrivate(nil),
	})
	if err != nil {
		log.Fatalf("add subtitle track: %v", err)
	}

	start := time.Now()
	w.SetStartTime(start)
	w.SetTag(gmc.TagLocation, []byte("37.5665,126.9780"))

	totalSamples := sampleRate * toneSeconds
	for off := 0; off < totalSamples; off += samplesPerFrame {
		data := make([]byte, samplesPerFrame*2)
		for i := 0; i < samplesPerFrame; i++ {
			t := float64(off+i) / sampleRate
			v := int16(3000 * math.Sin(2*math.Pi*toneHz*t))
			binary.LittleEndian.PutUint16(data[i*2:], uint16(v))
		}
		if err := w.WriteFrame(audio, gmc.Frame{PTS: uint64(off), Keyframe: true, Data: data}); err != nil {
			log.Fatalf("write audio frame: %v", err)
		}
	}

	if err := w.WriteFrame(subs, gmc.Frame{PTS: 0, Keyframe: true, Data: codec.EncodeTextFrame(1000, "hello")}); err != nil {
		log.Fatalf("write subtitle frame: %v", err)
	}
	if err := w.WriteFrame(subs, gmc.Frame{PTS: 1000, Keyframe: true, Data: codec.EncodeTextFrame(1000, "world")}); err != nil {
		log.Fatalf("write subtitle frame: %v", err)
	}

	if err := w.Finalize(); err != nil {
		log.Fatalf("finalize: %v", err)
	}

	r, err := gmc.Open(path)
	if err != nil {
		log.Fatalf("open: %v", err)
	}
	defer r.Close()

	fmt.Printf("finalized: %v\n", r.Finalized())
	for _, tr := range r.Tracks() {
		fmt.Printf("track %d: kind=%d codec=%s timebase=%d/%d\n", tr.ID, tr.Kind, tr.Codec, tr.TimebaseNum, tr.TimebaseDen)
	}
	fmt.Printf("tags: %v\n", r.Tags())
	if st, ok := r.StartTime(); ok {
		fmt.Printf("start time: %s\n", st.Format(time.RFC3339))
	}
	if lt, ok := r.LastTime(audio); ok {
		fmt.Printf("audio last time: %s\n", lt.Format(time.RFC3339Nano))
	}

	it, err := r.SeekTime(start.Add(time.Second))
	if err != nil {
		log.Fatalf("seek time: %v", err)
	}
	fmt.Println("frames from 1s mark:")
	for n := 0; n < 5 && it.Next(); n++ {
		fmt.Printf("  track=%d pts=%d\n", it.Track(), it.Frame().PTS)
	}
	if err := it.Err(); err != nil {
		log.Fatalf("iterate: %v", err)
	}
}
