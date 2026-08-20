//go:build arm64 && !noasm

package raster

//go:noescape
func bilinearSpanNEON(dst *byte, col *uint16, idx *byte, w, n, k0, x int, a, cu, e float32)

func bilinearSpan(dst []uint8, col []uint16, w, n, k0, x int, a, cu, e float32) {
	// The kernel stores a whole four bytes per pixel, so it stops early
	// enough that the last of them stays inside the span.
	if n <= 4 && w >= 8 {
		v := (w + 1 - (4+n-1)/n) &^ 3
		if v <= 0 {
			bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
			return
		}
		bilinearSpanNEON(&dst[0], &col[0], &bilinearIdx[n*16], v, n, k0, x, a, cu, e)
		bilinearSpanScalar(dst[v*n:], col, w-v, n, k0, x+v, a, cu, e)
		return
	}
	bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
}
