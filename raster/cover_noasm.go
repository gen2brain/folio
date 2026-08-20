package raster

// never is the span length no kernel is entered for, which is every length
// where there is no kernel.
const never = 1 << 30

// coverLimit is the shortest span worth a kernel call for a pixel of n bytes.
// A path edge emits spans of two and three pixels by the million, and there
// the call costs more than the arithmetic it saves, so a blitter reads this
// once and compares against it per span.
func coverLimit(n int) int {
	if n >= len(coverLo) {
		return never
	}
	return coverLo[n]
}

func coverVec(dst, cover []uint8, n int) bool {
	return len(cover) >= coverLimit(n) && len(dst) >= 16
}

// coverBlendScalar composites n bytes of src into each pixel of dst under one
// coverage byte per pixel. It is the definition every kernel is checked
// against.
func coverBlendScalar(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	dst = dst[:len(cover)*n]
	p := src[:n]
	for _, c := range cover {
		if c != 0 {
			blendSpan(dst[:n], p, c)
		}
		dst = dst[n:]
	}
}

func scaleRow(dst, src []uint8, k uint8) {
	dst = dst[:len(src)]
	for i, v := range src {
		dst[i] = mul255(v, k)
	}
}

func mulRow(dst, src []uint8) {
	dst = dst[:len(src)]
	for i, v := range src {
		dst[i] = mul255(dst[i], v)
	}
}
