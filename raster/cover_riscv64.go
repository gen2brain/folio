//go:build riscv64 && riscv64.rva23u64 && !noasm

package raster

var coverLo = [6]int{never, 8, 4, 4, 4, 4}

//go:noescape
func coverBlendRVV(dst, src, cover *byte, w, n int)

func coverBlendVec(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	_ = dst[len(cover)*n-1]
	coverBlendRVV(&dst[0], &src[0], &cover[0], len(cover), n)
}
