package main

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/Youngju-Heo/go-container/gmc"
)

type gmcHeaderJSON struct {
	Version    int     `json:"version"`
	Finalized  bool    `json:"finalized"`
	StartTime  *string `json:"startTime"`
	PrivateLen int     `json:"privateLen"`
	TrackCount int     `json:"trackCount"`
}

type gmcTimebase struct {
	Num uint32 `json:"num"`
	Den uint32 `json:"den"`
}

type gmcTrackJSON struct {
	ID         int         `json:"id"`
	Kind       string      `json:"kind"`
	Codec      string      `json:"codec"`
	Timebase   gmcTimebase `json:"timebase"`
	Reordered  bool        `json:"reordered"`
	PrivateLen int         `json:"privateLen"`
	LastPTS    *uint64     `json:"lastPTS"`
}

type gmcMediaJSON struct {
	Tracks []gmcTrackJSON `json:"tracks"`
}

type gmcIndexTrackJSON struct {
	ID       int     `json:"id"`
	FirstPTS *uint64 `json:"firstPTS"`
	LastPTS  *uint64 `json:"lastPTS"`
	Frames   *uint64 `json:"frames"`
}

type gmcIndexJSON struct {
	SyncPoints int                 `json:"syncPoints"`
	Tracks     []gmcIndexTrackJSON `json:"tracks"`
}

func gmcKindName(k gmc.TrackKind) string {
	switch k {
	case gmc.KindVideo:
		return "video"
	case gmc.KindAudio:
		return "audio"
	case gmc.KindData:
		return "data"
	default:
		return "unknown"
	}
}

// tagValueString renders a raw tag value: UTF-8 text as-is, otherwise a
// hex-encoded string with a "hex:" marker.
func tagValueString(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return "hex:" + hex.EncodeToString(b)
}

func collectGMC(path string, cfg Config) (*Report, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	r, err := gmc.Open(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	rep := &Report{
		File:   FileRef{Path: path, Name: filepath.Base(path), Size: fi.Size()},
		Format: "gmc",
	}

	tags := r.Tags()
	if v, ok := tags["title"]; ok {
		s := tagValueString(v)
		rep.Title = &s
	}

	if cfg.Header {
		hdr := gmcHeaderJSON{
			Version:    gmc.Version,
			Finalized:  r.Finalized(),
			PrivateLen: len(r.FilePrivate()),
			TrackCount: len(r.Tracks()),
		}
		if t, ok := r.StartTime(); ok {
			s := t.UTC().Format(time.RFC3339)
			hdr.StartTime = &s
		}
		b, err := json.Marshal(hdr)
		if err != nil {
			return nil, err
		}
		rep.Header = b
	}

	if cfg.Media {
		var m gmcMediaJSON
		for _, tr := range r.Tracks() {
			jt := gmcTrackJSON{
				ID:         int(tr.ID),
				Kind:       gmcKindName(tr.Kind),
				Codec:      tr.Codec,
				Timebase:   gmcTimebase{Num: tr.TimebaseNum, Den: tr.TimebaseDen},
				Reordered:  tr.Reordered,
				PrivateLen: len(tr.Private),
			}
			if v, ok := r.LastPTS(tr.ID); ok {
				jt.LastPTS = &v
			}
			m.Tracks = append(m.Tracks, jt)
		}
		b, err := json.Marshal(m)
		if err != nil {
			return nil, err
		}
		rep.Media = b
	}

	if cfg.Tag {
		tm := make(map[string]string, len(tags))
		for k, v := range tags {
			tm[k] = tagValueString(v)
		}
		b, err := json.Marshal(tm)
		if err != nil {
			return nil, err
		}
		rep.Tags = b
	}

	if cfg.Index {
		sums, sync := r.Summaries()
		idx := gmcIndexJSON{SyncPoints: sync}
		if sums != nil {
			for _, s := range sums {
				fp, lp, fr := s.FirstPTS, s.LastPTS, s.Frames
				idx.Tracks = append(idx.Tracks, gmcIndexTrackJSON{
					ID: int(s.Track), FirstPTS: &fp, LastPTS: &lp, Frames: &fr,
				})
			}
		} else {
			// Recovered (non-finalized): frames/firstPTS unknown, lastPTS best-effort.
			for _, tr := range r.Tracks() {
				it := gmcIndexTrackJSON{ID: int(tr.ID)}
				if v, ok := r.LastPTS(tr.ID); ok {
					it.LastPTS = &v
				}
				idx.Tracks = append(idx.Tracks, it)
			}
		}
		b, err := json.Marshal(idx)
		if err != nil {
			return nil, err
		}
		rep.Index = b
	}

	return rep, nil
}
