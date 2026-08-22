package svg

import "math"

// The Perlin generator of SVG 1.1 15.24, whose congruential sequence the
// section fixes exactly.
const (
	randM    = 2147483647
	randA    = 16807
	randQ    = 127773
	randR    = 2836
	bSize    = 0x100
	bLen     = bSize + bSize + 2
	bMask    = 0xff
	perlinN  = 0x1000
	maxOctav = 32
)

type noise struct {
	lattice  [bLen]int
	gradient [4][bLen][2]float64
}

type stitch struct{ w, h, wrapX, wrapY int }

func newNoise(seed int) *noise {
	n := &noise{}
	if seed <= 0 {
		seed = -seed%(randM-1) + 1
	}
	if seed > randM-1 {
		seed = randM - 1
	}
	for k := range 4 {
		for i := range bSize {
			n.lattice[i] = i
			for j := range 2 {
				seed = nextRand(seed)
				n.gradient[k][i][j] = float64((seed%(bSize+bSize))-bSize) / bSize
			}
			g := &n.gradient[k][i]
			s := math.Sqrt(g[0]*g[0] + g[1]*g[1])
			g[0] /= s
			g[1] /= s
		}
	}
	for i := bSize - 1; i > 0; i-- {
		k := n.lattice[i]
		seed = nextRand(seed)
		j := seed % bSize
		n.lattice[i] = n.lattice[j]
		n.lattice[j] = k
	}
	for i := range bSize + 2 {
		n.lattice[bSize+i] = n.lattice[i]
		for k := range 4 {
			n.gradient[k][bSize+i] = n.gradient[k][i]
		}
	}
	return n
}

func nextRand(seed int) int {
	v := randA*(seed%randQ) - randR*(seed/randQ)
	if v <= 0 {
		v += randM
	}
	return v
}

// turbulence sums the octaves at one point of one channel.
func (n *noise) turbulence(ch int, x, y, tileX, tileY, tileW, tileH, fx, fy float64,
	octaves int, fractal, tiled bool) float64 {

	var st *stitch
	if tiled {
		if fx != 0 {
			lo := math.Floor(tileW*fx) / tileW
			hi := math.Ceil(tileW*fx) / tileW
			if fx/lo < hi/fx {
				fx = lo
			} else {
				fx = hi
			}
		}
		if fy != 0 {
			lo := math.Floor(tileH*fy) / tileH
			hi := math.Ceil(tileH*fy) / tileH
			if fy/lo < hi/fy {
				fy = lo
			} else {
				fy = hi
			}
		}
		w := int(tileW*fx + 0.5)
		h := int(tileH*fy + 0.5)
		st = &stitch{
			w: w, h: h,
			wrapX: int(tileX*fx + perlinN + float64(w)),
			wrapY: int(tileY*fy + perlinN + float64(h)),
		}
	}
	sum := 0.0
	x *= fx
	y *= fy
	ratio := 1.0
	for range octaves {
		v := n.noise2(ch, x, y, st)
		if !fractal {
			v = math.Abs(v)
		}
		sum += v / ratio
		x *= 2
		y *= 2
		ratio *= 2
		if st != nil {
			st.w *= 2
			st.wrapX = 2*st.wrapX - perlinN
			st.h *= 2
			st.wrapY = 2*st.wrapY - perlinN
		}
	}
	return sum
}

func (n *noise) noise2(ch int, x, y float64, st *stitch) float64 {
	t := x + perlinN
	bx0, bx1 := int(t), int(t)+1
	rx0 := t - math.Trunc(t)
	rx1 := rx0 - 1
	t = y + perlinN
	by0, by1 := int(t), int(t)+1
	ry0 := t - math.Trunc(t)
	ry1 := ry0 - 1
	if st != nil {
		if bx0 >= st.wrapX {
			bx0 -= st.w
		}
		if bx1 >= st.wrapX {
			bx1 -= st.w
		}
		if by0 >= st.wrapY {
			by0 -= st.h
		}
		if by1 >= st.wrapY {
			by1 -= st.h
		}
	}
	bx0 &= bMask
	bx1 &= bMask
	by0 &= bMask
	by1 &= bMask
	i, j := n.lattice[bx0], n.lattice[bx1]
	b00 := n.lattice[i+by0]
	b10 := n.lattice[j+by0]
	b01 := n.lattice[i+by1]
	b11 := n.lattice[j+by1]
	sx := curve(rx0)
	sy := curve(ry0)
	q := n.gradient[ch][b00]
	u := rx0*q[0] + ry0*q[1]
	q = n.gradient[ch][b10]
	v := rx1*q[0] + ry0*q[1]
	a := lerp(sx, u, v)
	q = n.gradient[ch][b01]
	u = rx0*q[0] + ry1*q[1]
	q = n.gradient[ch][b11]
	v = rx1*q[0] + ry1*q[1]
	b := lerp(sx, u, v)
	return lerp(sy, a, b)
}

func curve(t float64) float64      { return t * t * (3 - 2*t) }
func lerp(t, a, b float64) float64 { return a + t*(b-a) }
