//go:build amd64 && !noasm

package raster

//go:noescape
func coverBlendAVX2(dst, src, cover, idxa, idxs *byte, m, step, gn int)

//go:noescape
func coverBlendSSE(dst, src, cover, idxa, idxs *byte, m, step, gn int)

func init() {
	if !hasAVX2 && !hasSSE4 {
		coverLo = [6]int{never, never, never, never, never, never}
	}
}

func coverBlend(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	if coverVec(dst, cover, n) {
		coverBlendVec(dst, src, cover, n)
		return
	}
	coverBlendScalar(dst, src, cover, n)
}

func coverBlendVec(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	w := len(cover)
	g := coverStep[n]
	gn := g * n
	lim := (len(dst)-16)/gn + 1

	done := 0
	if m := min(w/g, lim); m > 0 {
		coverBlendAsm(&dst[0], &src[0], &cover[0], &coverIdxA[coverIdxN[n]+(g-1)*16], &coverIdxS[n*16], m, g, gn)
		done = m * g
	}
	if done < w {
		coverBlendScalar(dst[done*n:], src, cover[done:], n)
	}
}

func coverBlendAsm(dst, src, cover, idxa, idxs *byte, m, step, gn int) {
	if hasAVX2 {
		coverBlendAVX2(dst, src, cover, idxa, idxs, m, step, gn)
		return
	}
	coverBlendSSE(dst, src, cover, idxa, idxs, m, step, gn)
}
