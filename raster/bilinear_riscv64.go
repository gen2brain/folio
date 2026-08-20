//go:build riscv64 && riscv64.rva23u64 && !noasm

package raster

//go:noescape
func bilinearSpanRVV(dst *byte, col *uint16, w, n, k0, x int, a, cu, e float32)

func bilinearSpan(dst []uint8, col []uint16, w, n, k0, x int, a, cu, e float32) {
	if n <= 4 && w > 0 {
		bilinearSpanRVV(&dst[0], &col[0], w, n, k0, x, a, cu, e)
		return
	}
	bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
}
