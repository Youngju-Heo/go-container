// Package gmc implements the GMC media container format.
package gmc

import (
	"errors"
	"hash/crc32"
)

const (
	fileMagic = "GMC1"
	endMagic  = "GMCE"

	formatVersion = 1

	// Version is the file format version this package reads and writes. Open
	// rejects files carrying any other version, so any opened file is Version.
	Version = formatVersion

	headerFixedSize = 20 // magic(4)+version(2)+flags(2)+tagsAreaLen(4)+privateLen(4)+crc(4)
	trailerSize     = 16 // footerOffset(8)+crc(4)+endMagic(4)

	chunkHeaderSize  = 5 // payloadLen(4)+type(1)
	chunkFramingSize = 9 // header(5)+crc(4)

	maxPayloadLen = 256 << 20 // 256 MiB

	defaultTagsAreaSize = 8 << 10
)

const (
	chunkTrackInfo  byte = 0x01
	chunkData       byte = 0x02
	chunkCheckpoint byte = 0x03
	chunkFooter     byte = 0x04
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

var (
	ErrCorrupt         = errors.New("gmc: corrupt data")
	ErrNonMonotonicPTS = errors.New("gmc: non-monotonic pts within track")
	ErrTagsTooLarge    = errors.New("gmc: tags exceed slot capacity")
	ErrUnknownTrack    = errors.New("gmc: unknown track")
	ErrClosed          = errors.New("gmc: writer closed")
	ErrNoStartTime     = errors.New("gmc: start time tag not set")
)
