package codec

import (
	"bytes"
	"errors"
	"testing"
)

func TestAnnexBToLengthPrefixed(t *testing.T) {
	// two NALs with 4-byte and 3-byte start codes
	annexb := append([]byte{0, 0, 0, 1, 0x65, 0xAA, 0xBB}, []byte{0, 0, 1, 0x41, 0xCC}...)
	out, err := AnnexBToLengthPrefixed(nil, annexb, 4)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0, 3, 0x65, 0xAA, 0xBB, 0, 0, 0, 2, 0x41, 0xCC}
	if !bytes.Equal(out, want) {
		t.Fatalf("out = %x, want %x", out, want)
	}
	// 2-byte length
	out2, err := AnnexBToLengthPrefixed(nil, annexb, 2)
	if err != nil || !bytes.Equal(out2[:2], []byte{0, 3}) {
		t.Fatalf("out2 = %x err=%v", out2, err)
	}
	// oversize NAL for 1-byte length
	big := append([]byte{0, 0, 0, 1}, make([]byte, 300)...)
	if _, err := AnnexBToLengthPrefixed(nil, big, 1); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("oversize: err = %v", err)
	}
	// no start code
	if _, err := AnnexBToLengthPrefixed(nil, []byte{1, 2, 3}, 4); !errors.Is(err, ErrInvalidFrame) {
		t.Fatalf("no start code: err = %v", err)
	}
}

func TestBuildAVCC(t *testing.T) {
	sps := []byte{0x67, 0x64, 0x00, 0x1F, 0xAC} // profile 0x64, compat 0x00, level 0x1F
	pps := []byte{0x68, 0xEB}
	avcc, err := BuildAVCC([][]byte{sps}, [][]byte{pps})
	if err != nil {
		t.Fatal(err)
	}
	if avcc[0] != 1 || avcc[1] != 0x64 || avcc[2] != 0x00 || avcc[3] != 0x1F {
		t.Fatalf("header = %x", avcc[:4])
	}
	if avcc[4] != 0xFF || avcc[5] != 0xE1 { // 4-byte lengths, 1 SPS
		t.Fatalf("flags = %x", avcc[4:6])
	}
	// SPS block: len u16 BE + bytes
	if avcc[6] != 0 || avcc[7] != 5 || !bytes.Equal(avcc[8:13], sps) {
		t.Fatalf("sps block = %x", avcc[6:13])
	}
	if _, err := BuildAVCC(nil, [][]byte{pps}); !errors.Is(err, ErrInvalidPrivate) {
		t.Fatalf("no sps: err = %v", err)
	}
}

func TestBuildHVCC(t *testing.T) {
	// synthetic HEVC SPS: nal header(2) + 1 byte + 12-byte profile_tier_level
	ptl := []byte{0x01, 0x60, 0x00, 0x00, 0x00, 0x90, 0x00, 0x00, 0x00, 0x00, 0x00, 0x5D}
	sps := append([]byte{0x42, 0x01, 0x01}, ptl...)
	vps := []byte{0x40, 0x01, 0x0C}
	pps := []byte{0x44, 0x01, 0xC0}
	hvcc, err := BuildHVCC([][]byte{vps}, [][]byte{sps}, [][]byte{pps})
	if err != nil {
		t.Fatal(err)
	}
	if hvcc[0] != 1 {
		t.Fatalf("version = %d", hvcc[0])
	}
	if !bytes.Equal(hvcc[1:13], ptl) {
		t.Fatalf("ptl = %x, want %x", hvcc[1:13], ptl)
	}
	if hvcc[21]&0x03 != 0x03 { // lengthSizeMinusOne = 3
		t.Fatalf("lengthSize byte = %x", hvcc[21])
	}
	if hvcc[22] != 3 { // three arrays: vps, sps, pps
		t.Fatalf("numArrays = %d", hvcc[22])
	}
}

func TestRemoveEPB(t *testing.T) {
	in := []byte{0x00, 0x00, 0x03, 0x01, 0x00, 0x00, 0x03, 0x03}
	want := []byte{0x00, 0x00, 0x01, 0x00, 0x00, 0x03}
	if got := removeEPB(in); !bytes.Equal(got, want) {
		t.Fatalf("got %x, want %x", got, want)
	}
}
