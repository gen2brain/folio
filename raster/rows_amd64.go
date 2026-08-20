//go:build amd64 && !noasm

package raster

//go:noescape
func blendRowsSSE(dst *uint16, a, c *byte, n int, iv, fv uint32)

func blendRows(dst []uint16, a, c []uint8, iv, fv uint32) {
	w := len(a)
	if hasSSE4 && w >= 16 {
		k := w &^ 15
		blendRowsSSE(&dst[0], &a[0], &c[0], k, iv, fv)
		blendRowsScalar(dst[k:], a[k:], c[k:], iv, fv)
		return
	}
	blendRowsScalar(dst, a, c, iv, fv)
}
