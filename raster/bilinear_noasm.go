package raster

// bilinearSlack is how far past the columns a span reads a kernel may load.
const bilinearSlack = 8

// bilinearSpanScalar resamples one span from a column table that already holds
// the vertical blend, and is what the kernels are checked against.
func bilinearSpanScalar(dst []uint8, col []uint16, w, n, k0, x int, a, cu, e float32) {
	for i := range w {
		u := float32(a*(float32(x+i)+0.5)) + cu + e - 0.5
		k := ifloor32(u)
		fu := uint32((u - float32(k)) * 256)
		iu := 256 - fu
		i0 := (k - k0) * n
		a0 := col[i0 : i0+n : i0+n]
		a1 := col[i0+n : i0+n+n : i0+n+n]
		p := dst[i*n : i*n+n : i*n+n]
		for c, s := range a0 {
			p[c] = uint8((uint32(s)*iu + uint32(a1[c])*fu + 1<<15) >> 16)
		}
	}
}
