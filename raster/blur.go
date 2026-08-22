package raster

import "math"

// blurBox is 3*sqrt(2*pi)/4, the factor SVG 1.1 15.17 turns a deviation into
// a box width with, and blurExact where that section stops allowing the
// approximation.
const (
	blurBox   = 1.8799712059732503
	blurExact = 2
)

// Blur blurs a premultiplied pixmap by a Gaussian of the given standard
// deviations, in pixels. Below two it convolves the kernel and from two up it
// is the three box blurs of SVG 1.1 15.17. Outside the pixmap is transparent.
func Blur(p *Pixmap, sigmaX, sigmaY float32) {
	if p == nil || p.W <= 0 || p.H <= 0 {
		return
	}
	n := p.Comps()
	var buf []uint8
	src, dst := p.Samples, buf
	pass := func(vertical bool, run func(dst, src []uint8, count, stride int)) {
		if dst == nil {
			buf = make([]uint8, len(p.Samples))
			dst = buf
		}
		if vertical {
			for x := 0; x < p.W; x++ {
				o := x * n
				run(dst[o:], src[o:], p.H, p.Stride)
			}
		} else {
			for y := 0; y < p.H; y++ {
				o := y * p.Stride
				run(dst[o:], src[o:], p.W, n)
			}
		}
		src, dst = dst, src
	}
	for axis, sigma := range [2]float32{sigmaX, sigmaY} {
		vertical := axis == 1
		if k := gaussKernel(sigma); k != nil {
			pass(vertical, func(dst, src []uint8, count, stride int) {
				convLine(dst, src, count, stride, n, k)
			})
			continue
		}
		for _, w := range boxPasses(boxWidth(sigma)) {
			lead, trail := w[0], w[1]
			pass(vertical, func(dst, src []uint8, count, stride int) {
				boxLine(dst, src, count, stride, n, lead, trail)
			})
		}
	}
	if buf != nil && &src[0] != &p.Samples[0] {
		copy(p.Samples, src)
	}
}

// gaussKernel is the sampled kernel, and nil for a deviation the box
// approximation covers.
func gaussKernel(sigma float32) []float32 {
	if sigma <= 0 || sigma >= blurExact || math.IsNaN(float64(sigma)) {
		return nil
	}
	s := float64(sigma)
	r := int(math.Ceil(s * 3))
	if r < 1 {
		return nil
	}
	k := make([]float32, 2*r+1)
	sum := 0.0
	for i := -r; i <= r; i++ {
		v := math.Exp(-float64(i*i) / (2 * s * s))
		k[i+r] = float32(v)
		sum += v
	}
	for i := range k {
		k[i] = float32(float64(k[i]) / sum)
	}
	return k
}

// boxWidth is the width of the boxes a deviation blurs through.
func boxWidth(sigma float32) int {
	if sigma <= 0 || math.IsNaN(float64(sigma)) {
		return 0
	}
	d := math.Floor(float64(sigma)*blurBox + 0.5)
	if d > 1<<20 {
		d = 1 << 20
	}
	return int(d)
}

// boxPasses is the lead and the trail of each of the three passes. An odd
// width centers all three on the pixel; an even one puts the first two on the
// boundaries either side and widens the third.
func boxPasses(d int) [3][2]int {
	if d <= 1 {
		return [3][2]int{}
	}
	if d%2 == 1 {
		r := (d - 1) / 2
		return [3][2]int{{r, r}, {r, r}, {r, r}}
	}
	h := d / 2
	return [3][2]int{{h, h - 1}, {h - 1, h}, {h, h}}
}

func boxLine(dst, src []uint8, n, stride, comps, lead, trail int) {
	if lead == 0 && trail == 0 {
		for i := 0; i < n; i++ {
			copy(dst[i*stride:i*stride+comps], src[i*stride:i*stride+comps])
		}
		return
	}
	w := lead + trail + 1
	half, width := int32(w/2), int32(w)
	var sum [5]int32
	step := func(i int, sign int32) {
		if i < 0 || i >= n {
			return
		}
		s := src[i*stride:]
		for c := 0; c < comps; c++ {
			sum[c] += sign * int32(s[c])
		}
	}
	for i := -lead; i <= trail; i++ {
		step(i, 1)
	}
	for x := 0; x < n; x++ {
		d := dst[x*stride:]
		for c := 0; c < comps; c++ {
			d[c] = uint8((sum[c] + half) / width)
		}
		step(x-lead, -1)
		step(x+trail+1, 1)
	}
}

func convLine(dst, src []uint8, n, stride, comps int, k []float32) {
	r := len(k) / 2
	var sum [5]float32
	for x := 0; x < n; x++ {
		sum = [5]float32{}
		lo, hi := max(0, x-r), min(n-1, x+r)
		for i := lo; i <= hi; i++ {
			w := k[i-x+r]
			s := src[i*stride:]
			for c := 0; c < comps; c++ {
				sum[c] += float32(w * float32(s[c]))
			}
		}
		d := dst[x*stride:]
		for c := 0; c < comps; c++ {
			d[c] = round255(sum[c])
		}
	}
}

func round255(v float32) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 255 {
		return 255
	}
	return uint8(v + 0.5)
}
