package html

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	xhtml "golang.org/x/net/html"
)

// textPath is what a plain text document calls its one part, and htmlPath
// what a loose markup file does.
const (
	textPath = "index.txt"
	htmlPath = "index.html"
)

// openText reads a file that is not a container: the bytes are the book,
// either as markup or as the text of one.
func openText(r io.ReaderAt, size int64) (*Document, error) {
	if size <= 0 || size > maxPartBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrInvalid, size)
	}
	b := make([]byte, size)
	if _, err := r.ReadAt(b, 0); err != nil && err != io.EOF {
		return nil, err
	}
	if !readsAsText(b) {
		return nil, fmt.Errorf("%w: not a book", ErrInvalid)
	}
	item := Item{Path: textPath, Type: "text/plain", Linear: true}
	kind := KindText
	if readsAsMarkup(b) {
		item = Item{Path: htmlPath, Type: "text/html", Linear: true}
		kind = KindHTML
	}
	d := &Document{
		kind:     kind,
		spine:    []Item{item},
		manifest: []Item{item},
		read: func(p string) ([]byte, error) {
			if p != item.Path {
				return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
			}
			return b, nil
		},
	}
	if kind == KindHTML {
		d.meta.Title = markupTitle(b)
	}
	return d, nil
}

// markupTags are the tags that say a file is markup rather than the text of
// one, from the sniffing rules of the WHATWG mime sniffing standard.
var markupTags = []string{
	"<!doctype html", "<html", "<head", "<script", "<iframe", "<h1", "<div",
	"<font", "<table", "<a", "<style", "<title", "<b", "<body", "<br", "<p",
	"<!--",
}

// readsAsMarkup reports a file that begins with a tag, which is what tells a
// loose HTML file from the plain text one it would otherwise be read as.
func readsAsMarkup(b []byte) bool {
	b = bytes.TrimLeft(b, " \t\n\r\f\v")
	if len(b) > 1024 {
		b = b[:1024]
	}
	for _, tag := range markupTags {
		if len(b) < len(tag) || !strings.EqualFold(string(b[:len(tag)]), tag) {
			continue
		}
		if len(b) == len(tag) {
			return true
		}
		switch c := b[len(tag)]; {
		case tag == "<!--", c == ' ', c == '\t', c == '\n', c == '\r', c == '\f', c == '>', c == '/':
			return true
		}
	}
	return false
}

// markupTitle is the title element of a loose markup file.
func markupTitle(b []byte) string {
	if len(b) > maxTitleScan {
		b = b[:maxTitleScan]
	}
	i := indexTag(b, "<title")
	if i < 0 {
		return ""
	}
	j := bytes.IndexByte(b[i:], '>')
	if j < 0 {
		return ""
	}
	rest := b[i+j+1:]
	k := indexTag(rest, "</title")
	if k < 0 {
		k = len(rest)
	}
	return strings.TrimSpace(xhtml.UnescapeString(string(rest[:k])))
}

// maxTitleScan bounds how far into a file the title is looked for.
const maxTitleScan = 64 << 10

// indexTag is where the named tag starts, whatever case it is written in.
func indexTag(b []byte, tag string) int {
	for i := 0; ; {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			return -1
		}
		i += j
		if len(b)-i >= len(tag) && strings.EqualFold(string(b[i:i+len(tag)]), tag) {
			return i
		}
		i++
	}
}

// readsAsText reports valid UTF-8 with no control characters in it.
func readsAsText(b []byte) bool {
	if len(b) >= 3 && b[0] == 0xef && b[1] == 0xbb && b[2] == 0xbf {
		return true
	}
	if len(b) > 4096 {
		b = b[:4096]
	}
	for len(b) > 0 {
		r, n := utf8.DecodeRune(b)
		if r == utf8.RuneError && n == 1 {
			return false
		}
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' && r != '\f' {
			return false
		}
		b = b[n:]
	}
	return true
}
