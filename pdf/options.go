package pdf

import "runtime"

// DefaultPixelLimit is how many pixels a page may allocate when Options says
// nothing, the same number and the same name as in the sibling codecs.
const DefaultPixelLimit = 1 << 28

// Options control rendering. A nil *Options means the defaults: DeviceRGB,
// no alpha channel, and a page composited onto white.
type Options struct {
	// ColorSpace is what the page composites in and what the result holds.
	// Nil means DeviceRGB. It must have one, three or four components.
	ColorSpace *ColorSpace

	// Alpha adds an alpha channel, and the page starts transparent, not white.
	Alpha bool

	// PixelLimit bounds the area a page may allocate, in pixels. Zero means
	// DefaultPixelLimit, negative means no limit.
	PixelLimit int

	// Strict turns the first error recorded while interpreting the page into
	// an error returned by Render. Without it a damaged page returns the part
	// that worked, and the errors are on the Document.
	Strict bool

	// Flatness is how far a flattened curve may stray from the true one, in
	// device pixels. Zero means the default.
	Flatness float32

	// Threads is how many goroutines rasterize one page. Zero and one mean
	// the page is drawn as it is interpreted; more means it is interpreted
	// once into a display list and then drawn into that many horizontal
	// bands at the same time. Negative means one for every processor.
	//
	// It is worth setting only for a page big enough that drawing it costs
	// more than reading it. Rendering several pages at once, a goroutine
	// each, scales better and needs nothing set here.
	//
	// A band clips what crosses its edge, and the crossing point rounds to
	// the rasterizer's 1/256 of a pixel, so a banded page may differ from
	// the same page drawn whole by one coverage unit along such an edge.
	Threads int
}

func (o *Options) colorSpace() *ColorSpace {
	if o == nil || o.ColorSpace == nil {
		return DeviceRGB
	}
	return o.ColorSpace
}

func (o *Options) alpha() bool { return o != nil && o.Alpha }

func (o *Options) pixelLimit() int {
	if o == nil || o.PixelLimit == 0 {
		return DefaultPixelLimit
	}
	return o.PixelLimit
}

func (o *Options) strict() bool { return o != nil && o.Strict }

// threads is how many bands to split a page into, at least one.
func (o *Options) threads() int {
	if o == nil || o.Threads == 0 {
		return 1
	}
	if o.Threads < 0 {
		return runtime.GOMAXPROCS(0)
	}
	return o.Threads
}

func (o *Options) flatness() float32 {
	if o == nil {
		return 0
	}
	return o.Flatness
}
