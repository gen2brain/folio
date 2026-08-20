//go:build amd64 && !noasm

package raster

//go:noescape
func bilinearSpanSSE(dst *byte, col *uint16, idx *byte, w, n, k0, x int, a, cu, e float32)

// bilinearIdx picks the second of a column pair out of the vector the first
// was loaded with: eight bytes starting 2n in, and a zero everywhere else.
var bilinearIdx [5 * 16]uint8

func init() {
	for n := 1; n <= 4; n++ {
		for j := range 16 {
			v := uint8(0x80)
			if j < 8 {
				v = uint8(2*n + j)
			}
			bilinearIdx[n*16+j] = v
		}
	}
}

func bilinearSpan(dst []uint8, col []uint16, w, n, k0, x int, a, cu, e float32) {
	// The kernel stores a whole four bytes per pixel, so it stops early
	// enough that the last of them stays inside the span.
	if hasSSE4 && n <= 4 && w >= 8 {
		v := (w + 1 - (4+n-1)/n) &^ 3
		if v <= 0 {
			bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
			return
		}
		bilinearSpanSSE(&dst[0], &col[0], &bilinearIdx[n*16], v, n, k0, x, a, cu, e)
		bilinearSpanScalar(dst[v*n:], col, w-v, n, k0, x+v, a, cu, e)
		return
	}
	bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
}
