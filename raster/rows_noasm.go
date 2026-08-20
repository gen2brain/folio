package raster

// blendRowsScalar weights two rows of bytes into one row of sixteen bit
// values, one entry per byte. The weights sum to 256, so nothing overflows.
// It is what the kernels are checked against.
func blendRowsScalar(dst []uint16, a, c []uint8, iv, fv uint32) {
	dst = dst[:len(a)]
	c = c[:len(a)]
	for i, v := range a {
		dst[i] = uint16(uint32(v)*iv + uint32(c[i])*fv)
	}
}
