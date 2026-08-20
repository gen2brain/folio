package raster

// never is the span length no kernel is entered for.
const never = 1 << 30

// coverLimit is the shortest span worth a kernel call for a pixel of n bytes.
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
// coverage byte per pixel, and is what the kernels are checked against.
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
