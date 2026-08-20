// Package pdf renders PDF documents.
//
// A document is opened from a file, a reader or a buffer, and a page is
// rendered at a resolution in dots per inch:
//
//	doc, err := pdf.Open("file.pdf")
//	defer doc.Close()
//
//	p, err := doc.Page(0)
//	img, err := p.Image(150)
//
// Image returns *image.RGBA. ImageOptions renders into the color space the
// caller asks for and returns the standard library type that matches, Render
// returns the pixmap itself, and RenderTo draws into a destination the caller
// owns. A page composites in one color space throughout and converts once,
// at that boundary, so a CMYK document can come back as *image.CMYK.
//
// Everything a page draws goes through Device, which Page.Run takes. The
// renderer is one implementation; a trace and a bounding box are others, and
// embedding BaseDevice is how to write another.
//
// A damaged file renders the part that could be read. What went wrong on the
// way is collected in Document.Err rather than returned, unless
// Options.Strict asks for the first of them.
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
