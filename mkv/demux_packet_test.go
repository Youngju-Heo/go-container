package mkv

import (
	"bytes"
	"io"
	"testing"
)

// appendSimpleBlock builds a SimpleBlock payload: track vint, i16 BE relative
// ts, flags, body.
func appendSimpleBlock(dst []byte, track uint64, rel int16, flags byte, body []byte) []byte {
	var p []byte
	p = appendVintSize(p, int64(track)) // track number as vint
	p = append(p, byte(uint16(rel)>>8), byte(uint16(rel)))
	p = append(p, flags)
	p = append(p, body...)
	return appendElement(dst, idSimpleBlock, p)
}

func buildClusterMKV(t *testing.T) []byte {
	t.Helper()
	// cluster 1 @ ts 0: video keyframe(rel 0), video non-key(rel 40)
	var c1 []byte
	c1 = appendUintElement(c1, idTimestamp, 0)
	c1 = appendSimpleBlock(c1, 1, 0, 0x80, []byte("kf0"))
	c1 = appendSimpleBlock(c1, 1, 40, 0x00, []byte("p1"))
	// audio: Xiph lacing, 3 frames sizes 2,3,rest @ rel 0
	lace := []byte{0x02, 2, 3} // count-1=2, size1=2, size2=3
	body := append(lace, []byte{0xA, 0xB, 0xC, 0xD, 0xE, 0xF, 0x1, 0x2}...)
	var p []byte
	p = appendVintSize(p, 2)
	p = append(p, 0, 0, 0x82) // rel 0, keyframe|Xiph(1<<1)
	p = append(p, body...)
	c1 = appendElement(c1, idSimpleBlock, p)

	// cluster 2 @ ts 100: BlockGroup subtitle with duration, no ReferenceBlock
	var blk []byte
	blk = appendVintSize(blk, 3)
	blk = append(blk, 0, 0, 0x00)
	blk = append(blk, []byte("subtitle-text")...)
	var bg []byte
	bg = appendElement(bg, idBlock, blk)
	bg = appendUintElement(bg, idBlockDur, 1500)
	var c2 []byte
	c2 = appendUintElement(c2, idTimestamp, 100)
	c2 = appendElement(c2, idBlockGroup, bg)

	var clusters []byte
	clusters = appendElement(clusters, idCluster, c1)
	clusters = appendElement(clusters, idCluster, c2)
	return buildTestMKV(t, clusters)
}

func TestReadPacketBlocksAndLacing(t *testing.T) {
	data := buildClusterMKV(t)
	d, err := NewDemuxer(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	var pkts []*Packet
	for {
		p, err := d.ReadPacket()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		pkts = append(pkts, p)
	}
	// 2 video + 3 laced audio + 1 subtitle
	if len(pkts) != 6 {
		t.Fatalf("packets = %d", len(pkts))
	}
	if pkts[0].Track != 1 || pkts[0].Timestamp != 0 || !pkts[0].Keyframe || !bytes.Equal(pkts[0].Data, []byte("kf0")) {
		t.Fatalf("pkt0 = %+v", pkts[0])
	}
	if pkts[1].Timestamp != 40 || pkts[1].Keyframe {
		t.Fatalf("pkt1 = %+v", pkts[1])
	}
	// laced audio: sizes 2,3,3 — DefaultDuration 20ms / scale 1ms = 20
	if !bytes.Equal(pkts[2].Data, []byte{0xA, 0xB}) || !bytes.Equal(pkts[3].Data, []byte{0xC, 0xD, 0xE}) || !bytes.Equal(pkts[4].Data, []byte{0xF, 0x1, 0x2}) {
		t.Fatalf("laced data %x %x %x", pkts[2].Data, pkts[3].Data, pkts[4].Data)
	}
	if pkts[2].Timestamp != 0 || pkts[3].Timestamp != 20 || pkts[4].Timestamp != 40 {
		t.Fatalf("laced ts %d %d %d", pkts[2].Timestamp, pkts[3].Timestamp, pkts[4].Timestamp)
	}
	// subtitle from BlockGroup: keyframe (no ReferenceBlock), duration 1500, cluster ts 100
	sub := pkts[5]
	if sub.Track != 3 || sub.Timestamp != 100 || !sub.Keyframe || sub.Duration != 1500 || string(sub.Data) != "subtitle-text" {
		t.Fatalf("subtitle = %+v", sub)
	}
}
