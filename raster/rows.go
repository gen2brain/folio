//go:build noasm || (!amd64 && !arm64 && !(riscv64 && riscv64.rva23u64))

package raster

func blendRows(dst []uint16, a, c []uint8, iv, fv uint32) {
	blendRowsScalar(dst, a, c, iv, fv)
}
