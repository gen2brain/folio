package raster

import (
	"bytes"
	"fmt"
	"testing"
)

// eachTier runs f once per instruction set the machine supports, so the
// narrow kernels are tested on a wide machine.
func eachTier(t *testing.T, f func(t *testing.T)) {
	t.Helper()
	tiers := simdTiers()
	if len(tiers) == 1 {
		f(t)
		return
	}
	for _, tier := range tiers {
		t.Run(tier, func(t *testing.T) {
			defer setTier(tiers[len(tiers)-1])
			setTier(tier)
			f(t)
		})
	}
}

func eachTierB(b *testing.B, f func(b *testing.B)) {
	b.Helper()
	tiers := simdTiers()
	if len(tiers) == 1 {
		f(b)
		return
	}
	for _, tier := range tiers {
		b.Run(tier, func(b *testing.B) {
			defer setTier(tiers[len(tiers)-1])
			setTier(tier)
			f(b)
		})
	}
}

// coverPattern pads the destination the way a pixmap row does, because a
// kernel is allowed to rewrite whole blocks and the padding is what proves it
// puts back what it found.
func coverPattern(n, w, seed int) (dst []uint8, src *[16]uint8, cover []uint8) {
	dst = make([]uint8, w*n+64)
	src = new([16]uint8)
	cover = make([]uint8, w)
	s := uint32(seed)*2654435761 + 1
	next := func() uint8 {
		s = s*1664525 + 1013904223
		return uint8(s >> 24)
	}
	for i := range dst {
		dst[i] = next()
	}
	for i := range n {
		src[i] = next()
	}
	for i := range cover {
		switch i % 7 {
		case 0:
			cover[i] = 0
		case 1:
			cover[i] = 255
		default:
			cover[i] = next()
		}
	}
	return
}

func TestCoverBlendMatchesScalar(t *testing.T) {
	eachTier(t, func(t *testing.T) {
		for n := 1; n <= 5; n++ {
			for _, w := range []int{0, 1, 2, 3, 7, 15, 16, 17, 31, 32, 33, 63, 100, 129, 257} {
				for seed := range 3 {
					dst, src, cover := coverPattern(n, w, seed*16+w)
					want := bytes.Clone(dst)
					coverBlendScalar(want, src, cover, n)
					coverBlend(dst, src, cover, n)
					if !bytes.Equal(dst, want) {
						t.Fatalf("n=%d w=%d seed=%d:\n got %v\nwant %v", n, w, seed, dst, want)
					}
					short := bytes.Clone(dst[:w*n])
					coverBlend(short, src, cover, n)
					coverBlendScalar(want[:w*n], src, cover, n)
					if !bytes.Equal(short, want[:w*n]) {
						t.Fatalf("n=%d w=%d seed=%d exact destination:\n got %v\nwant %v", n, w, seed, short, want[:w*n])
					}
				}
			}
		}
	})
}

// TestCoverBlendExhaustive walks every destination, source and coverage byte
// there is, which for one component is the kernel's whole reachable domain.
func TestCoverBlendExhaustive(t *testing.T) {
	eachTier(t, func(t *testing.T) {
		dst := make([]uint8, 256+64)
		want := make([]uint8, 256+64)
		cover := make([]uint8, 256)
		src := new([16]uint8)
		for i := range cover {
			cover[i] = uint8(i)
		}
		for s := range 256 {
			src[0] = uint8(s)
			for a := range 256 {
				for i := range dst {
					dst[i] = uint8(i)
					want[i] = uint8(i)
				}
				for i := range cover {
					cover[i] = uint8(a)
				}
				coverBlendScalar(want, src, cover, 1)
				coverBlend(dst, src, cover, 1)
				if !bytes.Equal(dst, want) {
					t.Fatalf("src=%d cover=%d:\n got %v\nwant %v", s, a, dst, want)
				}
			}
		}
	})
}

func BenchmarkCoverBlend(b *testing.B) {
	for _, n := range []int{1, 3, 4} {
		for _, w := range []int{16, 64, 512} {
			b.Run(fmt.Sprintf("n%d/w%d", n, w), func(b *testing.B) {
				eachTierB(b, func(b *testing.B) {
					dst, src, cover := coverPattern(n, w, 1)
					b.SetBytes(int64(w * n))
					for b.Loop() {
						coverBlend(dst, src, cover, n)
					}
				})
			})
		}
	}
}

func bilinearInput(n, cols, seed int) []uint16 {
	col := make([]uint16, cols*n+bilinearSlack)
	s := uint32(seed)*2654435761 + 1
	for i := range cols * n {
		s = s*1664525 + 1013904223
		col[i] = uint16(s>>16) % 65281
	}
	return col
}

