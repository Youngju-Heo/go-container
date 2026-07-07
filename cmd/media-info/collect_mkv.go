package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/Youngju-Heo/go-container/mkv"
)

// matroskaEpoch is the Matroska DateUTC reference: 2001-01-01T00:00:00 UTC.
var matroskaEpoch = time.Date(2001, 1, 1, 0, 0, 0, 0, time.UTC)

type mkvHeaderJSON struct {
	TimestampScale uint64  `json:"timestampScale"`
	Duration       float64 `json:"duration"`
	DateUTC        *string `json:"dateUTC"`
}

type mkvTrackJSON struct {
	Number          int     `json:"number"`
	Type            string  `json:"type"`
	CodecID         string  `json:"codecID"`
	PixelWidth      uint64  `json:"pixelWidth,omitempty"`
	PixelHeight     uint64  `json:"pixelHeight,omitempty"`
	SamplingFreq    float64 `json:"samplingFrequency,omitempty"`
	Channels        uint64  `json:"channels,omitempty"`
	BitDepth        uint64  `json:"bitDepth,omitempty"`
	DefaultDuration uint64  `json:"defaultDuration,omitempty"`
	CodecPrivateLen int     `json:"codecPrivateLen"`
}

type mkvMediaJSON struct {
	Tracks []mkvTrackJSON `json:"tracks"`
}

func mkvTypeName(t uint8) string {
	switch t {
	case 1:
		return "video"
	case 2:
		return "audio"
	case 17:
		return "subtitle"
	default:
		return "unknown"
	}
}

func collectMKV(path string, cfg Config) (*Report, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	d, err := mkv.NewDemuxer(f, fi.Size())
	if err != nil {
		return nil, err
	}
	info := d.Info()

	rep := &Report{
		File:   FileRef{Path: path, Name: filepath.Base(path), Size: fi.Size()},
		Format: "mkv",
	}
	if info.Title != "" {
		s := info.Title
		rep.Title = &s
	}

	if cfg.Header {
		hdr := mkvHeaderJSON{TimestampScale: info.TimestampScale, Duration: info.Duration}
		if info.HasDate {
			s := matroskaEpoch.Add(time.Duration(info.DateUTC)).UTC().Format(time.RFC3339)
			hdr.DateUTC = &s
		}
		b, err := json.Marshal(hdr)
		if err != nil {
			return nil, err
		}
		rep.Header = b
	}

	if cfg.Media {
		var m mkvMediaJSON
		for _, te := range d.Tracks() {
			m.Tracks = append(m.Tracks, mkvTrackJSON{
				Number:          int(te.Number),
				Type:            mkvTypeName(te.Type),
				CodecID:         te.CodecID,
				PixelWidth:      te.PixelWidth,
				PixelHeight:     te.PixelHeight,
				SamplingFreq:    te.SamplingFrequency,
				Channels:        te.Channels,
				BitDepth:        te.BitDepth,
				DefaultDuration: te.DefaultDuration,
				CodecPrivateLen: len(te.CodecPrivate),
			})
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		rep.Media = b
	}

	if cfg.Tag {
		b, err := json.Marshal(d.Tags())
		if err != nil {
			return nil, err
		}
		rep.Tags = b
	}

	if cfg.Index {
		// mkv index summary requires a full packet scan, which the
		// metadata-only policy forbids; report explicit null.
		rep.Index = jsonNull
	}

	return rep, nil
}
