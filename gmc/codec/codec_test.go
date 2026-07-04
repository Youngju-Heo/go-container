package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestVideoPrivateRoundtrip(t *testing.T) {
	avcc := []byte{0x01, 0x64, 0x00, 0x1F}
	b := EncodeVideoPrivate(VideoParams{Width: 1920, Height: 1080}, avcc)
	p, priv, err := DecodeVideoPrivate(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.Width != 1920 || p.Height != 1080 || !bytes.Equal(priv, avcc) {
		t.Fatalf("p=%+v priv=%x", p, priv)
	}
}

func TestAudioPrivateRoundtrip(t *testing.T) {
	b := EncodeAudioPrivate(AudioParams{SampleRate: 48000, Channels: 2, BitDepth: 16}, nil)
	p, priv, err := DecodeAudioPrivate(b)
	if err != nil {
		t.Fatal(err)
	}
	if p.SampleRate != 48000 || p.OutputSampleRate != 0 || p.Channels != 2 || p.BitDepth != 16 || len(priv) != 0 {
		t.Fatalf("p=%+v priv=%x", p, priv)
	}
}

func TestTextPrivateRoundtrip(t *testing.T) {
	b := EncodeTextPrivate([]byte("hdr"))
	priv, err := DecodeTextPrivate(b)
	if err != nil || !bytes.Equal(priv, []byte("hdr")) {
		t.Fatalf("priv=%q err=%v", priv, err)
	}
}

func TestPrivateEnvelopeCorruption(t *testing.T) {
	b := EncodeAudioPrivate(AudioParams{SampleRate: 44100, Channels: 1}, []byte{1, 2, 3})
	if _, _, err := DecodeAudioPrivate(b[:len(b)-1]); !errors.Is(err, ErrInvalidPrivate) {
		t.Fatalf("truncated: err = %v", err)
	}
	if _, _, err := DecodeAudioPrivate(nil); !errors.Is(err, ErrInvalidPrivate) {
		t.Fatalf("empty: err = %v", err)
	}
	bad := append([]byte(nil), b...)
	bad[0] = 9 // unknown version
	if _, _, err := DecodeAudioPrivate(bad); !errors.Is(err, ErrInvalidPrivate) {
		t.Fatalf("version: err = %v", err)
	}
}

func TestTextFrameRoundtrip(t *testing.T) {
	b := EncodeTextFrame(1500, "hello 자막")
	dur, txt, err := DecodeTextFrame(b)
	if err != nil || dur != 1500 || txt != "hello 자막" {
		t.Fatalf("dur=%d txt=%q err=%v", dur, txt, err)
	}
	if _, _, err := DecodeTextFrame(b[:7]); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("short: err = %v", err)
	}
}
