// Package mkv implements Matroska (MKV) import and export for GMC files,
// with a defensive pure-Go EBML reader and a minimal standard-form writer.
package mkv

import (
	"encoding/binary"
	"errors"
	"io"
	"math"
)

var errMalformed = errors.New("mkv: malformed ebml")

// maxElementSize bounds any declared element size (runaway-scan defense).
const maxElementSize int64 = 1 << 40

// --- writing primitives (append-style, big-endian per EBML) ---

// appendVintID writes an element ID verbatim (IDs keep their marker bits).
func appendVintID(dst []byte, id uint32) []byte {
	switch {
	case id > 0xFFFFFF:
		return append(dst, byte(id>>24), byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFFFF:
		return append(dst, byte(id>>16), byte(id>>8), byte(id))
	case id > 0xFF:
		return append(dst, byte(id>>8), byte(id))
	default:
		return append(dst, byte(id))
	}
}

// appendVintSize writes a size vint using the minimal length (1..8 bytes).
func appendVintSize(dst []byte, v int64) []byte {
	for n := 1; n <= 8; n++ {
		if v < int64(1)<<(7*n)-1 { // reserve all-ones (unknown size)
			b := make([]byte, n)
			for i := n - 1; i >= 0; i-- {
				b[i] = byte(v)
				v >>= 8
			}
			b[0] |= 0x80 >> (n - 1)
			return append(dst, b...)
		}
	}
	panic("mkv: vint size out of range")
}

func appendElement(dst []byte, id uint32, payload []byte) []byte {
	dst = appendVintID(dst, id)
	dst = appendVintSize(dst, int64(len(payload)))
	return append(dst, payload...)
}

func appendUintElement(dst []byte, id uint32, v uint64) []byte {
	var p []byte
	n := 1
	for tmp := v; tmp > 0xFF; tmp >>= 8 {
		n++
	}
	for i := n - 1; i >= 0; i-- {
		p = append(p, byte(v>>(8*i)))
	}
	return appendElement(dst, id, p)
}

func appendStringElement(dst []byte, id uint32, s string) []byte {
	return appendElement(dst, id, []byte(s))
}

func appendFloatElement(dst []byte, id uint32, f float64) []byte {
	var p [8]byte
	binary.BigEndian.PutUint64(p[:], math.Float64bits(f))
	return appendElement(dst, id, p[:])
}

// --- reading primitives ---

// readVintAt decodes an unsigned vint (marker removed) from b.
// Returns n=0 on malformed input.
func readVintAt(b []byte) (uint64, int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n := 1
	for mask := byte(0x80); b[0]&mask == 0; mask >>= 1 {
		n++
	}
	if n > 8 || len(b) < n {
		return 0, 0
	}
	v := uint64(b[0] &^ (0x80 >> (n - 1)))
	for i := 1; i < n; i++ {
		v = v<<8 | uint64(b[i])
	}
	return v, n
}

// readSignedVintAt decodes an EBML-lacing signed vint:
// signed = unsigned - (2^(7*n-1) - 1).
func readSignedVintAt(b []byte) (int64, int) {
	v, n := readVintAt(b)
	if n == 0 {
		return 0, 0
	}
	return int64(v) - (int64(1)<<(7*n-1) - 1), n
}

func parseUint(b []byte) uint64 {
	var v uint64
	for _, c := range b {
		v = v<<8 | uint64(c)
	}
	return v
}

func parseFloat(b []byte) (float64, error) {
	switch len(b) {
	case 0:
		return 0, nil
	case 4:
		return float64(math.Float32frombits(binary.BigEndian.Uint32(b))), nil
	case 8:
		return math.Float64frombits(binary.BigEndian.Uint64(b)), nil
	default:
		return 0, errMalformed
	}
}

// ebmlReader walks elements sequentially over an io.ReaderAt.
type ebmlReader struct {
	r    io.ReaderAt
	cur  int64
	size int64
}

func newEBMLReader(r io.ReaderAt, size int64) *ebmlReader {
	return &ebmlReader{r: r, size: size}
}

func (er *ebmlReader) pos() int64 { return er.cur }

func (er *ebmlReader) peek(n int) ([]byte, error) {
	if er.cur >= er.size {
		return nil, io.EOF
	}
	if int64(n) > er.size-er.cur {
		n = int(er.size - er.cur)
	}
	b := make([]byte, n)
	rn, err := er.r.ReadAt(b, er.cur)
	if err != nil && err != io.EOF {
		return nil, err
	}
	return b[:rn], nil
}

// readElement reads the next element header. unknown is true for
// unknown-size masters (all size bits set). io.EOF at end of stream.
func (er *ebmlReader) readElement() (uint32, int64, bool, error) {
	head, err := er.peek(12)
	if err != nil {
		return 0, 0, false, err
	}
	if len(head) == 0 || head[0] == 0 {
		return 0, 0, false, errMalformed
	}
	idLen := 1
	for mask := byte(0x80); head[0]&mask == 0; mask >>= 1 {
		idLen++
	}
	if idLen > 4 || len(head) < idLen {
		return 0, 0, false, errMalformed
	}
	var id uint32
	for i := 0; i < idLen; i++ {
		id = id<<8 | uint32(head[i])
	}
	sz, szLen := readVintAt(head[idLen:])
	if szLen == 0 {
		return 0, 0, false, errMalformed
	}
	unknown := sz == uint64(1)<<(7*szLen)-1
	er.cur += int64(idLen + szLen)
	if unknown {
		return id, 0, true, nil
	}
	if int64(sz) > maxElementSize || er.cur+int64(sz) > er.size {
		return 0, 0, false, errMalformed
	}
	return id, int64(sz), false, nil
}

func (er *ebmlReader) readBytes(n int64) ([]byte, error) {
	if n < 0 || er.cur+n > er.size {
		return nil, errMalformed
	}
	b := make([]byte, n)
	if n > 0 {
		if _, err := er.r.ReadAt(b, er.cur); err != nil {
			return nil, err
		}
	}
	er.cur += n
	return b, nil
}

func (er *ebmlReader) skip(n int64) { er.cur += n }
