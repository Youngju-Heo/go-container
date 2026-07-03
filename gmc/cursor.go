package gmc

import "encoding/binary"

// cursor is a bounds-checked sequential reader over a byte slice.
// Any out-of-range access sets bad and returns zero values.
type cursor struct {
	b   []byte
	pos int
	bad bool
}

func (c *cursor) need(n int) bool {
	if c.bad || n < 0 || c.pos+n > len(c.b) {
		c.bad = true
		return false
	}
	return true
}

func (c *cursor) u8() byte {
	if !c.need(1) {
		return 0
	}
	v := c.b[c.pos]
	c.pos++
	return v
}

func (c *cursor) u16() uint16 {
	if !c.need(2) {
		return 0
	}
	v := binary.LittleEndian.Uint16(c.b[c.pos:])
	c.pos += 2
	return v
}

func (c *cursor) u32() uint32 {
	if !c.need(4) {
		return 0
	}
	v := binary.LittleEndian.Uint32(c.b[c.pos:])
	c.pos += 4
	return v
}

func (c *cursor) u64() uint64 {
	if !c.need(8) {
		return 0
	}
	v := binary.LittleEndian.Uint64(c.b[c.pos:])
	c.pos += 8
	return v
}

func (c *cursor) bytes(n int) []byte {
	if !c.need(n) {
		return nil
	}
	v := c.b[c.pos : c.pos+n]
	c.pos += n
	return v
}
