package raster

import "math"

const maxPixmapBytes = 1 << 40

// Model is what raster needs to know about a color space: how many
// components it has, and how to get to and from RGB when something has to be
// shown or blended non separably.
type Model interface {
	Components() int
	ToRGB(dst, src []uint8)
	FromRGB(dst, src []uint8)
}

// Pixmap is an interleaved 8 bit image of N color components and an optional
// alpha channel, premultiplied when there is one. X and Y are where its top
// left sample sits, and everything that composites into it works in those
// coordinates rather than in the pixmap's own, so that a group covering part
// of a page needs no transform of its own.
type Pixmap struct {
	W, H    int
	N       int
	Alpha   bool
	Stride  int
	X, Y    int
	Samples []uint8
	Model   Model
}

// NewPixmap returns a zeroed pixmap, nil if the size does not fit in memory.
func NewPixmap(model Model, w, h int, alpha bool) *Pixmap {
	n := 0
	if model != nil {
		n = model.Components()
	}
	return newPixmap(model, n, w, h, alpha)
}

// NewMask returns an alpha only pixmap, the shape a clip mask and a glyph
// take.
func NewMask(w, h int) *Pixmap { return newPixmap(nil, 0, w, h, true) }

func newPixmap(model Model, n, w, h int, alpha bool) *Pixmap {
	if w < 0 || h < 0 || n < 0 {
		return nil
	}
	comps := n
	if alpha {
		comps++
	}
	if comps == 0 {
		return nil
	}
	size := int64(w) * int64(h) * int64(comps)
	if size > maxPixmapBytes || size > int64(math.MaxInt) {
		return nil
	}
	return &Pixmap{
		W: w, H: h, N: n, Alpha: alpha,
		Stride:  w * comps,
		Samples: make([]uint8, int(size)),
		Model:   model,
	}
}

// Coverage returns the first component of every pixel as an alpha only
// pixmap. A pixmap that already has one component is returned as it is.
func (p *Pixmap) Coverage() *Pixmap {
	n := p.Comps()
	if n == 1 {
		return p
	}
	out := NewMask(p.W, p.H)
	if out == nil {
		return nil
	}
	for y := 0; y < p.H; y++ {
		row, dst := p.Row(y), out.Row(y)
		for x := 0; x < p.W; x++ {
			dst[x] = row[x*n]
		}
	}
	return out
}

// Comps is how many bytes one pixel takes, which is the color components
// and the alpha channel when there is one.
func (p *Pixmap) Comps() int {
	if p.Alpha {
		return p.N + 1
	}
	return p.N
}

// Bounds is the pixmap's place in device space.
func (p *Pixmap) Bounds() Rect {
	return Rect{float32(p.X), float32(p.Y), float32(p.X + p.W), float32(p.Y + p.H)}
}

// Row returns the samples of one scanline.
func (p *Pixmap) Row(y int) []uint8 {
	return p.Samples[y*p.Stride : y*p.Stride+p.Stride]
}

// Clear sets every sample to zero, which is transparent when there is an
// alpha channel and black when there is not.
func (p *Pixmap) Clear() {
	clear(p.Samples)
}

// ClearWhite sets the pixmap to opaque white, the background a page is
// rendered onto.
func (p *Pixmap) ClearWhite() {
	n := p.Comps()
	if n == 0 || p.W == 0 || p.H == 0 {
		return
	}
	v := uint8(255)
	if p.N == 4 {
		v = 0
	}
	row := p.Row(0)
	for i := 0; i < p.N; i++ {
		row[i] = v
	}
	if p.Alpha {
		row[p.N] = 255
	}
	for done := n; done < p.W*n; {
		done += copy(row[done:p.W*n], row[:done])
	}
	for y := 1; y < p.H; y++ {
		copy(p.Row(y), row)
	}
}

// FillRect composites a paint over a rectangle, in the coordinates the
// pixmap's own X and Y place it in.
func (p *Pixmap) FillRect(x0, y0, x1, y1 int, paint Paint) {
	x0, y0 = max(x0, p.X), max(y0, p.Y)
	x1, y1 = min(x1, p.X+p.W), min(y1, p.Y+p.H)
	if x0 >= x1 || y0 >= y1 || paint.Alpha == 0 {
		return
	}
	b := p.Blitter(paint)
	for y := y0; y < y1; y++ {
		b.BlitSolid(x0, y, x1-x0, 255)
	}
}

var (
	// ModelGray, ModelRGB and ModelCMYK are the device spaces, here because
	// the non separable blend modes and the luminosity of a soft mask need
	// them without knowing anything about PDF.
	ModelGray Model = grayModel{}
	ModelRGB  Model = rgbModel{}
	ModelCMYK Model = cmykModel{}
)

type grayModel struct{}

func (grayModel) Components() int { return 1 }

func (grayModel) ToRGB(dst, src []uint8) {
	for i, v := range src {
		dst[i*3], dst[i*3+1], dst[i*3+2] = v, v, v
	}
}

func (grayModel) FromRGB(dst, src []uint8) {
	for i := range dst {
		r, g, b := uint32(src[i*3]), uint32(src[i*3+1]), uint32(src[i*3+2])
		dst[i] = uint8((r*77 + g*151 + b*28) >> 8)
	}
}

type rgbModel struct{}

func (rgbModel) Components() int          { return 3 }
func (rgbModel) ToRGB(dst, src []uint8)   { copy(dst, src) }
func (rgbModel) FromRGB(dst, src []uint8) { copy(dst, src) }

type cmykModel struct{}

func (cmykModel) Components() int { return 4 }

func (cmykModel) ToRGB(dst, src []uint8) {
	for i := 0; i+4 <= len(src); i += 4 {
		k := uint32(src[i+3])
		dst[i/4*3] = sub255(uint32(src[i]) + k)
		dst[i/4*3+1] = sub255(uint32(src[i+1]) + k)
		dst[i/4*3+2] = sub255(uint32(src[i+2]) + k)
	}
}

func (cmykModel) FromRGB(dst, src []uint8) {
	for i := 0; i+3 <= len(src); i += 3 {
		c := 255 - uint32(src[i])
		m := 255 - uint32(src[i+1])
		y := 255 - uint32(src[i+2])
		k := c
		if m < k {
			k = m
		}
		if y < k {
			k = y
		}
		o := i / 3 * 4
		dst[o], dst[o+1], dst[o+2], dst[o+3] = uint8(c-k), uint8(m-k), uint8(y-k), uint8(k)
	}
}

func sub255(v uint32) uint8 {
	if v > 255 {
		return 0
	}
	return uint8(255 - v)
}
