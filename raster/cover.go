//go:build noasm || (!amd64 && !arm64 && !(riscv64 && riscv64.rva23u64))

package raster

var coverLo = [6]int{never, never, never, never, never, never}

func coverBlend(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	coverBlendScalar(dst, src, cover, n)
}

func coverBlendVec(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	coverBlendScalar(dst, src, cover, n)
}
