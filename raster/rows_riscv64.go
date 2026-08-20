//go:build riscv64 && riscv64.rva23u64 && !noasm

package raster

//go:noescape
func blendRowsRVV(dst *uint16, a, c *byte, n int, iv, fv uint32)

func blendRows(dst []uint16, a, c []uint8, iv, fv uint32) {
	if w := len(a); w > 0 {
		_ = dst[w-1]
		_ = c[w-1]
		blendRowsRVV(&dst[0], &a[0], &c[0], w, iv, fv)
	}
}
