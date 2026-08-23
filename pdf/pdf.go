// Package pdf renders PDF documents.
//
//	doc, err := pdf.Open("file.pdf")
//	defer doc.Close()
//
//	p, err := doc.Page(0)
//	img, err := p.ImageDPI(150)
//	txt, err := p.Text()
//
// A page also reads as structured text, as its links, as SVG and as HTML,
// and [Page.Run] draws it through a [Device]. The device interface and
// the devices behind it live in the gfx package and are aliased here, so that
// rendering a page needs one import.
//
// A page composites in one color space throughout and converts once, at the
// public boundary, so a CMYK document renders to *image.CMYK.
//
// A damaged file renders the part that could be read. What went wrong is
// collected in [Document.Err] rather than returned, unless [Options.Strict]
// asks for the first of them to be.
//
// A [Document] may be rendered from several goroutines at once, one page
// each; a [Page] may not. Configuration belongs before they start.
package pdf

import (
	"github.com/gen2brain/folio/syntax"
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
