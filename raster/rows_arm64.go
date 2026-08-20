//go:build arm64 && !noasm

package raster

//go:noescape
func blendRowsNEON(dst *uint16, a, c *byte, n int, iv, fv uint32)

func blendRows(dst []uint16, a, c []uint8, iv, fv uint32) {
	w := len(a)
	if w >= 16 {
		k := w &^ 15
		blendRowsNEON(&dst[0], &a[0], &c[0], k, iv, fv)
		blendRowsScalar(dst[k:], a[k:], c[k:], iv, fv)
		return
	}
	blendRowsScalar(dst, a, c, iv, fv)
}
