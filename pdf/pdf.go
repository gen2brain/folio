// Package pdf renders PDF documents.
//
// A document is opened from a file, a reader or a buffer, and a page is
// rendered at a resolution in dots per inch:
//
//	doc, err := pdf.Open("file.pdf")
//	defer doc.Close()
//
//	p, err := doc.Page(0)
//	img, err := p.ImageDPI(150)
//
// ImageDPI returns *image.RGBA, and Image returns the page at the resolution
// its own content is in, which for a scan is the resolution of the scan.
// ImageOptions renders into the color space the caller asks for and returns
// the standard library type that matches, Render returns the pixmap itself,
// and RenderTo draws into a destination the caller owns. A page composites in
// one color space throughout and converts once, at that boundary, so a CMYK
// document can come back as *image.CMYK.
//
// # Text, links and SVG
//
// A page is more than a picture. Text returns what it says, StructuredText
// returns the same with the box every character, line and block occupies,
// Links returns where its link annotations lead, and WriteSVG writes it as
// SVG:
//
//	txt, err := p.Text()
//	st, err := p.StructuredText()
//	for _, l := range p.Links() {
//		fmt.Println(l.Rect, l.URI)
//	}
//
// All four work in page space at 72 dots per inch, with y counting down from
// the top left.
//
// What the document says about itself is Outline, which is the tree a viewer
// shows as a table of contents, Metadata, and PageLabels, which is what a
// viewer numbers the pages with when a preface counts in roman numerals.
//
// # Devices
//
// Everything a page draws goes through Device, which Page.Run takes. The
// renderer is one implementation; text extraction, SVG, a trace and a
// bounding box are others, and embedding BaseDevice is how to write another.
// The interface and the devices are in the gfx package, which knows nothing
// about PDF, and are aliased here so that one import is enough.
//
// A font the file does not embed is substituted: one of the base fourteen for
// a Latin face, and one the machine has for a character collection they have
// no glyphs for. SetSystemFonts(false) turns the second off, which makes a
// render depend on nothing outside the file.
//
// A damaged file renders the part that could be read. What went wrong on the
// way is collected in Document.Err rather than returned, unless
// Options.Strict asks for the first of them.
//
// # Concurrency
//
// A Document may be rendered from several goroutines at once, one page each.
// A Page may not: give each goroutine its own. Configuration - layers, usage,
// the image decoder - belongs before the goroutines start.
//
//	for i := range doc.NumPages() {
//		go func() {
//			p, err := doc.Page(i)
//			img, err := p.ImageDPI(150)
//		}()
//	}
//
// One page may instead be drawn in horizontal bands by several goroutines,
// which is worth it when the page is large:
//
//	px, err := p.Render(p.Matrix(300), &pdf.Options{Threads: 4})
//
// # Layers and usage
//
// Optional content is a list the caller may edit, and the usage decides which
// annotations and which layers a file means for the screen and which for
// paper:
//
//	layers := doc.Layers()
//	layers[0].On = false
//	doc.SetLayers(layers)
//
//	doc.SetUsage(pdf.UsagePrint)
package pdf

import (
	"github.com/gen2brain/pdf/syntax"
)

// The object model comes from the syntax package. It is aliased here so that
// callers reaching into a page dictionary need only one import.
type (
	// Object is a PDF object: Bool, Integer, Real, String, Name, Array, Dict,
	// Ref or *Stream. A nil Object is null.
	Object = syntax.Object
	// Bool is a PDF boolean.
	Bool = syntax.Bool
	// Integer is a PDF integer.
	Integer = syntax.Integer
	// Real is a PDF real number.
	Real = syntax.Real
	// String is a PDF string.
	String = syntax.String
	// Name is a PDF name.
	Name = syntax.Name
	// Array is a PDF array.
	Array = syntax.Array
	// Dict is a PDF dictionary.
	Dict = syntax.Dict
	// Ref is an indirect reference.
	Ref = syntax.Ref
	// Stream is a PDF stream.
	Stream = syntax.Stream
)

// Errors returned by this package. Anything a damaged file does that can be
// worked around is recorded in Document.Err instead.
var (
	// ErrInvalid means the file is not a PDF, or is damaged past recovery.
	ErrInvalid = syntax.ErrInvalid
	// ErrUnsupported means the file is well formed but uses something this
	// package cannot handle.
	ErrUnsupported = syntax.ErrUnsupported
	// ErrPassword means the document is encrypted and the password is wrong.
	ErrPassword = syntax.ErrPassword
)
