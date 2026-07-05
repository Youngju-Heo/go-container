// Package codec defines the GMC codec conventions that mirror Matroska:
// Matroska CodecID strings in TrackInfo.Codec, MKV block payload bytes in
// Frame.Data, and a small private envelope carrying the MKV TrackEntry
// Audio/Video parameters alongside the original CodecPrivate. The gmc core
// itself stays codec-agnostic; this package is the interpretation layer.
//
// # Per-codec private data (CodecPrivate)
//
// The codecPrivate bytes carried inside the envelope follow the Matroska
// CodecPrivate specification for each codec, byte for byte
// (https://www.matroska.org/technical/codec_specs.html). Nothing
// GMC-specific is added or reinterpreted:
//
//	V_MPEG4/ISO/AVC   AVCDecoderConfigurationRecord ("avcC", ISO/IEC 14496-15)
//	V_MPEGH/ISO/HEVC  HEVCDecoderConfigurationRecord ("hvcC", ISO/IEC 14496-15)
//	A_OPUS            OpusHead identification header (RFC 7845 §5.1)
//	A_AAC             AudioSpecificConfig (ISO/IEC 14496-3) — raw, no ADTS
//	A_FLAC            "fLaC" signature + STREAMINFO and all metadata blocks
//	                  preceding the first audio frame
//	A_PCM/INT/LIT     empty — parameters live in AudioParams (the mirror of
//	                  the MKV TrackEntry Audio element)
//	S_TEXT/UTF8       empty
//
// Consequently a CodecPrivate taken from (or destined for) an MKV file is
// used as-is, and applications ingesting live streams build the same records
// (see BuildAVCC/BuildHVCC for the H.26x helpers).
package codec

import (
	"encoding/binary"
	"errors"
)

// Matroska CodecIDs adopted verbatim.
const (
	CodecAVC      = "V_MPEG4/ISO/AVC"
	CodecHEVC     = "V_MPEGH/ISO/HEVC"
	CodecPCM      = "A_PCM/INT/LIT"
	CodecOpus     = "A_OPUS"
	CodecAAC      = "A_AAC"
	CodecFLAC     = "A_FLAC"
	CodecTextUTF8 = "S_TEXT/UTF8"
)

const envelopeVersion = 1

var (
	ErrInvalidPrivate = errors.New("codec: invalid private envelope")
	ErrInvalidFrame   = errors.New("codec: invalid frame payload")
)

// VideoParams mirrors the MKV TrackEntry Video element.
type VideoParams struct {
	Width, Height uint32
}

// AudioParams mirrors the MKV TrackEntry Audio element. OutputSampleRate is
// zero when identical to SampleRate (it differs for HE-AAC). BitDepth zero
// means unspecified.
type AudioParams struct {
	SampleRate, OutputSampleRate uint32
	Channels, BitDepth           uint8
}

func appendPrivate(b, codecPrivate []byte) []byte {
	b = binary.LittleEndian.AppendUint32(b, uint32(len(codecPrivate)))
	return append(b, codecPrivate...)
}

func splitPrivate(b []byte) ([]byte, error) {
	if len(b) < 4 {
		return nil, ErrInvalidPrivate
	}
	n := int(binary.LittleEndian.Uint32(b))
	if len(b) != 4+n {
		return nil, ErrInvalidPrivate
	}
	return append([]byte(nil), b[4:]...), nil
}

// EncodeVideoPrivate builds the TrackInfo.Private envelope for a video track:
// ver(1) width(4) height(4) privLen(4) codecPrivate.
func EncodeVideoPrivate(p VideoParams, codecPrivate []byte) []byte {
	b := []byte{envelopeVersion}
	b = binary.LittleEndian.AppendUint32(b, p.Width)
	b = binary.LittleEndian.AppendUint32(b, p.Height)
	return appendPrivate(b, codecPrivate)
}

func DecodeVideoPrivate(b []byte) (VideoParams, []byte, error) {
	if len(b) < 9 || b[0] != envelopeVersion {
		return VideoParams{}, nil, ErrInvalidPrivate
	}
	p := VideoParams{
		Width:  binary.LittleEndian.Uint32(b[1:5]),
		Height: binary.LittleEndian.Uint32(b[5:9]),
	}
	priv, err := splitPrivate(b[9:])
	if err != nil {
		return VideoParams{}, nil, err
	}
	return p, priv, nil
}

// EncodeAudioPrivate builds the envelope for an audio track:
// ver(1) sampleRate(4) outputSampleRate(4) channels(1) bitDepth(1) privLen(4) codecPrivate.
func EncodeAudioPrivate(p AudioParams, codecPrivate []byte) []byte {
	b := []byte{envelopeVersion}
	b = binary.LittleEndian.AppendUint32(b, p.SampleRate)
	b = binary.LittleEndian.AppendUint32(b, p.OutputSampleRate)
	b = append(b, p.Channels, p.BitDepth)
	return appendPrivate(b, codecPrivate)
}

func DecodeAudioPrivate(b []byte) (AudioParams, []byte, error) {
	if len(b) < 11 || b[0] != envelopeVersion {
		return AudioParams{}, nil, ErrInvalidPrivate
	}
	p := AudioParams{
		SampleRate:       binary.LittleEndian.Uint32(b[1:5]),
		OutputSampleRate: binary.LittleEndian.Uint32(b[5:9]),
		Channels:         b[9],
		BitDepth:         b[10],
	}
	priv, err := splitPrivate(b[11:])
	if err != nil {
		return AudioParams{}, nil, err
	}
	return p, priv, nil
}

// EncodeTextPrivate builds the envelope for a text/metadata track:
// ver(1) privLen(4) codecPrivate.
func EncodeTextPrivate(codecPrivate []byte) []byte {
	return appendPrivate([]byte{envelopeVersion}, codecPrivate)
}

func DecodeTextPrivate(b []byte) ([]byte, error) {
	if len(b) < 5 || b[0] != envelopeVersion {
		return nil, ErrInvalidPrivate
	}
	return splitPrivate(b[1:])
}