// TestBilinearSpanMatchesScalar walks the span the way bilinearRow sets it up,
// with the column table covering exactly the columns the span reaches.
func TestBilinearSpanMatchesScalar(t *testing.T) {
	ucoord := func(x int, a, cu, e float32) float32 {
		return float32(a*(float32(x)+0.5)) + cu + e - 0.5
	}
	eachTier(t, func(t *testing.T) {
		for n := 1; n <= 5; n++ {
			for _, w := range []int{1, 2, 4, 5, 7, 8, 9, 16, 17, 33, 64} {
				for _, a := range []float32{0.03125, 0.25, 0.5, 0.9, -0.25, -0.75} {
					for _, cu := range []float32{0, 3.5, -1.25} {
						const x, e = 5, 0.75
						k0 := ifloor32(ucoord(x, a, cu, e))
						k1 := ifloor32(ucoord(x+w-1, a, cu, e))
						k0, k1 = min(k0, k1), max(k0, k1)
						cols := k1 - k0 + 2
						col := bilinearInput(n, cols, w*13+n)
						got := make([]uint8, w*n+8)
						want := make([]uint8, w*n+8)
						for i := w * n; i < len(got); i++ {
							got[i], want[i] = 0xA5, 0xA5
						}
						bilinearSpanScalar(want, col, w, n, k0, x, a, cu, e)
						bilinearSpan(got, col, w, n, k0, x, a, cu, e)
						if !bytes.Equal(got, want) {
							t.Fatalf("n=%d w=%d a=%v cu=%v:\n got %v\nwant %v",
								n, w, a, cu, got, want)
						}
					}
				}
			}
		}
	})
}

// TestGradientIndexMatchesScalar walks a radial gradient's parameter over a
// span the way shade does, against the scalar it stands in for.
func TestGradientIndexMatchesScalar(t *testing.T) {
	lut := make([]uint8, 256*3)
	specs := []GradientSpec{
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true},
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true, Ext0: true},
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true, Ext1: true},
		{C0: Point{16, 12}, R0: 2, C1: Point{18, 13}, R1: 14, Radial: true, Ext0: true, Ext1: true},
		{C0: Point{16, 12}, R0: 14, C1: Point{18, 13}, R1: 2, Radial: true, Ext0: true},
		{C0: Point{9, 9}, R0: 0, C1: Point{9, 9}, R1: 20, Radial: true},
		{C0: Point{-40, 3}, R0: 1, C1: Point{60, 90}, R1: 3, Radial: true, Ext1: true},
	}
	eachTier(t, func(t *testing.T) {
		for i, spec := range specs {
			for _, m := range []Matrix{Identity, Scale(1.3, 0.9), Rotate(20), {0.5, 0.2, -0.3, 0.7, 4, -9}} {
				spec.Matrix, spec.LUT, spec.N = m, lut, 3
				g := NewGradient(spec)
				if g == nil {
					t.Fatalf("spec %d built nothing", i)
				}
				for _, w := range []int{1, 3, 4, 5, 8, 17, 64} {
					for _, y := range []int{-3, 0, 11} {
						got := make([]int32, w)
						want := make([]int32, w)
						g.indexScalar(-7, y, w, want)
						g.index(-7, y, w, got)
						for k := range got {
							if got[k] != want[k] {
								t.Fatalf("spec %d m=%v w=%d y=%d pixel %d: got %d, want %d",
									i, m, w, y, k, got[k], want[k])
							}
						}
					}
				}
			}
		}
	})
}

// TestBlendRowsMatchesScalar walks the whole reachable domain of a weight
// pair and both sample bytes, and every length either side of the block.
func TestBlendRowsMatchesScalar(t *testing.T) {
	eachTier(t, func(t *testing.T) {
		for _, w := range []int{0, 1, 3, 15, 16, 17, 31, 32, 33, 64, 100} {
			a := make([]uint8, w)
			c := make([]uint8, w)
			s := uint32(w)*2654435761 + 1
			for i := range a {
				s = s*1664525 + 1013904223
				a[i] = uint8(s >> 24)
				s = s*1664525 + 1013904223
				c[i] = uint8(s >> 24)
			}
			for fv := uint32(0); fv <= 256; fv += 17 {
				got := make([]uint16, w)
				want := make([]uint16, w)
				blendRowsScalar(want, a, c, 256-fv, fv)
				blendRows(got, a, c, 256-fv, fv)
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("w=%d fv=%d byte %d: got %d, want %d", w, fv, i, got[i], want[i])
					}
				}
			}
		}
		// every byte pair against every weight the sampler can produce
		a := make([]uint8, 256)
		c := make([]uint8, 256)
		got := make([]uint16, 256)
		want := make([]uint16, 256)
		for i := range a {
			a[i] = uint8(i)
		}
		for v := range 256 {
			for i := range c {
				c[i] = uint8(v)
			}
			for _, fv := range []uint32{0, 1, 127, 128, 255, 256} {
				blendRowsScalar(want, a, c, 256-fv, fv)
				blendRows(got, a, c, 256-fv, fv)
				for i := range got {
					if got[i] != want[i] {
						t.Fatalf("a=%d c=%d fv=%d: got %d, want %d", i, v, fv, got[i], want[i])
					}
				}
			}
		}
	})
}
