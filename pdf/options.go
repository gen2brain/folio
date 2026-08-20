package pdf

// DefaultPixelLimit is how many pixels a page may allocate when Options says
// nothing, the same number and the same name as in the sibling codecs.
const DefaultPixelLimit = 1 << 28

// Options control rendering. A nil *Options means the defaults: DeviceRGB,
// no alpha channel, and a page composited onto white.
type Options struct {
	// ColorSpace is what the page composites in and what the result holds.
	// Nil means DeviceRGB. It must have one, three or four components.
	ColorSpace *ColorSpace

	// Alpha adds an alpha channel, and the page then starts transparent
	// rather than white.
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

func (o *Options) flatness() float32 {
	if o == nil {
		return 0
	}
	return o.Flatness
}
