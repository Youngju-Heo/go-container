package codec

import "encoding/binary"

// splitAnnexB splits an Annex-B byte stream into NAL units, accepting both
// 3-byte (00 00 01) and 4-byte (00 00 00 01) start codes.
func splitAnnexB(annexb []byte) [][]byte {
	var nals [][]byte
	i, start := 0, -1
	for i+2 < len(annexb) {
		if annexb[i] == 0 && annexb[i+1] == 0 && annexb[i+2] == 1 {
			end := i
			if end > 0 && annexb[end-1] == 0 { // 4-byte start code
				end--
			}
			if start >= 0 && end > start {
				nals = append(nals, annexb[start:end])
			}
			i += 3
			start = i
			continue
		}
		i++
	}
	if start >= 0 && start < len(annexb) {
		nals = append(nals, annexb[start:])
	}
	return nals
}

// AnnexBToLengthPrefixed converts an Annex-B access unit into the
// length-prefixed layout used by MKV block payloads (and this codec
// convention). lengthSize must be 1, 2 or 4 and match the value declared in
// avcC/hvcC. dst is reused when non-nil.
func AnnexBToLengthPrefixed(dst, annexb []byte, lengthSize int) ([]byte, error) {
	if lengthSize != 1 && lengthSize != 2 && lengthSize != 4 {
		return nil, ErrInvalidFrame
	}
	nals := splitAnnexB(annexb)
	if len(nals) == 0 {
		return nil, ErrInvalidFrame
	}
	out := dst[:0]
	for _, nal := range nals {
		n := len(nal)
		if lengthSize < 4 && n >= 1<<(8*lengthSize) {
			return nil, ErrInvalidFrame
		}
		switch lengthSize {
		case 1:
			out = append(out, byte(n))
		case 2:
			out = binary.BigEndian.AppendUint16(out, uint16(n))
		case 4:
			out = binary.BigEndian.AppendUint32(out, uint32(n))
		}
		out = append(out, nal...)
	}
	return out, nil
}

// removeEPB strips H.26x emulation prevention bytes (00 00 03 -> 00 00).
func removeEPB(nal []byte) []byte {
	out := make([]byte, 0, len(nal))
	zeros := 0
	for i := 0; i < len(nal); i++ {
		if zeros >= 2 && nal[i] == 3 {
			zeros = 0
			continue
		}
		if nal[i] == 0 {
			zeros++
		} else {
			zeros = 0
		}
		out = append(out, nal[i])
	}
	return out
}

func appendParamSets(b []byte, sets [][]byte) []byte {
	for _, s := range sets {
		b = binary.BigEndian.AppendUint16(b, uint16(len(s)))
		b = append(b, s...)
	}
	return b
}

// BuildAVCC assembles an AVCDecoderConfigurationRecord (ISO 14496-15) with
// 4-byte NAL lengths from raw (non-Annex-B) SPS/PPS NAL units.
func BuildAVCC(sps, pps [][]byte) ([]byte, error) {
	if len(sps) == 0 || len(sps[0]) < 4 || len(pps) == 0 || len(sps) > 31 || len(pps) > 255 {
		return nil, ErrInvalidPrivate
	}
	b := []byte{1, sps[0][1], sps[0][2], sps[0][3], 0xFF, 0xE0 | byte(len(sps))}
	b = appendParamSets(b, sps)
	b = append(b, byte(len(pps)))
	return appendParamSets(b, pps), nil
}

// BuildHVCC assembles a minimal HEVCDecoderConfigurationRecord (ISO 14496-15)
// with 4-byte NAL lengths. The 12-byte profile_tier_level is copied from the
// SPS; the remaining descriptive fields use fixed defaults, which mainstream
// demuxers ignore in favor of the in-band parameter sets.
func BuildHVCC(vps, sps, pps [][]byte) ([]byte, error) {
	if len(sps) == 0 || len(vps) == 0 || len(pps) == 0 {
		return nil, ErrInvalidPrivate
	}
	rbsp := removeEPB(sps[0])
	if len(rbsp) < 15 { // nal header(2) + 1 + ptl(12)
		return nil, ErrInvalidPrivate
	}
	b := []byte{1}
	b = append(b, rbsp[3:15]...) // profile_tier_level
	b = append(b, 0xF0, 0x00)    // min_spatial_segmentation_idc
	b = append(b, 0xFC)          // parallelismType
	b = append(b, 0xFD)          // chromaFormat 4:2:0
	b = append(b, 0xF8)          // bitDepthLumaMinus8 = 0
	b = append(b, 0xF8)          // bitDepthChromaMinus8 = 0
	b = append(b, 0x00, 0x00)    // avgFrameRate
	b = append(b, 0x03)          // constantFrameRate=0 numTemporalLayers=0 temporalIdNested=0 lengthSizeMinusOne=3
	b = append(b, 3)             // numOfArrays
	for _, arr := range []struct {
		typ  byte
		nals [][]byte
	}{{32, vps}, {33, sps}, {34, pps}} {
		b = append(b, arr.typ) // array_completeness=0
		b = binary.BigEndian.AppendUint16(b, uint16(len(arr.nals)))
		b = appendParamSets(b, arr.nals)
	}
	return b, nil
}
