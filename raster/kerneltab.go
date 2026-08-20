//go:build (amd64 || arm64) && !noasm

package raster

// coverStep is how many pixels of n components fit in the sixteen byte block a
// kernel works on.
var (
	coverStep = [6]int{0, 16, 8, 4, 4, 2}
	coverLo   = [6]int{never, 16, 12, 8, 8, 8}
)

// coverIdxA and coverIdxS are the byte shuffles that fill that block: one
// replicates a coverage byte over the n bytes of its pixel, the other lays the
// n source bytes down in the same phase. A short block takes the entry for the
// count it holds, whose spare bytes name an index no table has, which shuffles
// in a zero coverage and leaves the destination alone.
var (
	coverIdxA [544]uint8
	coverIdxS [6 * 16]uint8
	coverIdxN [6]int
)

// bilinearIdx picks the second of a column pair out of the vector the first
// was loaded with: eight bytes starting 2n in, and a zero everywhere else.
var bilinearIdx [5 * 16]uint8

func init() {
	off := 0
	for n := 1; n <= 5; n++ {
		coverIdxN[n] = off
		for r := 1; r <= coverStep[n]; r++ {
			for j := range 16 {
				v := uint8(0x80)
				if j < r*n {
					v = uint8(j / n)
				}
				coverIdxA[off+(r-1)*16+j] = v
			}
		}
		off += coverStep[n] * 16
		for j := range 16 {
			coverIdxS[n*16+j] = uint8(j % n)
		}
	}
	for n := 1; n <= 4; n++ {
		for j := range 16 {
			v := uint8(0x80)
			if j < 8 {
				v = uint8(2*n + j)
			}
			bilinearIdx[n*16+j] = v
		}
	}
}
