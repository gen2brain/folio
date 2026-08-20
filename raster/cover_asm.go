//go:build !noasm && (amd64 || arm64 || (riscv64 && riscv64.rva23u64))

package raster

func coverBlend(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	if coverVec(dst, cover, n) {
		coverBlendVec(dst, src, cover, n)
		return
	}
	coverBlendScalar(dst, src, cover, n)
}
