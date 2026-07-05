package gmc

import (
	"math"
	"testing"
)

func TestMulDiv64(t *testing.T) {
	cases := []struct{ a, b, c, want uint64 }{
		{90000, 1_000_000_000, 90000, 1_000_000_000},        // exact
		{1, 1, 3, 0},                                        // truncation
		{math.MaxUint64, 2, 4, math.MaxUint64 / 2},          // 128-bit intermediate
		{math.MaxUint64, math.MaxUint64, 1, math.MaxUint64}, // quotient overflow -> clamp
	}
	for _, c := range cases {
		if got := mulDiv64(c.a, c.b, c.c); got != c.want {
			t.Fatalf("mulDiv64(%d,%d,%d) = %d, want %d", c.a, c.b, c.c, got, c.want)
		}
	}
}

func TestPTSNanoConversion(t *testing.T) {
	tb := TrackInfo{TimebaseNum: 1, TimebaseDen: 90000}
	if got := ptsToNano(90000, tb); got != 1_000_000_000 {
		t.Fatalf("ptsToNano = %d", got)
	}
	if got := nanoToPTS(1_000_000_000, tb); got != 90000 {
		t.Fatalf("nanoToPTS = %d", got)
	}
	// multi-day recording at 90 kHz: Δns×den overflows u64, must survive
	const days3 = uint64(3 * 24 * 3600 * 1_000_000_000)
	want := uint64(3*24*3600) * 90000
	if got := nanoToPTS(days3, tb); got != want {
		t.Fatalf("3-day nanoToPTS = %d, want %d", got, want)
	}
	// 1 ms timebase (MKV default)
	ms := TrackInfo{TimebaseNum: 1, TimebaseDen: 1000}
	if got := nanoToPTS(1_500_000_000, ms); got != 1500 {
		t.Fatalf("ms nanoToPTS = %d", got)
	}
}
