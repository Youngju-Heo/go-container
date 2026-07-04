package mkv

import "math/bits"

// mulDiv returns a*b/c with a 128-bit intermediate, clamping on overflow.
// (Duplicate of gmc's internal helper — three lines kept private per package.)
func mulDiv(a, b, c uint64) uint64 {
	hi, lo := bits.Mul64(a, b)
	if hi >= c {
		return ^uint64(0)
	}
	q, _ := bits.Div64(hi, lo, c)
	return q
}
