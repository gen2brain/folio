//go:build noasm || (!amd64 && !arm64 && !(riscv64 && riscv64.rva23u64))

package raster

func bilinearSpan(dst []uint8, col []uint16, w, n, k0, x int, a, cu, e float32) {
	bilinearSpanScalar(dst, col, w, n, k0, x, a, cu, e)
}
