//go:build arm64 && !noasm

package raster

//go:noescape
func coverBlendNEON(dst, src, cover, idxa, idxs *byte, m, step, gn int)

func coverBlendVec(dst []uint8, src *[16]uint8, cover []uint8, n int) {
	w := len(cover)
	g := coverStep[n]
	gn := g * n
	lim := (len(dst)-16)/gn + 1

	done := 0
	if m := min(w/g, lim); m > 0 {
		coverBlendNEON(&dst[0], &src[0], &cover[0], &coverIdxA[coverIdxN[n]+(g-1)*16], &coverIdxS[n*16], m, g, gn)
		done = m * g
	}
	if done < w {
		coverBlendScalar(dst[done*n:], src, cover[done:], n)
	}
}
