package gmc

import "math/bits"

// mulDiv64 returns a*b/c using a 128-bit intermediate product. The quotient
// is clamped to MaxUint64 when it does not fit (callers treat that as
// "beyond end of stream"). c must be non-zero.
func mulDiv64(a, b, c uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi >= c {
		return ^uint64(0)
	}
	q, _ := bits.Div64(hi, lo, c)
	return q
}

// ptsToNano converts a track-timebase pts to nanoseconds since the session
// origin: pts * num * 1e9 / den.
func ptsToNano(pts uint64, info TrackInfo) uint64 {
	return mulDiv64(pts, uint64(info.TimebaseNum)*1_000_000_000, uint64(info.TimebaseDen))
}

// nanoToPTS converts nanoseconds since the session origin to a track pts:
// ns * den / (num * 1e9).
func nanoToPTS(ns uint64, info TrackInfo) uint64 {
	return mulDiv64(ns, uint64(info.TimebaseDen), uint64(info.TimebaseNum)*1_000_000_000)
}
