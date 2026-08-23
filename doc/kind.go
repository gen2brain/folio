package doc

import (
	"bytes"
	"unicode/utf8"
)

// kindOf reads the head of a file for what it is, and KindUnknown for none of
// the three. A file none of the magic numbers claims is a book when it reads
// as text, which is what a plain text or a loose HTML document is.
func kindOf(b []byte) Kind {
	if len(b) == 0 {
		return KindUnknown
	}
	if bytes.Contains(b, []byte("%PDF-")) {
		return KindPDF
	}
	switch {
	case len(b) >= 4 && string(b[:4]) == "PK\x03\x04",
		len(b) >= 68 && (string(b[60:68]) == "BOOKMOBI" || string(b[60:68]) == "TEXtREAd"),
		len(b) >= 4 && string(b[:4]) == "ITSF",
		bytes.Contains(b, []byte("<FictionBook")):
		return KindBook
	}
	if bytes.EqualFold(rootElement(b), []byte("svg")) {
		return KindSVG
	}
	if readsAsText(b) {
		return KindBook
	}
	return KindUnknown
}

// rootElement is the name of the first element of an XML or HTML file, which
// is as far in as the declaration, the doctype and the comments before it
// reach. It is nil for a file that begins with anything else.
func rootElement(b []byte) []byte {
	for {
		b = bytes.TrimLeft(b, " \t\r\n\f\v")
		if len(b) < 2 || b[0] != '<' {
			return nil
		}
		switch {
		case b[1] == '?': // <?xml ... ?>
			b = skipPast(b, "?>")
		case bytes.HasPrefix(b[1:], []byte("!--")):
			b = skipPast(b, "-->")
		case b[1] == '!': // <!DOCTYPE ...>
			b = skipPast(b, ">")
		default:
			name := b[1:]
			if i := bytes.IndexAny(name, " \t\r\n\f\v/>"); i >= 0 {
				name = name[:i]
			}
			return name
		}
		if b == nil {
			return nil
		}
	}
}

// skipPast is what follows the first sep in b, and nil when there is none.
func skipPast(b []byte, sep string) []byte {
	i := bytes.Index(b, []byte(sep))
	if i < 0 {
		return nil
	}
	return b[i+len(sep):]
}

// readsAsText reports bytes the html package will accept as a text document.
func readsAsText(b []byte) bool {
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			// A rune cut in half by the end of the head is not a binary file.
			return len(b) < utf8.UTFMax
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' {
			return false
		}
		b = b[n:]
	}
	return true
}
