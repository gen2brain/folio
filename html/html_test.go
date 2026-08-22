package html

import (
	"archive/zip"
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"
)

// buildEPUB writes the parts into the archive an EPUB is, mimetype first and
// stored, which is what the specification asks for and what a reader sniffs.
func buildEPUB(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	mt, err := z.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		t.Fatal(err)
	}
	mt.Write([]byte("application/epub+zip"))
	names := make([]string, 0, len(parts))
	for name := range parts {
		names = append(names, name)
	}
	for _, name := range names {
		w, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(parts[name]))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const container = `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container" version="1.0">
 <rootfiles><rootfile full-path="EPUB/package.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`

const pkg = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:identifier id="other">wrong</dc:identifier>
  <dc:identifier id="pub">urn:uuid:1234</dc:identifier>
  <dc:title>A Book</dc:title>
  <dc:creator>One Author</dc:creator>
  <dc:creator>Another Author</dc:creator>
  <dc:language>en</dc:language>
  <dc:date>2019-04-01</dc:date>
  <dc:subject>Testing</dc:subject>
  <meta property="dcterms:modified">2020-02-03T04:05:06Z</meta>
 </metadata>
 <manifest>
  <item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>
  <item id="one" href="text/one.xhtml" media-type="application/xhtml+xml"/>
  <item id="two" href="text/two.xhtml" media-type="application/xhtml+xml"/>
  <item id="cover" href="images/cover.png" media-type="image/png"/>
 </manifest>
 <spine>
  <itemref idref="one"/>
  <itemref idref="two"/>
  <itemref idref="cover" linear="no"/>
 </spine>
</package>`

// The nav is wrapped in a section and its titles are wrapped in spans, which
// is what real books do and what a shape matcher misses.
const nav = `<?xml version="1.0"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops">
<body><section>
 <nav epub:type="landmarks"><ol><li><a href="text/one.xhtml">Start</a></li></ol></nav>
 <nav epub:type="toc"><h2>Contents</h2><ol>
  <li><a href="text/one.xhtml"><span>One</span></a>
   <ol><li><a href="text/one.xhtml#deep">Deeper</a></li></ol></li>
  <li><a href="text/two.xhtml">Two</a></li>
 </ol></nav>
</section></body></html>`

func openBook(t *testing.T, parts map[string]string) *Document {
	t.Helper()
	d, err := Load(buildEPUB(t, parts))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func fullBook(t *testing.T) *Document {
	return openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html><body><p>One</p></body></html>",
		"EPUB/text/two.xhtml":    "<html><body><p>Two</p></body></html>",
		"EPUB/images/cover.png":  "\x89PNG",
	})
}

func TestEPUBMetadata(t *testing.T) {
	m := fullBook(t).Metadata()
	if m.Title != "A Book" {
		t.Fatalf("title = %q", m.Title)
	}
	if m.Author != "One Author, Another Author" {
		t.Fatalf("author = %q", m.Author)
	}
	if m.Identifier != "urn:uuid:1234" {
		t.Fatalf("identifier = %q, want the one the package says is unique", m.Identifier)
	}
	if m.Language != "en" || len(m.Subjects) != 1 {
		t.Fatalf("metadata = %+v", m)
	}
	if got := m.Created.Format("2006-01-02"); got != "2019-04-01" {
		t.Fatalf("created = %v", got)
	}
	if got := m.Modified.Format("2006-01-02"); got != "2020-02-03" {
		t.Fatalf("modified = %v", got)
	}
}

func TestEPUBSpine(t *testing.T) {
	d := fullBook(t)
	if d.Kind() != KindEPUB {
		t.Fatalf("kind = %v", d.Kind())
	}
	spine := d.Spine()
	if len(spine) != 3 {
		t.Fatalf("spine = %+v", spine)
	}
	if !spine[0].Linear || spine[2].Linear {
		t.Fatalf("the package marks the cover auxiliary: %+v", spine)
	}
	if spine[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("path = %q, want it relative to the archive", spine[0].Path)
	}
	if len(d.Manifest()) != 4 {
		t.Fatalf("manifest has %d items, want 4", len(d.Manifest()))
	}
	b, err := d.Read(spine[1].Path)
	if err != nil || !strings.Contains(string(b), "Two") {
		t.Fatalf("read = %q, %v", b, err)
	}
	if _, err := d.Read("EPUB/nothing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("reading a part that is not there = %v", err)
	}
}

func TestEPUBOutline(t *testing.T) {
	o := fullBook(t).Outline()
	if len(o) != 2 {
		t.Fatalf("outline = %+v", o)
	}
	if o[0].Title != "One" {
		t.Fatalf("a title wrapped in a span came out %q", o[0].Title)
	}
	if o[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("path = %q", o[0].Path)
	}
	if len(o[0].Children) != 1 || o[0].Children[0].Fragment != "deep" {
		t.Fatalf("children = %+v", o[0].Children)
	}
	if o[1].Title != "Two" {
		t.Fatalf("second = %+v", o[1])
	}
}

// TestEPUBNCX checks the EPUB 2 table of contents, which a book carries
// instead of a navigation document or as well as one.
func TestEPUBNCX(t *testing.T) {
	pkg2 := strings.Replace(pkg,
		`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`,
		`<item id="ncx" href="toc.ncx" media-type="application/x-dtbncx+xml"/>`, 1)
	pkg2 = strings.Replace(pkg2, `<spine>`, `<spine toc="ncx">`, 1)
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg2,
		"EPUB/text/one.xhtml":    "<html/>",
		"EPUB/text/two.xhtml":    "<html/>",
		"EPUB/toc.ncx": `<?xml version="1.0"?><ncx xmlns="http://www.daisy.org/z3986/2005/ncx/">
			<navMap>
			 <navPoint><navLabel><text>Chapter One</text></navLabel><content src="text/one.xhtml"/>
			  <navPoint><navLabel><text>Inside</text></navLabel><content src="text/one.xhtml#in"/></navPoint>
			 </navPoint>
			</navMap></ncx>`,
	})
	o := d.Outline()
	if len(o) != 1 || o[0].Title != "Chapter One" || o[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("outline = %+v", o)
	}
	if len(o[0].Children) != 1 || o[0].Children[0].Fragment != "in" {
		t.Fatalf("children = %+v", o[0].Children)
	}
}

// TestEPUBNoSpine checks a package that lists its chapters and forgets to put
// them in the spine, which is still a book worth reading.
func TestEPUBNoSpine(t *testing.T) {
	pkg2 := strings.Replace(pkg, `<itemref idref="one"/>`, "", 1)
	pkg2 = strings.Replace(pkg2, `<itemref idref="two"/>`, "", 1)
	pkg2 = strings.Replace(pkg2, `<itemref idref="cover" linear="no"/>`, "", 1)
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg2,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html/>",
		"EPUB/text/two.xhtml":    "<html/>",
	})
	// The navigation document is a chapter by media type, so three.
	if got := len(d.Spine()); got != 3 {
		t.Fatalf("spine has %d parts, want the chapters of the manifest", got)
	}
}

func TestEPUBBroken(t *testing.T) {
	cases := []struct {
		name  string
		parts map[string]string
	}{
		{"no container", map[string]string{"EPUB/package.opf": pkg}},
		{"no package", map[string]string{"META-INF/container.xml": container}},
		{"container names nothing", map[string]string{
			"META-INF/container.xml": `<container><rootfiles/></container>`,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Load(buildEPUB(t, tc.parts)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("err = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestText(t *testing.T) {
	d, err := Load([]byte("Call me Ishmael.\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Kind() != KindText {
		t.Fatalf("kind = %v", d.Kind())
	}
	if len(d.Spine()) != 1 {
		t.Fatalf("spine = %+v", d.Spine())
	}
	b, err := d.Read(d.Spine()[0].Path)
	if err != nil || string(b) != "Call me Ishmael.\n" {
		t.Fatalf("read = %q, %v", b, err)
	}
}

func TestNotABook(t *testing.T) {
	if _, err := Load([]byte{0x00, 0x01, 0x02, 0xff}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if _, err := Load(nil); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty = %v, want ErrInvalid", err)
	}
}

func TestResolve(t *testing.T) {
	cases := []struct{ base, ref, want string }{
		{"EPUB/text/one.xhtml", "two.xhtml", "EPUB/text/two.xhtml"},
		{"EPUB/text/one.xhtml", "../images/a.png", "EPUB/images/a.png"},
		{"EPUB/text/one.xhtml", "#frag", "EPUB/text/one.xhtml"},
		{"EPUB/text/one.xhtml", "two.xhtml#frag", "EPUB/text/two.xhtml"},
		{"EPUB/text/one.xhtml", "/EPUB/a.css", "EPUB/a.css"},
		{"one.xhtml", "https://example.com/x", "https://example.com/x"},
	}
	for _, tc := range cases {
		if got := Resolve(tc.base, tc.ref); got != tc.want {
			t.Fatalf("Resolve(%q, %q) = %q, want %q", tc.base, tc.ref, got, tc.want)
		}
	}
}

// FuzzBook opens arbitrary bytes as a book and then asks it everything: the
// metadata, the spine, the outline and every part. None of it may panic.
func FuzzBook(fu *testing.F) {
	fu.Add(buildEPUB(&testing.T{}, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    "<html/>",
	}))
	fu.Add([]byte("plain text"))
	fu.Add(buildMOBI([]byte("<html><body>Hi</body></html>"), []byte{0x81}, []byte("\x89PNG\r\n\x1a\n0123456789")))
	fu.Fuzz(func(t *testing.T, b []byte) {
		d, err := Load(b)
		if err != nil {
			return
		}
		defer d.Close()
		d.Metadata()
		d.Text()
		for _, it := range d.Spine() {
			d.Read(it.Path)
			d.ParsePart(it.Path)
		}
		for _, it := range d.Manifest() {
			d.Read(it.Path)
		}
		var walk func([]Outline, int)
		walk = func(o []Outline, depth int) {
			if depth > 64 {
				t.Fatal("outline nested past the guard")
			}
			for _, e := range o {
				walk(e.Children, depth+1)
			}
		}
		walk(d.Outline(), 0)
	})
}

// buildMOBI writes the smallest PalmDB that is a book: a header, a record
// list, the record that describes the rest, one compressed text record and
// one picture.
func buildMOBI(text []byte, trailing []byte, image []byte) []byte {
	rec0 := make([]byte, 16+232)
	be16(rec0[0:], 2)                 // PalmDOC LZ77
	be32(rec0[4:], uint32(len(text))) // text length
	be16(rec0[8:], 1)                 // one text record
	be16(rec0[10:], 4096)             // record size
	copy(rec0[16:], "MOBI")
	be32(rec0[20:], 232)              // header length
	be32(rec0[28:], 65001)            // UTF-8
	be32(rec0[16+0x50-16:], 2)        // first non-book index
	be32(rec0[0x54:], uint32(16+232)) // the full name follows the header
	be32(rec0[0x58:], uint32(len("A Title")))
	be32(rec0[0x80:], 0x40) // EXTH is there
	if len(trailing) > 0 {
		be16(rec0[0xf2:], 2)
	}

	var exth []byte
	add := func(kind int, v string) {
		e := make([]byte, 8+len(v))
		be32(e, uint32(kind))
		be32(e[4:], uint32(len(e)))
		copy(e[8:], v)
		exth = append(exth, e...)
	}
	add(100, "An Author")
	add(101, "A Publisher")
	add(105, "A Subject")
	head := make([]byte, 12)
	copy(head, "EXTH")
	be32(head[4:], uint32(12+len(exth)))
	be32(head[8:], 3)
	rec0 = append(rec0, append(head, exth...)...)
	be32(rec0[0x54:], uint32(len(rec0)))
	rec0 = append(rec0, "A Title"...)

	var comp []byte
	for i := 0; i < len(text); i += 8 {
		n := min(8, len(text)-i)
		comp = append(comp, byte(n))
		comp = append(comp, text[i:i+n]...)
	}
	comp = append(comp, trailing...)

	recs := [][]byte{rec0, comp}
	if image != nil {
		recs = append(recs, image)
	}
	head2 := make([]byte, 78+8*len(recs))
	copy(head2, "A Book")
	copy(head2[60:], "BOOKMOBI")
	be16(head2[76:], uint16(len(recs)))
	off := len(head2)
	for i, r := range recs {
		be32(head2[78+8*i:], uint32(off))
		off += len(r)
	}
	out := head2
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

func be16(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

func be32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func TestMOBI(t *testing.T) {
	png := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, 16)...)
	d, err := Load(buildMOBI([]byte("<html><body><p>Hello</p></body></html>"), nil, png))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if d.Kind() != KindMOBI {
		t.Fatalf("kind = %v", d.Kind())
	}
	m := d.Metadata()
	if m.Title != "A Title" || m.Author != "An Author" || m.Publisher != "A Publisher" {
		t.Fatalf("metadata = %+v", m)
	}
	if len(m.Subjects) != 1 || m.Subjects[0] != "A Subject" {
		t.Fatalf("subjects = %v", m.Subjects)
	}
	b, err := d.Read("index.html")
	if err != nil || string(b) != "<html><body><p>Hello</p></body></html>" {
		t.Fatalf("text = %q, %v", b, err)
	}
	// The picture is a part named by the number the HTML refers to it by.
	if len(d.Manifest()) != 2 || d.Manifest()[1].Path != "00001" {
		t.Fatalf("manifest = %+v", d.Manifest())
	}
	if got, _ := d.Read("00001"); string(got) != string(png) {
		t.Fatalf("the picture came back as %d bytes", len(got))
	}
}

// TestMOBITrailing checks that the entries a record carries after its data
// are taken off, which is what tells the text from the index behind it.
func TestMOBITrailing(t *testing.T) {
	// One trailing entry of four bytes, its own size written backwards in the
	// last byte with the top bit marking where it ends.
	trailing := []byte{'x', 'y', 'z', 0x80 | 4}
	d, err := Load(buildMOBI([]byte("Hello"), trailing, nil))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if b, _ := d.Read("index.html"); string(b) != "Hello" {
		t.Fatalf("text = %q, want the trailing entry gone", b)
	}
}

// TestMOBIRepeat checks the two byte form of the compression, which points
// back into what has already been written and may run past the end of it.
func TestMOBIRepeat(t *testing.T) {
	rec := []byte{2, 'a', 'b', 0x80, byte(2<<3 | (6 - 3))}
	if got := string(palmDoc(nil, rec)); got != "abababab" {
		t.Fatalf("palmDoc = %q, want abababab", got)
	}
}

func TestCP1252(t *testing.T) {
	if got := string(fromCP1252([]byte{'a', 0x92, 'b', 0xe9})); got != "a’bé" {
		t.Fatalf("fromCP1252 = %q", got)
	}
}

func TestNodeText(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>One</p><p>Two</p>", "One\nTwo"},
		{"<p>One <b>two</b> three</p>", "One two three"},
		{"<p>One<br>Two</p>", "One\nTwo"},
		{"<head><title>T</title></head><body><p>Hi</p>", "Hi"},
		{"<p>Hi</p><script>var x = 1</script><style>p{}</style>", "Hi"},
		{"<ul><li>One</li><li>Two</li></ul>", "One\nTwo"},
		{"<p>  lots   of\n space  </p>", "lots of space"},
		{"<pre>kept\n  as is</pre>", "kept\n  as is"},
		{"<p><ruby>山路<rp>(</rp><rt>やまみち</rt><rp>)</rp></ruby></p>", "山路やまみち"},
		{"<p>日本<span>\n</span>語</p>", "日本語"},
		{"<p>one<span>\n</span>two</p>", "one two"},
	}
	for _, tc := range cases {
		root, err := Parse([]byte("<html><body>" + tc.in + "</body></html>"))
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.TrimSpace(NodeText(root)); got != tc.want {
			t.Fatalf("NodeText(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDocumentText(t *testing.T) {
	d := fullBook(t)
	got, err := d.Text()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "One") || !strings.Contains(got, "Two") {
		t.Fatalf("text = %q", got)
	}
	// The cover is a picture in the spine and has no text in it.
	if strings.Contains(got, "PNG") {
		t.Fatalf("a picture in the spine reached the text: %q", got)
	}
}

// TestOutlineFromHeadings checks the table of contents a book with none gets.
func TestOutlineFromHeadings(t *testing.T) {
	pkg2 := strings.Replace(pkg,
		`<item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/>`,
		"", 1)
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg2,
		"EPUB/text/one.xhtml": "<html><body><h1 id='a'>One</h1><h2>Under one</h2>" +
			"<h2>Also under</h2><h1>Two</h1></body></html>",
		"EPUB/text/two.xhtml": "<html><body><h1>Three</h1></body></html>",
	})
	o := d.Outline()
	if len(o) != 3 {
		t.Fatalf("outline = %+v", o)
	}
	if o[0].Title != "One" || o[0].Fragment != "a" || o[0].Path != "EPUB/text/one.xhtml" {
		t.Fatalf("first = %+v", o[0])
	}
	if len(o[0].Children) != 2 || o[0].Children[1].Title != "Also under" {
		t.Fatalf("children = %+v", o[0].Children)
	}
	if o[2].Title != "Three" || len(o[2].Children) != 0 {
		t.Fatalf("third = %+v", o[2])
	}
}

func TestCSSTokens(t *testing.T) {
	cases := []struct {
		in   string
		want []cssToken
	}{
		{`p`, []cssToken{{kind: cssIdent, value: "p"}}},
		{`/* c */p`, []cssToken{{kind: cssIdent, value: "p"}}},
		{`#a1`, []cssToken{{kind: cssHash, value: "a1", id: true}}},
		{`#123`, []cssToken{{kind: cssHash, value: "123"}}},
		{`@media`, []cssToken{{kind: cssAtKeyword, value: "media"}}},
		{`12px`, []cssToken{{kind: cssDimension, num: 12, unit: "px", integer: true}}},
		{`-1.5E2%`, []cssToken{{kind: cssPercentage, num: -150, signed: true}}},
		{`+3`, []cssToken{{kind: cssNumber, num: 3, integer: true, signed: true}}},
		{`"a\"b"`, []cssToken{{kind: cssString, value: `a"b`}}},
		{"'a\nb'", []cssToken{{kind: cssBadString}, {kind: cssSpace}, {kind: cssIdent, value: "b"}, {kind: cssString}}},
		{`url( a.css )`, []cssToken{{kind: cssURL, value: "a.css"}}},
		{`url("a.css")`, []cssToken{{kind: cssFunction, value: "url"}, {kind: cssString, value: "a.css"}, {kind: cssCloseParen}}},
		{`url(a"b)`, []cssToken{{kind: cssBadURL}}},
		{`\41 b`, []cssToken{{kind: cssIdent, value: "Ab"}}},
		{`--x`, []cssToken{{kind: cssIdent, value: "--x"}}},
		{`<!--a -->`, []cssToken{{kind: cssCDO}, {kind: cssIdent, value: "a"}, {kind: cssSpace}, {kind: cssCDC}}},
		{`a-->`, []cssToken{{kind: cssIdent, value: "a--"}, {kind: cssDelim, delim: '>'}}},
		{"\r\n\tp", []cssToken{{kind: cssSpace}, {kind: cssIdent, value: "p"}}},
	}
	for _, tc := range cases {
		l := newCSSLexer(tc.in)
		var got []cssToken
		for {
			tok := l.next()
			if tok.kind == cssEOF {
				break
			}
			got = append(got, tok)
		}
		if len(got) != len(tc.want) {
			t.Fatalf("%q gave %d tokens %+v, want %d", tc.in, len(got), got, len(tc.want))
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Fatalf("%q token %d = %+v, want %+v", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

const selDoc = `<html><body>
<div id="d1" class="box wide" lang="en-GB">
  <p id="p1" class="first">One</p>
  <p id="p2">Two <em id="e1">stress</em></p>
  <span id="s1">Three</span>
</div>
<div id="d2"><a id="a1" href="x.html" title="t">Link</a></div>
</body></html>`

// matchIDs returns the identifiers of every element a selector matches, in
// document order, which is what a selector table is checked against.
func matchIDs(t *testing.T, sel string) []string {
	t.Helper()
	root, err := Parse([]byte(selDoc))
	if err != nil {
		t.Fatal(err)
	}
	sels, ok := parseSelectors(skipSpace(tokensOf(sel)))
	if !ok {
		t.Fatalf("%q did not parse", sel)
	}
	var got []string
	Walk(root, func(n *Node) bool {
		for _, s := range sels {
			if s.Match(n) {
				got = append(got, Attr(n, "id"))
				break
			}
		}
		return true
	})
	return got
}

func TestCSSSelectors(t *testing.T) {
	cases := []struct {
		sel  string
		want string
	}{
		{`p`, "p1 p2"},
		{`*`, "   d1 p1 p2 e1 s1 d2 a1"},
		{`.first`, "p1"},
		{`#p2`, "p2"},
		{`div p`, "p1 p2"},
		{`div > p`, "p1 p2"},
		{`body > p`, ""},
		{`p + p`, "p2"},
		{`p ~ span`, "s1"},
		{`p + span`, "s1"},
		{`div.box p`, "p1 p2"},
		{`div.box.wide`, "d1"},
		{`div.box.narrow`, ""},
		{`p:first-child`, "p1"},
		{`p:last-child`, ""},
		{`span:last-child`, "s1"},
		{`p:not(.first)`, "p2"},
		{`div:not(#d1)`, "d2"},
		{`div :nth-child(2)`, "p2"},
		{`div :nth-child(odd)`, "p1 e1 s1 a1"},
		{`div :nth-last-child(1)`, "e1 s1 a1"},
		{`p:nth-of-type(2)`, "p2"},
		{`em:only-of-type`, "e1"},
		{`[href]`, "a1"},
		{`[href="x.html"]`, "a1"},
		{`[href^="x."]`, "a1"},
		{`[href$=".html"]`, "a1"},
		{`[href*="."]`, "a1"},
		{`[class~="wide"]`, "d1"},
		{`[lang|="en"]`, "d1"},
		{`[title="T" i]`, "a1"},
		{`[title="T"]`, ""},
		{`a:hover`, ""},
		{`a:link`, ""},
		{`p::before`, ""},
		{`p:before`, ""},
		{`p::first-line`, ""},
		{`p::before em`, ""},
		{`p, span`, "p1 p2 s1"},
		{`html|p`, "p1 p2"},
		{`*|p`, "p1 p2"},
	}
	for _, tc := range cases {
		got := strings.Join(matchIDs(t, tc.sel), " ")
		if got != tc.want {
			t.Errorf("%q matched %q, want %q", tc.sel, got, tc.want)
		}
	}
}

// TestCSSNamespacedAttr checks the one namespaced attribute a book writes.
// An XHTML part parsed as HTML keeps the prefix in the attribute name, so a
// selector for it has to look for both spellings.
func TestCSSNamespacedAttr(t *testing.T) {
	root, err := Parse([]byte(`<html xmlns:epub="http://www.idpf.org/2007/ops"><body>` +
		`<div epub:type="chapter" id="i">x</div><div id="j">y</div></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	var el, other *Node
	Walk(root, func(n *Node) bool {
		switch Attr(n, "id") {
		case "i":
			el = n
		case "j":
			other = n
		}
		return true
	})
	if el == nil || other == nil {
		t.Fatal("no divs")
	}
	for _, tc := range []struct {
		sel  string
		want bool
	}{
		{`div[epub|type="chapter"]`, true},
		{`[epub|type]`, true},
		{`div[epub|type="other"]`, false},
		{`[type]`, false},
	} {
		sels, ok := parseSelectors(skipSpace(tokensOf(tc.sel)))
		if !ok {
			t.Errorf("%q did not parse", tc.sel)
			continue
		}
		if got := sels[0].Match(el); got != tc.want {
			t.Errorf("%q matched %v, want %v", tc.sel, got, tc.want)
		}
		if sels[0].Match(other) {
			t.Errorf("%q matched the div with no attribute", tc.sel)
		}
	}
}

func TestCSSSelectorErrors(t *testing.T) {
	for _, sel := range []string{``, `p >`, `> p`, `p ,`, `.`, `#`, `p..`, `[`, `[a=]`, `[a~]`, `p:not()`, `p:nth-child()`, `p q!`} {
		if _, ok := parseSelectors(skipSpace(tokensOf(sel))); ok {
			t.Errorf("%q parsed and should not have", sel)
		}
	}
}

func TestCSSSpecificity(t *testing.T) {
	cases := []struct {
		sel     string
		a, b, c int
	}{
		{`*`, 0, 0, 0},
		{`p`, 0, 0, 1},
		{`.x`, 0, 1, 0},
		{`#x`, 1, 0, 0},
		{`div p`, 0, 0, 2},
		{`div.x > p#y`, 1, 1, 2},
		{`[href]`, 0, 1, 0},
		{`p:first-child`, 0, 1, 1},
		{`p:not(.x)`, 0, 1, 1},
		{`p::before`, 0, 0, 2},
		{`a:hover`, 0, 1, 1},
	}
	for _, tc := range cases {
		s, ok := parseSelectors(skipSpace(tokensOf(tc.sel)))
		if !ok {
			t.Fatalf("%q did not parse", tc.sel)
		}
		if want := specificity(tc.a, tc.b, tc.c); s[0].Spec() != want {
			t.Errorf("%q specificity = %d, want %d", tc.sel, s[0].Spec(), want)
		}
	}
}

// styleOf computes the style of the one element of a fragment that carries an
// identifier.
func styleOf(t *testing.T, sheet, body, id string) *Style {
	t.Helper()
	root, err := Parse([]byte("<html><body>" + body + "</body></html>"))
	if err != nil {
		t.Fatal(err)
	}
	s := ParseCSS([]byte(sheet), OriginAuthor)
	if len(s.Errors) > 0 {
		t.Fatalf("sheet %q: %v", sheet, s.Errors)
	}
	st := Cascade(root, Media{Width: 600, Height: 800, FontSize: 16}, UserAgent(), s)
	var out *Style
	Walk(root, func(n *Node) bool {
		if Attr(n, "id") == id {
			out = st.Of(n)
		}
		return true
	})
	if out == nil {
		t.Fatalf("no element with id %q", id)
	}
	return out
}

func TestCSSCascade(t *testing.T) {
	s := styleOf(t, `p { color: red } .x { color: green } #i { color: blue }`,
		`<p id="i" class="x">t</p>`, "i")
	if s.Color != (Color{0, 0, 255, 255}) {
		t.Errorf("identifier lost to a class: %v", s.Color)
	}

	s = styleOf(t, `#i { color: blue } .x { color: green !important }`,
		`<p id="i" class="x">t</p>`, "i")
	if s.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("important lost to specificity: %v", s.Color)
	}

	s = styleOf(t, `p { color: red } p { color: green }`, `<p id="i">t</p>`, "i")
	if s.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("the later of two equal rules lost: %v", s.Color)
	}

	s = styleOf(t, `p { color: red }`, `<p id="i" style="color: green">t</p>`, "i")
	if s.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("the style attribute lost to a sheet: %v", s.Color)
	}

	s = styleOf(t, `p { color: red !important }`, `<p id="i" style="color: green">t</p>`, "i")
	if s.Color != (Color{255, 0, 0, 255}) {
		t.Errorf("an important rule lost to the style attribute: %v", s.Color)
	}
	s = styleOf(t, `p { color: red !important }`, `<p id="i" style="color: green !important">t</p>`, "i")
	if s.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("an important style attribute lost to an important rule: %v", s.Color)
	}

	// The user agent sheet is what an unstyled book looks like, and it loses
	// to anything the author writes.
	s = styleOf(t, ``, `<h1 id="i">t</h1>`, "i")
	if s.FontSize != 32 || s.FontWeight != 700 || s.Display != DisplayBlock {
		t.Errorf("h1 = %+v", *s)
	}
	s = styleOf(t, `h1 { font-weight: normal }`, `<h1 id="i">t</h1>`, "i")
	if s.FontWeight != 400 {
		t.Errorf("the author lost to the user agent: %d", s.FontWeight)
	}
}

func TestCSSInheritance(t *testing.T) {
	s := styleOf(t, `div { color: red; margin-left: 10px; font-size: 20px }`,
		`<div><p id="i">t</p></div>`, "i")
	if s.Color != (Color{255, 0, 0, 255}) {
		t.Errorf("color did not inherit: %v", s.Color)
	}
	if s.FontSize != 20 {
		t.Errorf("font size did not inherit: %v", s.FontSize)
	}
	if !s.MarginLeft.Auto() && s.MarginLeft.Value != 0 {
		t.Errorf("margin inherited and should not have: %+v", s.MarginLeft)
	}

	s = styleOf(t, `div { color: red } p { color: inherit }`, `<div><p id="i">t</p></div>`, "i")
	if s.Color != (Color{255, 0, 0, 255}) {
		t.Errorf("inherit did not take the parent's: %v", s.Color)
	}
	s = styleOf(t, `div { color: red } p { color: initial }`, `<div><p id="i">t</p></div>`, "i")
	if s.Color != (Color{0, 0, 0, 255}) {
		t.Errorf("initial did not reset: %v", s.Color)
	}
}

// TestCSSMediumFontSize checks that the size a reader is set to is what the
// keywords and the root em are multiples of.
func TestCSSMediumFontSize(t *testing.T) {
	root, err := Parse([]byte(`<html><body><p id="i" style="font-size: large">t</p></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	for _, base := range []float32{16, 24} {
		st := Cascade(root, Media{Width: 600, Height: 800, FontSize: base}, UserAgent())
		var s *Style
		Walk(root, func(n *Node) bool {
			if Attr(n, "id") == "i" {
				s = st.Of(n)
			}
			return true
		})
		if want := base * 6 / 5; s.FontSize < want-0.01 || s.FontSize > want+0.01 {
			t.Errorf("large at %v = %v, want %v", base, s.FontSize, want)
		}
	}
}

func TestCSSLengths(t *testing.T) {
	s := styleOf(t, `body { font-size: 10px }
		#i { font-size: 2em; text-indent: 2em; margin-left: 50%; padding-top: 1in;
		     width: 12pt; height: 2rem; line-height: 1.5 }`,
		`<p id="i">t</p>`, "i")
	if s.FontSize != 20 {
		t.Errorf("font size = %v, want 20 from the parent's em", s.FontSize)
	}
	if s.TextIndent != Px(40) {
		t.Errorf("text indent = %+v, want 40 from the element's own em", s.TextIndent)
	}
	if s.MarginLeft != (Length{Value: 50, Unit: UnitPercent}) {
		t.Errorf("margin = %+v, want a percentage left standing", s.MarginLeft)
	}
	if s.PaddingTop != Px(96) {
		t.Errorf("padding = %+v, want 96", s.PaddingTop)
	}
	if s.Width != Px(16) {
		t.Errorf("width = %+v, want 16", s.Width)
	}
	if s.Height != Px(32) {
		t.Errorf("height = %+v, want 32 from the root's em", s.Height)
	}
	if s.LineHeight != (Length{Value: 1.5, Unit: UnitScale}) {
		t.Errorf("line height = %+v, want a bare number left standing", s.LineHeight)
	}
}

func TestCSSShorthands(t *testing.T) {
	s := styleOf(t, `#i { margin: 1px 2px 3px 4px; padding: 5px 6px }`, `<p id="i">t</p>`, "i")
	if s.MarginTop != Px(1) || s.MarginRight != Px(2) || s.MarginBottom != Px(3) || s.MarginLeft != Px(4) {
		t.Errorf("margin = %+v %+v %+v %+v", s.MarginTop, s.MarginRight, s.MarginBottom, s.MarginLeft)
	}
	if s.PaddingTop != Px(5) || s.PaddingRight != Px(6) || s.PaddingBottom != Px(5) || s.PaddingLeft != Px(6) {
		t.Errorf("padding = %+v %+v %+v %+v", s.PaddingTop, s.PaddingRight, s.PaddingBottom, s.PaddingLeft)
	}

	// A longhand after a shorthand wins whatever order the two are applied
	// in, which is why the shorthand is split before the cascade and not
	// after it.
	s = styleOf(t, `#i { margin: 1px; margin-top: 9px }`, `<p id="i">t</p>`, "i")
	if s.MarginTop != Px(9) || s.MarginLeft != Px(1) {
		t.Errorf("margin = %+v %+v", s.MarginTop, s.MarginLeft)
	}
	s = styleOf(t, `#i { margin-top: 9px; margin: 1px }`, `<p id="i">t</p>`, "i")
	if s.MarginTop != Px(1) {
		t.Errorf("the shorthand lost to the longhand before it: %+v", s.MarginTop)
	}

	s = styleOf(t, `#i { font: italic bold 20px/2 "Times New Roman", serif }`, `<p id="i">t</p>`, "i")
	if s.FontStyle != StyleItalic || s.FontWeight != 700 || s.FontSize != 20 {
		t.Errorf("font = %+v", *s)
	}
	if s.LineHeight != (Length{Value: 2, Unit: UnitScale}) {
		t.Errorf("line height = %+v", s.LineHeight)
	}
	if len(s.FontFamily) != 2 || s.FontFamily[0] != "Times New Roman" || s.FontFamily[1] != "serif" {
		t.Errorf("family = %q", s.FontFamily)
	}

	// The slash may sit against either side or neither.
	for _, sh := range []string{"20px/2 serif", "20px / 2 serif", "20px /2 serif", "20px/ 2 serif"} {
		s = styleOf(t, `#i { font: `+sh+` }`, `<p id="i">t</p>`, "i")
		if s.FontSize != 20 || s.LineHeight != (Length{Value: 2, Unit: UnitScale}) ||
			len(s.FontFamily) != 1 || s.FontFamily[0] != "serif" {
			t.Errorf("font: %s gave size %v line %+v family %q", sh, s.FontSize, s.LineHeight, s.FontFamily)
		}
	}
	s = styleOf(t, `#i { font: 20px serif }`, `<p id="i">t</p>`, "i")
	if s.FontSize != 20 || s.LineHeight != (Length{Value: 1.2, Unit: UnitScale}) {
		t.Errorf("font with no line height = %v %+v", s.FontSize, s.LineHeight)
	}
	s = styleOf(t, `#i { font: caption }`, `<p id="i">t</p>`, "i")
	if s.FontSize != 16 {
		t.Errorf("a system font keyword set a size: %v", s.FontSize)
	}
}

func TestCSSColors(t *testing.T) {
	cases := []struct {
		in   string
		want Color
	}{
		{"#f00", Color{255, 0, 0, 255}},
		{"#ff0000", Color{255, 0, 0, 255}},
		{"#f00f", Color{255, 0, 0, 255}},
		{"#ff000080", Color{255, 0, 0, 128}},
		{"red", Color{255, 0, 0, 255}},
		{"REBECCAPURPLE", Color{102, 51, 153, 255}},
		{"transparent", Color{}},
		{"rgb(255, 0, 0)", Color{255, 0, 0, 255}},
		{"rgb(100%, 0%, 0%)", Color{255, 0, 0, 255}},
		{"rgba(255, 0, 0, 0.5)", Color{255, 0, 0, 128}},
		{"rgb(255 0 0 / 50%)", Color{255, 0, 0, 128}},
		{"hsl(0, 100%, 50%)", Color{255, 0, 0, 255}},
		{"hsl(120, 100%, 50%)", Color{0, 255, 0, 255}},
	}
	for _, tc := range cases {
		v := value{toks: skipSpace(tokensOf(tc.in))}
		got, ok := v.color()
		if !ok || got != tc.want {
			t.Errorf("%q = %v %v, want %v", tc.in, got, ok, tc.want)
		}
	}
	for _, bad := range []string{"#ff00f", "#gg0000", "notacolor", "rgb(1,2)", "rgb(1,2,3,4,5)"} {
		if _, ok := (value{toks: skipSpace(tokensOf(bad))}).color(); ok {
			t.Errorf("%q parsed as a color", bad)
		}
	}
}

func TestCSSMedia(t *testing.T) {
	sheet := `p { color: red }
		@media (max-width: 400px) { p { color: green } }
		@media print { p { color: blue } }
		@media screen and (min-width: 500px) { p { color: black } }`
	s := ParseCSS([]byte(sheet), OriginAuthor)
	if len(s.Rules) != 4 || len(s.Errors) > 0 {
		t.Fatalf("rules %d errors %v", len(s.Rules), s.Errors)
	}
	root, err := Parse([]byte(`<html><body><p id="i">t</p></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	at := func(w float32) Color {
		st := Cascade(root, Media{Width: w, Height: 800, FontSize: 16}, UserAgent(), s)
		var c Color
		Walk(root, func(n *Node) bool {
			if Attr(n, "id") == "i" {
				c = st.Of(n).Color
			}
			return true
		})
		return c
	}
	if got := at(300); got != (Color{0, 128, 0, 255}) {
		t.Errorf("at 300 = %v, want the max-width rule", got)
	}
	if got := at(600); got != (Color{0, 0, 0, 255}) {
		t.Errorf("at 600 = %v, want the min-width rule", got)
	}
}

func TestCSSRecovery(t *testing.T) {
	s := ParseCSS([]byte(`
		@charset "utf-8";
		@namespace epub "http://www.idpf.org/2007/ops";
		@font-face { font-family: x; src: url(x.otf) }
		p { color: red; ; bogus; color red; color: green }
		!! { color: blue }
		q { color: red
	`), OriginAuthor)
	if len(s.Rules) != 2 {
		t.Fatalf("rules = %d, want the p and the unterminated q", len(s.Rules))
	}
	if len(s.Rules[0].Decls) != 2 {
		t.Fatalf("declarations = %+v", s.Rules[0].Decls)
	}
	if len(s.Errors) == 0 {
		t.Fatal("nothing was recorded as dropped")
	}
}

func TestCSSImport(t *testing.T) {
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml": `<html><head>` +
			`<link rel="stylesheet" href="../css/a.css"/>` +
			`<style>p { color: green }</style></head>` +
			`<body><p id="i">t</p></body></html>`,
		"EPUB/css/a.css": `@import "b.css"; p { color: blue }`,
		"EPUB/css/b.css": `p { color: red; font-size: 30px }`,
	})
	root, st, err := d.StylePart("EPUB/text/one.xhtml", Media{Width: 600, Height: 800, FontSize: 16})
	if err != nil {
		t.Fatal(err)
	}
	var s *Style
	Walk(root, func(n *Node) bool {
		if Attr(n, "id") == "i" {
			s = st.Of(n)
		}
		return true
	})
	if s == nil {
		t.Fatal("no styled paragraph")
	}
	if s.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("color = %v, want the style element to win", s.Color)
	}
	if s.FontSize != 30 {
		t.Errorf("font size = %v, want the imported sheet's", s.FontSize)
	}
}

// TestCSSImportCycle checks that a sheet importing itself is read once.
func TestCSSImportCycle(t *testing.T) {
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       pkg,
		"EPUB/nav.xhtml":         nav,
		"EPUB/text/one.xhtml":    `<html><head><link rel="stylesheet" href="../css/a.css"/></head><body><p>t</p></body></html>`,
		"EPUB/css/a.css":         `@import "a.css"; p { color: red }`,
	})
	root, err := d.ParsePart("EPUB/text/one.xhtml")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(d.Stylesheets("EPUB/text/one.xhtml", root)); got != 2 {
		t.Fatalf("sheets = %d, want the user agent sheet and one more", got)
	}
}

// FuzzCSS reads arbitrary bytes as a stylesheet and applies them to a tree. A
// stylesheet is untrusted input like everything else here.
func FuzzCSS(fu *testing.F) {
	fu.Add(uaCSS)
	fu.Add(`p{color:red}`)
	fu.Add(`@media (max-width:1px){a>b .c:not(#d)[e~="f"]{margin:1px 2px}}`)
	fu.Add(`@import url("x.css") print;`)
	fu.Add(`p{font:italic bold 2em/1.4 "x",serif}`)
	fu.Fuzz(func(t *testing.T, s string) {
		sheet := ParseCSS([]byte(s), OriginAuthor)
		root, err := Parse([]byte(selDoc))
		if err != nil {
			t.Fatal(err)
		}
		Cascade(root, Media{Width: 600, Height: 800, FontSize: 16}, UserAgent(), sheet)
	})
}

// TestLineBreakConformance runs the Unicode line break test, which is the
// oracle for UAX #14. It needs the reference directory tools/fetch.sh fills.
func TestLineBreakConformance(t *testing.T) {
	name := filepath.Join(cmp.Or(os.Getenv("PDF_REF_DIR"), "/temp/pdf"), "specs/LineBreakTest.txt")
	b, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	total, bad := 0, 0
	for n, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		text, want, ok := lineBreakCase(line)
		if !ok {
			t.Fatalf("line %d: cannot read %q", n+1, line)
		}
		total++
		got := map[int]bool{}
		for _, br := range lineBreaks(text) {
			got[br.pos] = true
		}
		for pos, w := range want {
			if got[pos] != w {
				if bad++; bad <= 20 {
					t.Errorf("line %d %q: break at %d = %v, want %v", n+1, line, pos, got[pos], w)
				}
				break
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d cases differ", bad, total)
	}
	t.Logf("%d cases", total)
}

// lineBreakCase reads one case of the Unicode test: code points in hex with
// the division sign where a break is allowed and the times sign where it is
// not.
func lineBreakCase(line string) (string, map[int]bool, bool) {
	var text strings.Builder
	want := map[int]bool{}
	for _, f := range strings.Fields(line) {
		switch f {
		case "÷":
			want[text.Len()] = true
			continue
		case "×":
			want[text.Len()] = false
			continue
		}
		v, err := strconv.ParseUint(f, 16, 32)
		if err != nil {
			return "", nil, false
		}
		text.WriteRune(rune(v))
	}
	// The break before the first character is never one, and the one after
	// the last always is; neither is what lineBreaks reports.
	delete(want, 0)
	return text.String(), want, true
}

// TestLineBreaks is the part of UAX #14 that matters to a book, kept in the
// repository so that CI covers it without the reference directory. The
// division sign marks where a break is allowed.
func TestLineBreaks(t *testing.T) {
	cases := []string{
		"one ÷two ÷three",
		"a  ÷b",
		"no-÷break-÷here",
		"and÷—÷dashes",
		"(a) ÷b",
		"$12.50 ÷each",
		"a, ÷b",
		"end.\n÷next",
		"日÷本÷語",
		"ひ÷ら÷が÷な",
		"a b ÷c",
		"“quoted” ÷after",
		"a​÷b",
		"one\r\n÷two",
	}
	for _, tc := range cases {
		text := strings.ReplaceAll(tc, "÷", "")
		want := map[int]bool{}
		n := 0
		for _, r := range tc {
			if r == '÷' {
				want[n] = true
				continue
			}
			n += utf8.RuneLen(r)
		}
		want[len(text)] = true
		got := map[int]bool{}
		for _, br := range lineBreaks(text) {
			got[br.pos] = true
		}
		for pos := range len(text) + 1 {
			if got[pos] != want[pos] {
				t.Errorf("%q: break at %d = %v, want %v (all %v)", tc, pos, got[pos], want[pos], got)
				break
			}
		}
	}

	if b := lineBreaks("a\nb"); len(b) != 2 || !b[0].mandatory || b[0].pos != 2 {
		t.Errorf("a newline is not a mandatory break: %+v", b)
	}
	if b := lineBreaks(""); b != nil {
		t.Errorf("empty text has a break: %+v", b)
	}
}

func FuzzLineBreak(fu *testing.F) {
	fu.Add("one two three")
	fu.Add("日本語のテキスト")
	fu.Add("a‍̈b\r\n (1.5)")
	fu.Fuzz(func(t *testing.T, s string) {
		// A break lands where a character starts, which for a string that is
		// not valid UTF-8 is where the decoder puts a replacement.
		starts := map[int]bool{len(s): true}
		for i := range s {
			starts[i] = true
		}
		last := -1
		for _, br := range lineBreaks(s) {
			if br.pos <= last || !starts[br.pos] {
				t.Fatalf("break at %d after %d in %q", br.pos, last, s)
			}
			last = br.pos
		}
	})
}

func BenchmarkLineBreaks(b *testing.B) {
	const para = "It is a truth universally acknowledged, that a single man in " +
		"possession of a good fortune, must be in want of a wife. However little " +
		"known the feelings or views of such a man may be on his first entering a " +
		"neighbourhood, this truth is so well fixed in the minds of the surrounding " +
		"families, that he is considered as the rightful property of some one or " +
		"other of their daughters."
	b.SetBytes(int64(len(para)))
	for b.Loop() {
		lineBreaks(para)
	}
}

// onePart is a package with a single chapter in its spine, which is what a
// layout test wants: page zero is the fragment under test.
const onePart = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/">
  <dc:identifier id="pub">urn:uuid:1234</dc:identifier><dc:title>A Book</dc:title>
 </metadata>
 <manifest>
  <item id="one" href="text/one.xhtml" media-type="application/xhtml+xml"/>
 </manifest>
 <spine><itemref idref="one"/></spine>
</package>`

// styledPage lays a fragment out on one page and returns it.
func styledPage(t *testing.T, sheet, body string, o *LayoutOptions) (*Document, *Page) {
	t.Helper()
	parts := map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       onePart,
		"EPUB/text/one.xhtml": "<html><head><style>" + sheet + "</style></head><body>" +
			body + "</body></html>",
	}
	d := openBook(t, parts)
	n, err := d.Layout(o)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("the book laid out to nothing")
	}
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	return d, p
}

func TestLayout(t *testing.T) {
	d, p := styledPage(t, `p { margin: 0 }`,
		"<p>one two three</p><p>four five</p>", &LayoutOptions{Width: 400, Height: 400, Margin: 10})
	defer d.Close()
	if got := p.Text(); got != "one two three\nfour five\n" {
		t.Fatalf("page text = %q", got)
	}
	if b := p.Bounds(); b.X1 != 300 || b.Y1 != 300 {
		t.Errorf("bounds = %+v, want 300 by 300 points", b)
	}
	img, err := p.ImageDPI(72)
	if err != nil {
		t.Fatal(err)
	}
	if r := img.Bounds(); r.Dx() != 300 || r.Dy() != 300 {
		t.Errorf("image = %v", r)
	}
	if !hasInk(img) {
		t.Error("the page rendered blank")
	}
}

// TestLayoutWraps checks that a line breaks where it no longer fits and that
// the words survive it.
func TestLayoutWraps(t *testing.T) {
	long := strings.Repeat("word ", 40)
	d, p := styledPage(t, `body, p { margin: 0; font-size: 16px }`,
		"<p>"+long+"</p>", &LayoutOptions{Width: 300, Height: 800, Margin: 0})
	defer d.Close()
	lines := strings.Split(strings.TrimSpace(p.Text()), "\n")
	if len(lines) < 4 {
		t.Fatalf("%d lines, want the text to wrap: %q", len(lines), p.Text())
	}
	if got := strings.Join(strings.Fields(p.Text()), " "); got != strings.TrimSpace(long) {
		t.Errorf("the words changed: %q", got)
	}
	f := styleFace(&Style{FontSize: 16, FontWeight: 400}, nil)
	for _, ln := range lines {
		if w := f.width(ln); w > 300 {
			t.Errorf("line %q is %.1f wide, want at most 300", ln, w)
		}
	}
}

// TestLayoutPaginates checks that every line reaches exactly one page, which
// is the whole of what pagination is answerable for.
func TestLayoutPaginates(t *testing.T) {
	var body strings.Builder
	for i := range 200 {
		fmt.Fprintf(&body, "<p>paragraph number %d</p>", i)
	}
	d, _ := styledPage(t, ``, body.String(), &LayoutOptions{Width: 400, Height: 300, Margin: 10})
	defer d.Close()
	n := d.NumPages()
	if n < 5 {
		t.Fatalf("%d pages, want the text to run over several", n)
	}
	var all strings.Builder
	for i := range n {
		p, err := d.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString(p.Text())
	}
	for i := range 200 {
		if want := fmt.Sprintf("paragraph number %d\n", i); !strings.Contains(all.String(), want) {
			t.Fatalf("%q is on no page", want)
		}
	}
	if got, want := strings.Count(all.String(), "paragraph number "), 200; got != want {
		t.Errorf("%d paragraphs across the pages, want %d", got, want)
	}
}

// TestLayoutPageBreak checks that a page break the book asks for is taken.
func TestLayoutPageBreak(t *testing.T) {
	d, _ := styledPage(t, `h2 { page-break-before: always }`,
		"<p>before</p><h2>heading</h2><p>after</p>", &LayoutOptions{Width: 400, Height: 600, Margin: 10})
	defer d.Close()
	if n := d.NumPages(); n != 2 {
		t.Fatalf("%d pages, want 2", n)
	}
	p0, _ := d.Page(0)
	p1, _ := d.Page(1)
	if !strings.Contains(p0.Text(), "before") || strings.Contains(p0.Text(), "heading") {
		t.Errorf("page 0 = %q", p0.Text())
	}
	if !strings.Contains(p1.Text(), "heading") || !strings.Contains(p1.Text(), "after") {
		t.Errorf("page 1 = %q", p1.Text())
	}
}

// TestLayoutMarginsCollapse checks the rule the rest of block layout is a
// special case of: two vertical margins that meet become the larger.
func TestLayoutMarginsCollapse(t *testing.T) {
	at := func(sheet string) []float32 {
		d, _ := styledPage(t, sheet, "<div><p>one</p></div><p>two</p>",
			&LayoutOptions{Width: 400, Height: 800, Margin: 0})
		defer d.Close()
		var ys []float32
		var walk func(*box)
		walk = func(b *box) {
			for i := range b.lines {
				ys = append(ys, b.lines[i].y)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return ys
	}
	// The div has no padding, so the paragraph's margin collapses through it
	// and out to the body, and the two paragraphs share one gap.
	ys := at(`body { margin: 0 } div { margin: 0 } p { margin: 20px 0 }`)
	if len(ys) != 2 {
		t.Fatalf("%d lines", len(ys))
	}
	if ys[0] != 20 {
		t.Errorf("first line at %v, want the collapsed 20", ys[0])
	}
	gap := ys[1] - ys[0]
	ys2 := at(`body { margin: 0 } div { margin: 0; padding-top: 1px } p { margin: 20px 0 }`)
	if ys2[0] != 21 {
		t.Errorf("first line at %v, want padding to stop the collapse", ys2[0])
	}
	if ys2[1]-ys2[0] != gap {
		t.Errorf("gap %v, want the same %v", ys2[1]-ys2[0], gap)
	}
}

func TestLayoutAlign(t *testing.T) {
	left := func(sheet string) float32 {
		d, _ := styledPage(t, sheet, "<p>short</p>", &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		var x float32 = -1
		var walk func(*box)
		walk = func(b *box) {
			for i := range b.lines {
				if len(b.lines[i].frags) > 0 && x < 0 {
					x = b.lines[i].frags[0].x
				}
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return x
	}
	l := left(`body, p { margin: 0 } p { text-align: left }`)
	c := left(`body, p { margin: 0 } p { text-align: center }`)
	r := left(`body, p { margin: 0 } p { text-align: right }`)
	if !(l < c && c < r) {
		t.Errorf("left %v, centre %v, right %v", l, c, r)
	}
	if l != 0 {
		t.Errorf("left aligned at %v, want 0", l)
	}
}

func hasInk(img *image.RGBA) bool {
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] != 255 || img.Pix[i+1] != 255 || img.Pix[i+2] != 255 {
			return true
		}
	}
	return false
}

// FuzzLayout lays arbitrary markup out and renders a page of it. A book is
// untrusted input, and layout is where its numbers turn into allocations.
func FuzzLayout(fu *testing.F) {
	fu.Add("<p>one two three</p>", "p { margin: 1em }")
	fu.Add("<ul><li>a</li><li>b</li></ul>", "li { list-style-type: decimal }")
	fu.Add("<p style='font-size:900em'>x</p><h1>y</h1>", "* { width: 1e9px }")
	fu.Add("<pre>a\nb</pre><br><img src='x.png'/>", "p { text-align: justify }")
	fu.Add("<table><tr><td colspan='9'>a</td><td rowspan='4'>b</td></tr><tr><td>c</td></tr></table>",
		"td { border: 1px solid red; width: 40% }")
	fu.Add("<div class=f>x</div><p>one two</p>", ".f { float: right; width: 1px } p { clear: both }")
	fu.Add("<div id=w><span id=a>x</span>y</div>",
		"#w{position:relative;height:9px}#a{position:absolute;bottom:0;right:0}")
	fu.Add("<p>Small Caps</p>", "p{font-variant:small-caps;letter-spacing:-9px;text-transform:capitalize}")
	fu.Add("<div class=c><i class=f>f</i></div>",
		".f{float:left;width:9px}.c::after{content:'';display:table;clear:both}i::before{content:'x'}")
	fu.Fuzz(func(t *testing.T, body, sheet string) {
		root, err := Parse([]byte("<html><body>" + body + "</body></html>"))
		if err != nil {
			t.Fatal(err)
		}
		st := Cascade(root, Media{Width: 300, Height: 400, FontSize: 16},
			UserAgent(), ParseCSS([]byte(sheet), OriginAuthor))
		l := &layout{}
		l.run(buildBoxes(root, st), 300)
		for _, top := range paginate(l.spans, 400) {
			if top < 0 || top > l.y {
				t.Fatalf("page at %v of a column %v deep", top, l.y)
			}
		}
	})
}

// TestLayoutConcurrent renders the pages of one book from several goroutines,
// which is what a reader drawing ahead of the page it shows does.
// TestLayoutPlacedDeep checks a box placed below everything in the flow. The
// column runs to the bottom of what it holds, and a page is culled by how far
// its subtree reaches rather than by the height of the box itself.
func TestLayoutPlacedDeep(t *testing.T) {
	d, _ := styledPage(t, `body, p { margin: 0 } .a { position: absolute; top: 300px }`,
		`<p>one</p><span class="a">deep</span>`,
		&LayoutOptions{Width: 400, Height: 100, Margin: 0})
	defer d.Close()
	var all strings.Builder
	for i := range d.NumPages() {
		p, err := d.Page(i)
		if err != nil {
			t.Fatal(err)
		}
		all.WriteString(p.Text())
	}
	if got := all.String(); !strings.Contains(got, "deep") {
		t.Errorf("the pages say %q, want the placed box on one of them", got)
	}
}

func TestLayoutConcurrent(t *testing.T) {
	var body strings.Builder
	for i := range 60 {
		fmt.Fprintf(&body, "<p>paragraph number %d with a few words in it</p>", i)
	}
	d, _ := styledPage(t, ``, body.String(), &LayoutOptions{Width: 400, Height: 300, Margin: 10})
	defer d.Close()
	n := d.NumPages()
	want := make([]string, n)
	for i := range n {
		p, _ := d.Page(i)
		want[i] = p.Text()
	}
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			p, err := d.Page(i)
			if err != nil {
				t.Error(err)
				return
			}
			if got := p.Text(); got != want[i] {
				t.Errorf("page %d = %q, want %q", i, got, want[i])
			}
			if _, err := p.ImageDPI(48); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}

func TestCSSBorders(t *testing.T) {
	s := styleOf(t, `#i { border: 2px dashed #f00 }`, `<p id="i">t</p>`, "i")
	for _, b := range [...]Border{s.BorderTop, s.BorderRight, s.BorderBottom, s.BorderLeft} {
		if b.Width != 2 || b.Style != BorderDashed || b.Color != (Color{255, 0, 0, 255}) {
			t.Fatalf("border = %+v", b)
		}
	}

	s = styleOf(t, `#i { border-width: 1px 2px 3px 4px; border-style: solid }`, `<p id="i">t</p>`, "i")
	if s.BorderTop.Width != 1 || s.BorderRight.Width != 2 || s.BorderBottom.Width != 3 || s.BorderLeft.Width != 4 {
		t.Errorf("widths = %v %v %v %v", s.BorderTop.Width, s.BorderRight.Width, s.BorderBottom.Width, s.BorderLeft.Width)
	}

	// A border with no colour of its own is the colour of the text, which is
	// why the cascade computes that one before the rest.
	s = styleOf(t, `#i { color: green; border: 1px solid }`, `<p id="i">t</p>`, "i")
	if s.BorderTop.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("border colour = %v, want the text colour", s.BorderTop.Color)
	}
	s = styleOf(t, `#i { color: green; border-style: solid }`, `<p id="i">t</p>`, "i")
	if s.BorderTop.Color != (Color{0, 128, 0, 255}) {
		t.Errorf("border colour = %v, want the text colour", s.BorderTop.Color)
	}

	// A style of none takes the edge away whatever the width says.
	s = styleOf(t, `#i { border: 4px solid red; border-top-style: none }`, `<p id="i">t</p>`, "i")
	if s.BorderTop.Thickness() != 0 || s.BorderLeft.Thickness() != 4 {
		t.Errorf("thickness = %v %v", s.BorderTop.Thickness(), s.BorderLeft.Thickness())
	}

	s = styleOf(t, `#i { background: #eee url(x.png) no-repeat }`, `<p id="i">t</p>`, "i")
	if s.Background != (Color{238, 238, 238, 255}) {
		t.Errorf("background = %v, want the colour out of the shorthand", s.Background)
	}
}

// TestLayoutBorders checks that a border takes room and stops a margin
// collapsing through the box.
func TestLayoutBorders(t *testing.T) {
	lines := func(sheet string) []float32 {
		d, _ := styledPage(t, sheet, "<div><p>one</p></div><p>two</p>",
			&LayoutOptions{Width: 400, Height: 800, Margin: 0})
		defer d.Close()
		var ys []float32
		var walk func(*box)
		walk = func(b *box) {
			for i := range b.lines {
				ys = append(ys, b.lines[i].y)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return ys
	}
	plain := lines(`body { margin: 0 } div { margin: 0 } p { margin: 20px 0 }`)
	bordered := lines(`body { margin: 0 } div { margin: 0; border-top: 5px solid red } p { margin: 20px 0 }`)
	if plain[0] != 20 {
		t.Fatalf("first line at %v", plain[0])
	}
	if bordered[0] != 25 {
		t.Errorf("first line at %v, want the border to stop the collapse", bordered[0])
	}
}

// TestCSSBorderRadius reads the corner shorthand in every shape it is
// written in: one value for all four corners, four for one each, and a slash
// that gives the vertical radii their own list.
func TestCSSBorderRadius(t *testing.T) {
	px := func(v float32) Length { return Length{Value: v, Unit: UnitPx} }
	for _, tc := range []struct {
		sheet string
		want  [4]Corner
	}{
		{`#i { border-radius: 5px }`, [4]Corner{
			{px(5), px(5)}, {px(5), px(5)}, {px(5), px(5)}, {px(5), px(5)}}},
		{`#i { border-radius: 4px 4px 0 0 }`, [4]Corner{
			{px(4), px(4)}, {px(4), px(4)}, {px(0), px(0)}, {px(0), px(0)}}},
		{`#i { border-radius: 10px / 5px }`, [4]Corner{
			{px(10), px(5)}, {px(10), px(5)}, {px(10), px(5)}, {px(10), px(5)}}},
		{`#i { border-radius: 1px 2px 3px 4px / 5px 6px }`, [4]Corner{
			{px(1), px(5)}, {px(2), px(6)}, {px(3), px(5)}, {px(4), px(6)}}},
		{`#i { border-radius: .5em }`, [4]Corner{
			{px(8), px(8)}, {px(8), px(8)}, {px(8), px(8)}, {px(8), px(8)}}},
		{`#i { border-top-left-radius: 7px 8px }`, [4]Corner{{px(7), px(8)}}},
	} {
		if got := styleOf(t, tc.sheet, `<p id="i">t</p>`, "i").Radius; got != tc.want {
			t.Errorf("%s gave %v, want %v", tc.sheet, got, tc.want)
		}
	}

	// No edge may be asked for more than its length: the four are scaled by
	// the same factor until none is.
	s := styleOf(t, `#i { border-radius: 100px }`, `<p id="i">t</p>`, "i")
	r, round := radii(s, 50, 50)
	if !round || r[0] != (radius{25, 25}) || r[2] != (radius{25, 25}) {
		t.Errorf("radii scaled to %v", r)
	}
	if _, round := radii(styleOf(t, `#i { color: red }`, `<p id="i">t</p>`, "i"), 50, 50); round {
		t.Error("a box with no radius is rounded")
	}
}

// TestLayoutBorderRadius renders the corners: a rounded background does not
// reach the corner of its box, a rounded border is a ring, and four colours
// meet on the diagonals.
func TestLayoutBorderRadius(t *testing.T) {
	at := func(img *image.RGBA, x, y int) [3]uint8 {
		i := img.PixOffset(x, y)
		return [3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
	}
	render := func(sheet string) *image.RGBA {
		t.Helper()
		d, p := styledPage(t, "body { margin: 0 } .b { margin: 0; width: 100px; height: 100px }"+sheet,
			`<div class="b">x</div>`, &LayoutOptions{Width: 200, Height: 200, Margin: 0})
		defer d.Close()
		img, err := p.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	var (
		white = [3]uint8{255, 255, 255}
		red   = [3]uint8{255, 0, 0}
		blue  = [3]uint8{0, 0, 255}
	)
	square := render(`.b { background: red }`)
	if got := at(square, 1, 1); got != red {
		t.Errorf("the corner of a square background is %v, want %v", got, red)
	}
	round := render(`.b { background: red; border-radius: 30px }`)
	if got := at(round, 1, 1); got != white {
		t.Errorf("the corner of a rounded background is %v, want %v", got, white)
	}
	if got := at(round, 50, 50); got != red {
		t.Errorf("the middle of a rounded background is %v, want %v", got, red)
	}
	if got := at(round, 50, 1); got != red {
		t.Errorf("the top edge of a rounded background is %v, want %v", got, red)
	}

	ring := render(`.b { border: 6px solid blue; border-radius: 30px }`)
	for _, c := range []struct {
		x, y int
		want [3]uint8
	}{{1, 1, white}, {50, 3, blue}, {50, 50, white}, {3, 50, blue}} {
		if got := at(ring, c.x, c.y); got != c.want {
			t.Errorf("the ring at %d,%d is %v, want %v", c.x, c.y, got, c.want)
		}
	}

	sides := render(`.b { border: 8px solid; border-top-color: red;
		border-right-color: lime; border-bottom-color: blue;
		border-left-color: black; border-radius: 25px }`)
	for _, c := range []struct {
		x, y int
		want [3]uint8
	}{
		{58, 3, red}, {113, 58, [3]uint8{0, 255, 0}},
		{58, 113, blue}, {3, 58, [3]uint8{0, 0, 0}},
		{2, 2, white}, {58, 58, white},
	} {
		if got := at(sides, c.x, c.y); got != c.want {
			t.Errorf("the side at %d,%d is %v, want %v", c.x, c.y, got, c.want)
		}
	}
}

// TestLayoutMargins checks what a margin nobody wrote is. The initial value
// is zero, not auto: a block with a width of its own sits at the left edge,
// and only a margin written as auto centres it.
func TestLayoutMargins(t *testing.T) {
	left := func(sheet string) float32 {
		t.Helper()
		d, _ := styledPage(t, "body { margin: 0 } "+sheet, `<div class="b">x</div>`,
			&LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		out := float32(-1)
		var walk func(*box)
		walk = func(b *box) {
			if b.style.Width.Value == 100 && out < 0 {
				out = b.x
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return out
	}
	for _, tc := range []struct {
		sheet string
		want  float32
	}{
		{`.b { width: 100px }`, 0},
		{`.b { width: 100px; margin: 0 auto }`, 150},
		{`.b { width: 100px; margin-left: 20px }`, 20},
	} {
		if got := left(tc.sheet); got != tc.want {
			t.Errorf("%s put the box at %v, want %v", tc.sheet, got, tc.want)
		}
	}
}

// TestLayoutFloat checks that a float narrows the lines beside it and that
// clear moves a box below.
func TestLayoutFloat(t *testing.T) {
	d, _ := styledPage(t, `body, p { margin: 0 } .f { float: left; width: 100px; height: 40px }`,
		`<div class="f"></div><p>`+strings.Repeat("word ", 60)+`</p><p class="c">after</p>`,
		&LayoutOptions{Width: 400, Height: 900, Margin: 0})
	defer d.Close()
	var lines []lineBox
	var walk func(*box)
	walk = func(b *box) {
		lines = append(lines, b.lines...)
		for _, k := range b.kids {
			walk(k)
		}
	}
	walk(d.parts[0].root)
	if len(lines) < 4 {
		t.Fatalf("%d lines", len(lines))
	}
	beside, below := lines[0], lines[len(lines)-1]
	if beside.frags[0].x < 100 {
		t.Errorf("the first line starts at %v, want it beside the float", beside.frags[0].x)
	}
	if below.frags[0].x != 0 {
		t.Errorf("the last line starts at %v, want it past the float", below.frags[0].x)
	}

	// clear moves a box below every float, whatever room is left beside it.
	d2, _ := styledPage(t, `body, p { margin: 0 } .f { float: left; width: 50px; height: 200px }`,
		`<div class="f"></div><p class="c" style="clear: both">after</p>`,
		&LayoutOptions{Width: 400, Height: 900, Margin: 0})
	defer d2.Close()
	lines = nil
	walk(d2.parts[0].root)
	if len(lines) == 0 || lines[0].y < 200 {
		t.Fatalf("cleared line at %v, want it below the float", lines)
	}
}

// TestGeneratedContent checks the boxes a rule asks for before and after the
// content of an element: what they say, that a later declaration takes the
// box away again, and that they inherit from the element they hang off.
func TestGeneratedContent(t *testing.T) {
	d, p := styledPage(t, `p { margin: 0; color: red }
		p::before { content: "X " } p:after { content: " Z" }`,
		"<p>one</p>", &LayoutOptions{Width: 400, Height: 400, Margin: 10})
	defer d.Close()
	if got := p.Text(); got != "X one Z\n" {
		t.Errorf("page text = %q", got)
	}

	// The reset every book copies from another book: a rule that asks for an
	// empty box and then takes it away.
	d2, p2 := styledPage(t, `q::before, q::after { content: ''; content: none }`,
		"<p>a<q>b</q>c</p>", &LayoutOptions{Width: 400, Height: 400, Margin: 10})
	defer d2.Close()
	if got := p2.Text(); got != "abc\n" {
		t.Errorf("page text = %q", got)
	}

	d3, p3 := styledPage(t, `p::before { content: "A"; display: none }
		 p::after { content: attr(title) }`,
		"<p>one</p>", &LayoutOptions{Width: 400, Height: 400, Margin: 10})
	defer d3.Close()
	if got := p3.Text(); got != "one\n" {
		t.Errorf("page text = %q", got)
	}

	root, err := Parse([]byte(`<html><body><p id="i">one</p></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	sheet := ParseCSS([]byte(`p { color: red } p::before { content: "x" }
		p::before { font-weight: bold }`), OriginAuthor)
	st := Cascade(root, Media{Width: 600, Height: 800, FontSize: 16}, UserAgent(), sheet)
	var el *Node
	Walk(root, func(n *Node) bool {
		if Attr(n, "id") == "i" {
			el = n
		}
		return true
	})
	if el == nil {
		t.Fatal("no paragraph")
	}
	g := st.Pseudo(el, PseudoBefore)
	if g == nil {
		t.Fatal("nothing generated before the paragraph")
	}
	if g.Content != "x" || !g.HasContent || g.FontWeight != 700 {
		t.Errorf("generated %q, weight %d", g.Content, g.FontWeight)
	}
	if g.Color != (Color{R: 255, A: 255}) {
		t.Errorf("generated colour %v, want the paragraph's", g.Color)
	}
	if st.Pseudo(el, PseudoAfter) != nil {
		t.Error("something generated after the paragraph")
	}
	if st.Of(el).HasContent {
		t.Error("the element itself generates content")
	}
}

// TestLayoutClearfix checks the one thing a container needs generated content
// for: a box after its floats, cleared, gives it their height.
func TestLayoutClearfix(t *testing.T) {
	const body = `<div class="c"><div class="f">f</div></div><p>after</p>`
	const sheet = `* { margin: 0 } .f { float: left; width: 50px; height: 60px }`
	find := func(d *Document) float32 {
		t.Helper()
		var out float32 = -1
		var walk func(*box)
		walk = func(b *box) {
			for i := range b.lines {
				for _, f := range b.lines[i].frags {
					if f.text == "after" && out < 0 {
						out = b.y
					}
				}
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		if out < 0 {
			t.Fatal("no paragraph")
		}
		return out
	}
	d, _ := styledPage(t, sheet, body, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
	defer d.Close()
	if y := find(d); y != 0 {
		t.Errorf("without a clearfix the paragraph is at %v, want 0", y)
	}
	d2, _ := styledPage(t, sheet+` .c::after { content: ""; display: table; clear: both }`,
		body, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
	defer d2.Close()
	if y := find(d2); y != 60 {
		t.Errorf("with a clearfix the paragraph is at %v, want 60", y)
	}
}

// TestLayoutFloatText checks that what a float says is extracted. Its text
// is not on the lines of the block it was written in, so a walk that stops at
// the lines of a block loses it.
func TestLayoutFloatText(t *testing.T) {
	d, p := styledPage(t, `p { margin: 0 } .f { float: left; width: 30px }`,
		`<p>one <span class="f">FLOAT</span> two</p>`,
		&LayoutOptions{Width: 400, Height: 400, Margin: 0})
	defer d.Close()
	if got := p.Text(); got != "one two\nFLOAT\n" {
		t.Errorf("page text = %q", got)
	}
}

// TestLayoutTable checks the grid a table makes: the columns sit side by
// side, a cell that spans columns covers them, and one that spans rows makes
// the rows below it start lower.
func TestLayoutTable(t *testing.T) {
	d, _ := styledPage(t, `body, table { margin: 0 } td { padding: 0 }`,
		`<table><tr><td>a</td><td>b</td><td>c</td></tr>`+
			`<tr><td colspan="2">wide</td><td>d</td></tr>`+
			`<tr><td rowspan="2">tall one that wraps over two rows of text here</td><td>e</td><td>f</td></tr>`+
			`<tr><td>g</td><td>h</td></tr></table>`,
		&LayoutOptions{Width: 400, Height: 900, Margin: 0})
	defer d.Close()

	var table *box
	var walk func(*box)
	walk = func(b *box) {
		if b.style.Display == DisplayTable && table == nil {
			table = b
		}
		for _, k := range b.kids {
			walk(k)
		}
	}
	walk(d.parts[0].root)
	if table == nil {
		t.Fatal("no table box")
	}
	rows, _ := tableRows(table)
	if len(rows) != 4 {
		t.Fatalf("%d rows", len(rows))
	}
	cells, ncols := buildGrid(rows)
	if ncols != 3 {
		t.Fatalf("%d columns, want 3", ncols)
	}
	if len(cells) != 10 {
		t.Fatalf("%d cells, want 10", len(cells))
	}
	at := func(r, c int) *cell {
		for _, x := range cells {
			if x.row == r && x.col == c {
				return x
			}
		}
		return nil
	}
	if w := at(1, 0); w == nil || w.cols != 2 {
		t.Fatalf("the spanning cell = %+v", w)
	}
	// The cell after the one that spans two columns starts in the third.
	if c := at(1, 2); c == nil {
		t.Fatalf("no cell in the third column of the second row")
	}
	// The row under the one that spans it has its cells in columns one and
	// two, not zero.
	if c := at(3, 0); c != nil {
		t.Errorf("a cell landed under the one that spans down: %+v", c)
	}
	if c := at(3, 1); c == nil {
		t.Errorf("no cell beside the one that spans down")
	}

	first := rows[0].kids[0]
	second := rows[0].kids[1]
	if first.x+first.w > second.x+0.01 {
		t.Errorf("cells overlap: %v+%v against %v", first.x, first.w, second.x)
	}
	if rows[1].y < rows[0].y+rows[0].h-0.01 {
		t.Errorf("rows overlap: %v against %v+%v", rows[1].y, rows[0].y, rows[0].h)
	}
}

// TestCollapseResolution checks which border wins an edge two cells share:
// hidden takes it away, the wider wins, and at the same width the style
// order of CSS 2.1 decides, with the nearer owner taking a tie.
func TestCollapseResolution(t *testing.T) {
	b := func(w float32, st BorderStyle) Border { return Border{Width: w, Style: st} }
	for _, tc := range []struct {
		near, far, want Border
	}{
		{b(1, BorderSolid), b(9, BorderHidden), Border{Style: BorderHidden}},
		{b(9, BorderHidden), b(1, BorderSolid), Border{Style: BorderHidden}},
		{b(1, BorderSolid), b(2, BorderSolid), b(2, BorderSolid)},
		{b(3, BorderSolid), b(2, BorderDouble), b(3, BorderSolid)},
		{b(2, BorderSolid), b(2, BorderDouble), b(2, BorderDouble)},
		{b(2, BorderDashed), b(2, BorderSolid), b(2, BorderSolid)},
		{b(2, BorderDotted), b(2, BorderDashed), b(2, BorderDashed)},
		{b(2, BorderSolid), b(2, BorderSolid), b(2, BorderSolid)},
		{b(0, BorderNone), b(2, BorderSolid), b(2, BorderSolid)},
	} {
		if got := stronger(tc.near, tc.far); got != tc.want {
			t.Errorf("stronger(%v, %v) = %v, want %v", tc.near, tc.far, got, tc.want)
		}
	}
}

// TestLayoutCollapse renders a table both ways. Separate borders draw the
// line between two cells twice, collapsed borders draw it once, and a hidden
// edge takes it away.
func TestLayoutCollapse(t *testing.T) {
	across := func(sheet, body string) int {
		t.Helper()
		d, p := styledPage(t, "body, table { margin: 0 } td { padding: 4px; width: 40px }"+sheet,
			body, &LayoutOptions{Width: 300, Height: 300, Margin: 0})
		defer d.Close()
		img, err := p.ImageDPI(96)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for x := range 120 {
			i := img.PixOffset(x, 12)
			if img.Pix[i] == 0 && img.Pix[i+1] == 0 && img.Pix[i+2] == 0 {
				n++
			}
		}
		return n
	}
	const two = `<table><tr><td>a</td><td>b</td></tr></table>`
	const border = `table, td { border: 2px solid black }`
	if got := across(border, two); got != 12 {
		t.Errorf("%d black pixels across a separate table, want 12", got)
	}
	if got := across(`table { border-collapse: collapse }`+border, two); got != 6 {
		t.Errorf("%d black pixels across a collapsed table, want 6", got)
	}
	if got := across(`table { border-collapse: collapse } td.h { border-right: hidden }`+border,
		`<table><tr><td class="h">a</td><td>b</td></tr></table>`); got != 4 {
		t.Errorf("%d black pixels with a hidden edge, want 4", got)
	}
}

// TestLayoutSpacing checks the gap a table leaves around its cells when its
// borders are not collapsed.
func TestLayoutSpacing(t *testing.T) {
	geom := func(sheet string) [][2]float32 {
		t.Helper()
		d, _ := styledPage(t, "body, table { margin: 0 } td { padding: 0; width: 40px }"+sheet,
			`<table><tr><td>a</td><td>b</td></tr><tr><td>c</td><td>d</td></tr></table>`,
			&LayoutOptions{Width: 300, Height: 300, Margin: 0})
		defer d.Close()
		var out [][2]float32
		var walk func(*box)
		walk = func(b *box) {
			if b.style.Display == DisplayTableCell && b.kind != textBox {
				out = append(out, [2]float32{b.x, b.y})
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return out
	}
	tight := geom(``)
	if len(tight) != 4 || tight[0] != [2]float32{0, 0} || tight[1][0] != 40 {
		t.Fatalf("cells at %v", tight)
	}
	spaced := geom(`table { border-spacing: 10px 20px }`)
	if len(spaced) != 4 || spaced[0] != [2]float32{10, 20} || spaced[1][0] != 60 {
		t.Fatalf("cells at %v", spaced)
	}
	if spaced[2][1] != tight[2][1]+40 {
		t.Errorf("the second row is at %v, want %v below the first",
			spaced[2][1], tight[2][1]+40)
	}
}

// TestCSSWritingMode reads the writing mode in the spellings a book writes
// it in, including the two prefixed ones EPUB 3 was published with and the
// old names that mean a horizontal line.
func TestCSSWritingMode(t *testing.T) {
	for _, tc := range []struct {
		decl string
		want Writing
	}{
		{`writing-mode: vertical-rl`, WritingVerticalRL},
		{`-epub-writing-mode: vertical-rl`, WritingVerticalRL},
		{`-webkit-writing-mode: vertical-rl`, WritingVerticalRL},
		{`writing-mode: tb-rl`, WritingVerticalRL},
		{`writing-mode: vertical-lr`, WritingVerticalLR},
		{`writing-mode: horizontal-tb`, WritingHorizontal},
		{`writing-mode: rl-tb`, WritingHorizontal},
		{`writing-mode: sideways`, WritingHorizontal},
	} {
		if got := styleOf(t, "#i {"+tc.decl+"}", `<p id="i">t</p>`, "i").Writing; got != tc.want {
			t.Errorf("%s gave %d, want %d", tc.decl, got, tc.want)
		}
	}
	if got := styleOf(t, `#i { text-orientation: upright }`, `<p id="i">t</p>`, "i").Orient; got != OrientUpright {
		t.Errorf("text-orientation gave %d", got)
	}

	// A character stands upright in vertical text when UAX #50 says it
	// does, and turns with the line otherwise.
	f := face{vertical: true}
	for _, tc := range []struct {
		r    rune
		want bool
	}{{'日', true}, {'あ', true}, {'。', true}, {'A', false}, {'1', false}, {'—', false}} {
		if got := f.standsUp(tc.r); got != tc.want {
			t.Errorf("%c stands up %v, want %v", tc.r, got, tc.want)
		}
	}
	for _, tc := range []struct {
		o    Orientation
		r    rune
		want bool
	}{{OrientUpright, 'A', true}, {OrientSideways, '日', false}} {
		if got := (face{vertical: true, orient: tc.o}).standsUp(tc.r); got != tc.want {
			t.Errorf("%c under orientation %d stands up %v", tc.r, tc.o, got)
		}
	}
	if (face{}).standsUp('日') {
		t.Error("a character stands up on a horizontal line")
	}
}

// TestLayoutVertical lays a Japanese page out down the page and right to
// left, which is where the ink lands: the first line is against the right
// edge and runs from the top.
func TestLayoutVertical(t *testing.T) {
	d, p := styledPage(t, `html { -epub-writing-mode: vertical-rl; font-size: 20px }
		body, p { margin: 0 }`, `<p>日本語です</p>`,
		&LayoutOptions{Width: 300, Height: 200, Margin: 0})
	defer d.Close()
	if got := p.Text(); got != "日本語です\n" {
		t.Errorf("page text = %q", got)
	}
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	ink := func(x0, y0, x1, y1 int) int {
		n := 0
		for y := y0; y < y1; y++ {
			for x := x0; x < x1; x++ {
				if i := img.PixOffset(x, y); img.Pix[i] < 200 {
					n++
				}
			}
		}
		return n
	}
	if n := ink(280, 0, 300, 100); n < 100 {
		t.Errorf("%d dark pixels down the right edge, want the first line there", n)
	}
	if n := ink(0, 0, 20, 100); n != 0 {
		t.Errorf("%d dark pixels down the left edge, want none", n)
	}
	if n := ink(280, 100, 300, 200); n > 50 {
		t.Errorf("%d dark pixels below the line, want it to end above", n)
	}
}

// TestCSSBackground reads the picture behind a box: the shorthand in the
// order a book writes it, the longhands, and both spellings of an address.
func TestCSSBackground(t *testing.T) {
	pct := func(v float32) Length { return Length{Value: v, Unit: UnitPercent} }
	for _, tc := range []struct {
		sheet  string
		image  string
		repeat Repeat
		x, y   Length
	}{
		{`#i { background: url(a.png) no-repeat right center }`, "a.png", RepeatNone, pct(100), pct(50)},
		{`#i { background: #fff url('b.png') repeat-x }`, "b.png", RepeatX, Length{}, Length{}},
		{`#i { background-image: url("c.png") }`, "c.png", RepeatBoth, Length{}, Length{}},
		{`#i { background-image: none }`, "", RepeatBoth, Length{}, Length{}},
		{`#i { background-image: url(d.png); background-position: left bottom }`,
			"d.png", RepeatBoth, pct(0), pct(100)},
		{`#i { background-image: url(e.png); background-position: 10px }`,
			"e.png", RepeatBoth, Length{Value: 10, Unit: UnitPx}, pct(50)},
	} {
		got := styleOf(t, tc.sheet, `<p id="i">t</p>`, "i")
		if got.BackgroundImage != tc.image || got.BackgroundRepeat != tc.repeat {
			t.Errorf("%s gave %q repeat %d", tc.sheet, got.BackgroundImage, got.BackgroundRepeat)
		}
		if got.BackgroundX != tc.x || got.BackgroundY != tc.y {
			t.Errorf("%s sits at %v %v, want %v %v", tc.sheet, got.BackgroundX, got.BackgroundY, tc.x, tc.y)
		}
	}
	got := styleOf(t, `#i { background-size: 50% auto }`, `<p id="i">t</p>`, "i")
	if got.BackgroundW != pct(50) || !got.BackgroundH.Auto() {
		t.Errorf("background-size gave %v %v", got.BackgroundW, got.BackgroundH)
	}

	// An address in a sheet is relative to the sheet, not to the part that
	// links it.
	sheet := ParseCSS([]byte(`p { background-image: url(pic.png) }
		div { background: url("../up.png") }`), OriginAuthor)
	resolveURLs(sheet, "EPUB/css/main.css")
	for i, want := range []string{"EPUB/css/pic.png", "EPUB/up.png"} {
		if got := (value{toks: sheet.Rules[i].Decls[0].value}).url(); got != want {
			t.Errorf("the address resolved to %q, want %q", got, want)
		}
	}
}

// TestLayoutBackdrop paints the picture behind a box: where it sits, how it
// is tiled and how big it is drawn, all clipped to the box.
func TestLayoutBackdrop(t *testing.T) {
	m := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for y := range 8 {
		for x := range 8 {
			c := color.RGBA{255, 0, 0, 255}
			if x >= 4 {
				c = color.RGBA{0, 0, 255, 255}
			}
			m.Set(x, y, c)
		}
	}
	var pic bytes.Buffer
	if err := png.Encode(&pic, m); err != nil {
		t.Fatal(err)
	}
	sheet := `body { margin: 0 } div { margin: 0; width: 200px; height: 40px }
		.a { background: url(pic.png) no-repeat right center }
		.b { background-image: url(pic.png); background-repeat: repeat-x }
		.c { background-image: url(pic.png); background-repeat: no-repeat;
		     background-position: left top; background-size: 50% auto }`
	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       onePart,
		"EPUB/text/one.xhtml": "<html><head><style>" + sheet + "</style></head><body>" +
			`<div class="a">a</div><div class="b">b</div><div class="c">c</div></body></html>`,
		"EPUB/text/pic.png": pic.String(),
	})
	if _, err := d.Layout(&LayoutOptions{Width: 300, Height: 300, Margin: 0}); err != nil {
		t.Fatal(err)
	}
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	at := func(x, y int) [3]uint8 {
		i := img.PixOffset(x, y)
		return [3]uint8{img.Pix[i], img.Pix[i+1], img.Pix[i+2]}
	}
	var (
		white = [3]uint8{255, 255, 255}
		red   = [3]uint8{255, 0, 0}
		blue  = [3]uint8{0, 0, 255}
	)
	for _, c := range []struct {
		what string
		x, y int
		want [3]uint8
	}{
		{"the tile against the right edge", 194, 20, red},
		{"the tile's other half", 198, 20, blue},
		{"the box beside it", 100, 20, white},
		{"a row of tiles along the top", 2, 42, red},
		{"the row's second tile", 14, 42, blue},
		{"below the row", 100, 70, white},
		{"a picture drawn half the width of its box", 20, 82, red},
		{"the other half of it", 70, 82, blue},
		{"past the picture", 150, 82, white},
	} {
		if got := at(c.x, c.y); got != c.want {
			t.Errorf("%s at %d,%d is %v, want %v", c.what, c.x, c.y, got, c.want)
		}
	}
}

func TestCSSTypography(t *testing.T) {
	s := styleOf(t, `#i { font-variant: small-caps; letter-spacing: 2px; text-transform: uppercase }`,
		`<p id="i">t</p>`, "i")
	if !s.SmallCaps || s.LetterSpacing != 2 || s.TextTransform != TransformUpper {
		t.Errorf("style = %+v", *s)
	}
	// All three are inherited, which is what makes a run of small capitals
	// survive the emphasis inside it.
	s = styleOf(t, `div { font-variant: small-caps; letter-spacing: 2px }`,
		`<div><em id="i">t</em></div>`, "i")
	if !s.SmallCaps || s.LetterSpacing != 2 {
		t.Errorf("inherited = %+v", *s)
	}

	if got, want := transform("one two", TransformUpper), "ONE TWO"; got != want {
		t.Errorf("upper = %q", got)
	}
	if got, want := transform("one two", TransformCapitalize), "One Two"; got != want {
		t.Errorf("capitalize = %q", got)
	}
}

// TestLayoutTracking checks that letter spacing reaches the measurement, not
// only the drawing.
func TestLayoutTracking(t *testing.T) {
	width := func(sheet string) float32 {
		d, _ := styledPage(t, sheet, `<p id="i">abcdef</p>`, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		var w float32
		var walk func(*box)
		walk = func(b *box) {
			for i := range b.lines {
				w = max(w, b.lines[i].natural)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return w
	}
	plain := width(`body, p { margin: 0 }`)
	tracked := width(`body, p { margin: 0 } p { letter-spacing: 5px }`)
	if tracked-plain < 29 || tracked-plain > 31 {
		t.Errorf("six characters at five pixels added %v, want thirty", tracked-plain)
	}
}

// TestLayoutSized checks that min and max hold the used width between them.
func TestLayoutSized(t *testing.T) {
	at := func(sheet string) float32 {
		d, _ := styledPage(t, sheet, `<div id="i">x</div>`, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		var w float32
		var walk func(*box)
		walk = func(b *box) {
			if b.node != nil && Attr(b.node, "id") == "i" {
				w = b.w
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return w
	}
	if got := at(`body { margin: 0 } #i { max-width: 120px }`); got != 120 {
		t.Errorf("max-width gave %v", got)
	}
	if got := at(`body { margin: 0 } #i { width: 50px; min-width: 300px }`); got != 300 {
		t.Errorf("min-width gave %v", got)
	}
	if got := at(`body { margin: 0 } #i { width: 50px; max-width: 20px }`); got != 20 {
		t.Errorf("max-width over width gave %v", got)
	}
}

// TestLayoutPositioned checks that a relative box moves without moving the
// flow and that an absolute one is placed against its positioned ancestor.
func TestLayoutPositioned(t *testing.T) {
	boxes := func(sheet, body string) map[string]*box {
		d, _ := styledPage(t, sheet, body, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		out := map[string]*box{}
		var walk func(*box)
		walk = func(b *box) {
			if b.node != nil {
				if id := Attr(b.node, "id"); id != "" {
					out[id] = b
				}
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return out
	}
	m := boxes(`body, p { margin: 0 } #a { position: relative; left: 30px; top: 10px }`,
		`<p id="a">one</p><p id="b">two</p>`)
	if m["a"].x != 30 || m["a"].y != 10 {
		t.Errorf("the relative box is at %v,%v", m["a"].x, m["a"].y)
	}
	if m["b"].y > 30 {
		t.Errorf("the flow moved with it: the next box is at %v", m["b"].y)
	}

	m = boxes(`body, p, div { margin: 0 } #w { position: relative; height: 200px; padding: 10px }
		#a { position: absolute; top: 5px; right: 6px; width: 40px }`,
		`<div id="w"><p>text</p><span id="a">x</span></div>`)
	if m["a"] == nil {
		t.Fatal("the absolute box was not laid out")
	}
	if got, want := m["a"].y, float32(15); got != want {
		t.Errorf("top = %v, want %v", got, want)
	}
	if got, want := m["a"].x+m["a"].w, float32(400-10-6); got != want {
		t.Errorf("right edge = %v, want %v", got, want)
	}
}

// TestLayoutFallbackFace checks that a character the base fourteen cannot
// draw finds a face that can, which is what a book in any other script needs.
func TestLayoutFallbackFace(t *testing.T) {
	base := styleFace(&Style{FontSize: 16, FontWeight: 400}, nil)
	if base.m.has('日') {
		t.Skip("the substitute already has the ideographs")
	}
	f := fallbackFace(base, '日')
	if f.prog == nil {
		t.Skip("the machine has no face for the ideographs")
	}
	if !f.m.has('日') {
		t.Fatalf("%q was chosen and has no glyph for it", f.prog.Family)
	}
	if f.size != base.size || f.track != base.track {
		t.Errorf("the fallback changed the size: %+v against %+v", f, base)
	}

	// The run is split where the face has to change, and the text survives.
	d, p := styledPage(t, `body, p { margin: 0 }`, "<p>one 日本 two</p>",
		&LayoutOptions{Width: 400, Height: 400, Margin: 0})
	defer d.Close()
	if got, want := strings.TrimSpace(p.Text()), "one 日本 two"; got != want {
		t.Fatalf("page text = %q, want %q", got, want)
	}
	var faces []face
	var walk func(*box)
	walk = func(b *box) {
		for i := range b.lines {
			for _, fr := range b.lines[i].frags {
				faces = append(faces, fr.face)
			}
		}
		for _, k := range b.kids {
			walk(k)
		}
	}
	walk(d.parts[0].root)
	if len(faces) < 3 {
		t.Fatalf("%d fragments, want the ideographs in one of their own", len(faces))
	}
	if faces[0].prog == faces[1].prog {
		t.Errorf("the ideographs were drawn with the Latin face")
	}
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	if !hasInk(img) {
		t.Error("the page rendered blank")
	}
}

func TestCSSFontFace(t *testing.T) {
	s := ParseCSS([]byte(`
		@font-face { font-family: "Old Standard"; src: url(a.otf) format("opentype") }
		@font-face { font-family: Old Standard; font-weight: bold; font-style: italic;
		             src: local("Nothing"), url("b.woff") format("woff"), url(c.ttf) }
		@font-face { src: url(d.otf) }
		@font-face { font-family: X }
		p { color: red }
	`), OriginAuthor)
	if len(s.Errors) > 0 {
		t.Fatalf("errors %v", s.Errors)
	}
	if len(s.Rules) != 1 {
		t.Fatalf("%d rules, want the p to survive the at-rules", len(s.Rules))
	}
	if len(s.Faces) != 2 {
		t.Fatalf("%d faces, want the two that name a family and a source: %+v", len(s.Faces), s.Faces)
	}
	if got := s.Faces[0]; got.Family != "Old Standard" || got.Weight != 400 || got.Italic ||
		len(got.Src) != 1 || got.Src[0] != "a.otf" {
		t.Errorf("first = %+v", got)
	}
	// A local face is not a place this can read, so it is left out; the two
	// urls after it stay in the order they were written.
	if got := s.Faces[1]; got.Weight != 700 || !got.Italic ||
		len(got.Src) != 2 || got.Src[0] != "b.woff" || got.Src[1] != "c.ttf" {
		t.Errorf("second = %+v", got)
	}

	resolveFaces(s, "EPUB/css/main.css")
	if got := s.Faces[0].Src[0]; got != "EPUB/css/a.otf" {
		t.Errorf("resolved to %q", got)
	}
}

// TestLayoutEmbeddedFont checks that a book which brings a font with it is
// drawn in it, whatever the machine has.
func TestLayoutEmbeddedFont(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(cmp.Or(os.Getenv("CONFORMANCE_DIR"), "/temp/pdf"),
		"corpus/books/epub3/packed/wasteland-woff-obf.epub"))
	if err != nil {
		t.Skip("no corpus")
	}
	d, err := Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if len(d.obfuscated) == 0 {
		t.Fatal("the book says its fonts are scrambled and none was recorded")
	}
	it := d.Spine()[len(d.Spine())/2]
	root, err := d.ParsePart(it.Path)
	if err != nil {
		t.Fatal(err)
	}
	set := newFontSet(d, d.Stylesheets(it.Path, root))
	if set == nil {
		t.Fatal("the book declares no faces")
	}
	prog := set.pick([]string{"OldStandard"}, false, false)
	if prog == nil {
		t.Fatal("the face the book brings did not load")
	}
	if prog.Family != "Old Standard TT" {
		t.Errorf("loaded %q", prog.Family)
	}
	// The same file is read once however many parts name it.
	if again := set.pick([]string{"OldStandard"}, false, false); again != prog {
		t.Error("the font was read twice")
	}
}

// TestLayoutVerticalAlign covers the two places vertical-align means
// something: where an inline box sits on the line it is on, and where the
// content of a table cell sits in the height its row came to.
func TestLayoutVerticalAlign(t *testing.T) {
	frags := func(sheet, body string) []frag {
		t.Helper()
		d, _ := styledPage(t, "body { margin: 0; font-size: 20px; line-height: 20px } "+sheet,
			body, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		defer d.Close()
		var out []frag
		var walk func(*box)
		walk = func(b *box) {
			for _, ln := range b.lines {
				out = append(out, ln.frags...)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return out
	}

	f := frags(``, `<p>a<sup>b</sup>c<sub>d</sub></p>`)
	if len(f) != 4 {
		t.Fatalf("%d fragments, want 4", len(f))
	}
	if f[0].dy != 0 || f[2].dy != 0 {
		t.Errorf("plain text is at %v and %v, want the baseline", f[0].dy, f[2].dy)
	}
	if f[1].dy <= 0 {
		t.Errorf("a superscript is at %v, want it above the baseline", f[1].dy)
	}
	if f[3].dy >= 0 {
		t.Errorf("a subscript is at %v, want it below the baseline", f[3].dy)
	}

	// The edge alignments are measured against the line, so the two of them
	// sit exactly one line apart when nothing else makes the line taller.
	f = frags(`.t { vertical-align: top } .b { vertical-align: bottom }`,
		`<p>a<span class="t">b</span><span class="b">c</span></p>`)
	if len(f) != 3 {
		t.Fatalf("%d fragments, want 3", len(f))
	}
	if f[1].dy != 0 || f[2].dy != 0 {
		t.Errorf("top is at %v and bottom at %v, want both on a line they fit in",
			f[1].dy, f[2].dy)
	}

	cells := func(sheet string) []float32 {
		t.Helper()
		d, _ := styledPage(t, "body, table { margin: 0 } td { padding: 0; width: 60px } "+sheet,
			`<table><tr><td id="tall">a<br>b<br>c</td><td id="short">d</td></tr></table>`,
			&LayoutOptions{Width: 300, Height: 300, Margin: 0})
		defer d.Close()
		var out []float32
		var walk func(*box)
		walk = func(b *box) {
			if b.style.Display == DisplayTableCell && b.kind != textBox {
				y := float32(-1)
				if len(b.lines) > 0 {
					y = b.lines[0].y
				} else if len(b.kids) > 0 && len(b.kids[0].lines) > 0 {
					y = b.kids[0].lines[0].y
				}
				out = append(out, y)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return out
	}

	top := cells(`td { vertical-align: top }`)
	mid := cells(`td { vertical-align: middle }`)
	bot := cells(`td { vertical-align: bottom }`)
	for _, v := range [][]float32{top, mid, bot} {
		if len(v) != 2 {
			t.Fatalf("%d cells, want 2", len(v))
		}
	}
	if top[0] != top[1] {
		t.Errorf("aligned to the top the cells start at %v and %v", top[0], top[1])
	}
	if !(mid[1] > top[1] && bot[1] > mid[1]) {
		t.Errorf("the short cell is at %v top, %v middle, %v bottom, want each below the last",
			top[1], mid[1], bot[1])
	}
	if mid[0] != top[0] || bot[0] != top[0] {
		t.Errorf("the tall cell moved: %v, %v, %v", top[0], mid[0], bot[0])
	}
}

// TestLayoutPercentHeight covers CSS 2.1 10.5: a percentage height is a
// percentage of the containing block's height, and auto when that block has
// no height of its own. It used to be resolved against the width.
func TestLayoutPercentHeight(t *testing.T) {
	height := func(sheet string) float32 {
		t.Helper()
		d, _ := styledPage(t, "body { margin: 0 } "+sheet, `<div id="a"><div id="b">x</div></div>`,
			&LayoutOptions{Width: 400, Height: 200, Margin: 0})
		defer d.Close()
		var found float32
		var walk func(*box)
		walk = func(bx *box) {
			if Attr(bx.node, "id") == "b" {
				found = bx.h
			}
			for _, k := range bx.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return found
	}

	// The page is 400 wide and 200 tall, so a height resolved against the
	// wrong axis is unmistakable.
	auto := height(`#b { height: 50% }`)
	if auto > 100 {
		t.Errorf("inside a block with no height of its own a percentage came to %v, want auto", auto)
	}
	fixed := height(`#a { height: 120px } #b { height: 50% }`)
	if fixed != 60 {
		t.Errorf("half of 120px came to %v, want 60", fixed)
	}
	root := height(`html, body, #a { height: 100% } #b { height: 25% }`)
	if root != 50 {
		t.Errorf("a quarter of the page came to %v, want 50", root)
	}
	least := height(`#a { height: 120px } #b { min-height: 50% }`)
	if least != 60 {
		t.Errorf("a min-height of half of 120px came to %v, want 60", least)
	}
}

// TestLayoutInlineBlock puts a box on a line: it is as wide as what it holds,
// it sits beside the text rather than under it, and its baseline is the one
// of its last line.
func TestLayoutInlineBlock(t *testing.T) {
	lay := func(sheet, body string) (*box, []frag, float32) {
		t.Helper()
		d, _ := styledPage(t, "body { margin: 0; font-size: 20px; line-height: 20px } "+sheet,
			body, &LayoutOptions{Width: 400, Height: 400, Margin: 0})
		t.Cleanup(func() { d.Close() })
		var found *box
		var frags []frag
		lines := 0
		var walk func(*box)
		walk = func(b *box) {
			if b.node != nil && Attr(b.node, "id") == "k" {
				// What the box holds is a line of its own, and the
				// fragments wanted here are the ones on the line it sits on.
				found = b
				return
			}
			lines += len(b.lines)
			for i := range b.lines {
				frags = append(frags, b.lines[i].frags...)
			}
			for _, k := range b.kids {
				walk(k)
			}
		}
		walk(d.parts[0].root)
		return found, frags, float32(lines)
	}

	k, frags, lines := lay(`#k { display: inline-block }`,
		`<p>ab<span id="k">cd</span>ef</p>`)
	if k == nil {
		t.Fatal("the inline-block left no box")
	}
	if len(frags) != 3 {
		t.Fatalf("%d fragments, want the text either side of the box", len(frags))
	}
	if frags[1].sub != k {
		t.Fatal("the box is not the fragment between the two runs")
	}
	if !(frags[0].x < frags[1].x && frags[1].x < frags[2].x) {
		t.Errorf("the three sit at %v, %v, %v, want them in order along the line",
			frags[0].x, frags[1].x, frags[2].x)
	}
	if k.x != frags[1].x {
		t.Errorf("the box is at %v and its fragment at %v", k.x, frags[1].x)
	}
	// It is as wide as the two characters it holds, not as wide as the page.
	if k.w <= 0 || k.w > 100 {
		t.Errorf("the box came out %v wide, want it to shrink to what it holds", k.w)
	}
	// The text either side of it and the box are on one line of the block,
	// and the box holds a line of its own.
	if lines != 1 {
		t.Errorf("%v lines outside the box, want the text either side of it on one", lines)
	}

	// A block does not share a line, so the same markup laid out as a block
	// puts the box below the text that comes before it.
	block, _, _ := lay(`#k { display: block }`, `<p>ab<span id="k">cd</span>ef</p>`)
	if block == nil {
		t.Fatal("the block left no box")
	}
	if block.y <= k.y {
		t.Errorf("as a block the box is at %v and inline at %v, want the block lower",
			block.y, k.y)
	}
}

// TestSVGInSpine covers a book whose spine items are drawings rather than
// documents, which is how a pre-paginated book is written, and an img that
// names one from inside a chapter.
func TestSVGInSpine(t *testing.T) {
	const svgPkg = `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="pub">
 <metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:title>Drawn</dc:title></metadata>
 <manifest>
  <item id="one" href="p1.svg" media-type="image/svg+xml"/>
  <item id="two" href="text/two.xhtml" media-type="application/xhtml+xml"/>
  <item id="art" href="art.svg" media-type="image/svg+xml"/>
 </manifest>
 <spine><itemref idref="one"/><itemref idref="two"/></spine>
</package>`
	// The drawing is half as wide as it is tall, so a square page fits it to
	// the height and centres it, leaving the page either side.
	const page1 = `<svg xmlns="http://www.w3.org/2000/svg" width="100" height="200" viewBox="0 0 100 200">` +
		`<rect width="100" height="200" fill="red"/>` +
		`<text x="5" y="100" font-size="20">Drawn page</text></svg>`
	const art = `<svg xmlns="http://www.w3.org/2000/svg" width="40" height="20"><rect width="40" height="20" fill="blue"/></svg>`

	d := openBook(t, map[string]string{
		"META-INF/container.xml": container,
		"EPUB/package.opf":       svgPkg,
		"EPUB/p1.svg":            page1,
		"EPUB/art.svg":           art,
		"EPUB/text/two.xhtml":    `<html><body><p><img src="../art.svg"/></p></body></html>`,
	})
	n, err := d.Layout(&LayoutOptions{Width: 200, Height: 200, Margin: 0})
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("%d pages, want one for the drawing and one for the chapter", n)
	}

	// The drawing is a page of the book, and the text in it is what the page
	// says even though no box tree holds it.
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	if got := p.Path(); got != "EPUB/p1.svg" {
		t.Errorf("page 0 came out of %q", got)
	}
	if s := p.Text(); !strings.Contains(s, "Drawn page") {
		t.Errorf("the drawn page reads %q, want the text of the drawing", s)
	}
	img, err := p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	// It is fitted to the page: the whole height, half the width, centred.
	if r, _, _, _ := img.At(100, 190).RGBA(); r>>8 < 200 {
		t.Errorf("the middle of the drawing is %v, want it red", img.At(100, 190))
	}
	if r, g, b, _ := img.At(10, 100).RGBA(); r>>8 != 255 || g>>8 != 255 || b>>8 != 255 {
		t.Errorf("beside the drawing is %v, want the page", img.At(10, 100))
	}

	// A chapter that names a drawing draws it at the size the file asks for.
	p, err = d.Page(1)
	if err != nil {
		t.Fatal(err)
	}
	img, err = p.ImageDPI(96)
	if err != nil {
		t.Fatal(err)
	}
	blue := 0
	for i := 0; i < len(img.Pix); i += 4 {
		if img.Pix[i] < 64 && img.Pix[i+2] > 200 {
			blue++
		}
	}
	if blue < 40*20/2 {
		t.Errorf("the drawing an img named covers %d pixels, want about %d", blue, 40*20)
	}
}

// palmDB wraps records into the smallest PalmDB that holds them.
func palmDB(kind string, recs [][]byte) []byte {
	head := make([]byte, 78+8*len(recs))
	copy(head, "A Book")
	copy(head[60:], kind)
	be16(head[76:], uint16(len(recs)))
	off := len(head)
	for i, r := range recs {
		be32(head[78+8*i:], uint32(off))
		off += len(r)
	}
	out := head
	for _, r := range recs {
		out = append(out, r...)
	}
	return out
}

// TestMOBIHuffcdic covers the compression a MOBI may carry instead of LZ77:
// a HUFF record of code tables and a CDIC record of the phrases they name.
func TestMOBIHuffcdic(t *testing.T) {
	// Every top byte is a whole eight bit code, and each is arranged to name
	// phrase zero: the index is the maximum for the code less the code, and
	// the maximum is written as the byte itself.
	huff := make([]byte, huffMinSize)
	copy(huff, "HUFF")
	be32(huff[4:], huffHeader)
	be32(huff[8:], 24)
	be32(huff[12:], 24+256*4)
	for i := 0; i < 256; i++ {
		be32(huff[24+4*i:], uint32(i)<<8|0x80|8)
	}

	cdic := make([]byte, 16+2)
	copy(cdic, "CDIC")
	be32(cdic[4:], cdicHeader)
	be32(cdic[8:], 1)  // one phrase in the book
	be32(cdic[12:], 1) // one bit of an index names it within this record
	be16(cdic[16:], 2) // the phrase sits just past this table
	phrase := make([]byte, 2)
	be16(phrase, 0x8000|uint16(len("hello")))
	cdic = append(cdic, append(phrase, "hello"...)...)

	h := readHuffcdic([][]byte{nil, huff, cdic}, 1, 2)
	if h == nil {
		t.Fatal("the dictionary did not read")
	}
	if got := string(h.unpack(nil, []byte{0}, 0)); got != "hello" {
		t.Fatalf("unpacked %q, want hello", got)
	}
	// A record of two bytes is two codes, so the phrase comes out twice.
	if got := string(h.unpack(nil, []byte{0, 0}, 0)); got != "hellohello" {
		t.Fatalf("unpacked %q, want it twice", got)
	}
}

// TestMOBIMarkup covers the tags and the links MOBI writes that HTML has no
// idea about.
func TestMOBIMarkup(t *testing.T) {
	head := `<html><body><a filepos=0000000042 >go</a>` +
		`<mbp:pagebreak/><idx:entry/>`
	in := []byte(head + `<p>x</p><img recindex="00003"/>` +
		`<img src="kindle:embed:0002?mime=image/jpeg"/></body></html>`)
	// The place a link names is where the paragraph starts, which is where
	// the anchor for it has to go.
	at := len(head)
	got := string(mobiMarkup(in, []int{at}))
	for _, want := range []string{
		`href="#filepos0000000042"`,
		`<a id="` + filepos(at) + `"></a>`,
		`page-break-after:always`,
		`<img src="00003"/>`,
		`<img src="00002"/>`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("markup has no %s:\n%s", want, got)
		}
	}
	// A tag of its own that closes nothing is dropped, or the tree it opens
	// would run to the end of the book.
	if strings.Contains(got, "idx:entry") || strings.Contains(got, "mbp:") {
		t.Errorf("a tag of its own survived:\n%s", got)
	}
	// The anchor goes where the offset says, which is at the paragraph.
	if i := strings.Index(got, `<a id="`+filepos(at)+`">`); i < 0 ||
		!strings.HasPrefix(got[i:], `<a id="`+filepos(at)+`"></a><p>x</p>`) {
		t.Errorf("the anchor landed in the wrong place:\n%s", got)
	}
}

// TestMOBIIndex covers the index a book keeps its table of contents in: a
// record naming the tags, a record of entries, and a record of the strings
// they point at.
func TestMOBIIndex(t *testing.T) {
	// The master record: one record of entries follows it, then one of
	// strings. TAGX says an entry may carry a place, a label and a level,
	// each named by one bit of one control byte.
	master := make([]byte, 192)
	copy(master, "INDX")
	be32(master[4:], 192)
	be32(master[24:], 1) // one record of entries
	be32(master[36:], 2) // two entries in all
	be32(master[52:], 1) // one record of strings
	tagx := make([]byte, 12+4*4)
	copy(tagx, "TAGX")
	be32(tagx[4:], uint32(len(tagx)))
	be32(tagx[8:], 1) // one control byte
	copy(tagx[12:], []byte{ncxFilepos, 1, 0x01, 0})
	copy(tagx[16:], []byte{ncxLabel, 1, 0x02, 0})
	copy(tagx[20:], []byte{ncxLevel, 1, 0x04, 0})
	copy(tagx[24:], []byte{0, 0, 0, 1})
	master = append(master, tagx...)

	entry := func(at, label, level int) []byte {
		b := []byte{0, 0x07}
		for _, v := range []int{at, label, level} {
			b = append(b, varint(v)...)
		}
		return b
	}
	body := make([]byte, 56)
	copy(body, "INDX")
	be32(body[4:], 56)
	one, two := entry(100, 0, 0), entry(200, 4, 1)
	off1, off2 := len(body), len(body)+len(one)
	body = append(body, one...)
	body = append(body, two...)
	be32(body[20:], uint32(len(body))) // the table sits after the entries
	be32(body[24:], 2)
	idxt := make([]byte, 4+2*2)
	copy(idxt, "IDXT")
	be16(idxt[4:], uint16(off1))
	be16(idxt[6:], uint16(off2))
	body = append(body, idxt...)

	var cncx []byte
	for _, s := range []string{"One", "Two"} {
		cncx = append(cncx, byte(0x80|len(s)))
		cncx = append(cncx, s...)
	}

	out, pos := mobiOutline([][]byte{nil, master, body, cncx}, 1)
	if len(out) != 1 || out[0].Title != "One" || len(out[0].Children) != 1 {
		t.Fatalf("outline = %+v", out)
	}
	if out[0].Children[0].Title != "Two" {
		t.Fatalf("child = %+v", out[0].Children[0])
	}
	if out[0].Fragment != filepos(100) || out[0].Children[0].Fragment != filepos(200) {
		t.Fatalf("fragments = %q, %q", out[0].Fragment, out[0].Children[0].Fragment)
	}
	if len(pos) != 2 || pos[0] != 100 || pos[1] != 200 {
		t.Fatalf("places = %v", pos)
	}
}

// varint writes a number the way an index entry carries one: seven bits at a
// time, most significant first, with the top bit ending it.
func varint(v int) []byte {
	var b []byte
	for {
		b = append([]byte{byte(v & 0x7f)}, b...)
		v >>= 7
		if v == 0 {
			break
		}
	}
	b[len(b)-1] |= 0x80
	return b
}

// TestMOBIHybrid covers a file holding a KF7 book and a KF8 one, which is
// what a converter writes: the newer half is the book, and the records of the
// older one are not part of it.
func TestMOBIHybrid(t *testing.T) {
	old := buildMOBI([]byte("<html><body><p>Old</p></body></html>"), nil, nil)
	recs := [][]byte{}
	n := int(old[76])<<8 | int(old[77])
	for i := 0; i < n; i++ {
		off := int(be32of(old[78+8*i:]))
		end := len(old)
		if i+1 < n {
			end = int(be32of(old[78+8*(i+1):]))
		}
		recs = append(recs, old[off:end])
	}
	// The older half says where the newer one starts, and the record before
	// it says so too.
	exth := make([]byte, 12+12)
	copy(exth, "EXTH")
	be32(exth[4:], uint32(len(exth)))
	be32(exth[8:], 1)
	be32(exth[12:], exthBoundary)
	be32(exth[16:], 12)
	be32(exth[20:], uint32(len(recs)+1))
	rec0 := append(append([]byte(nil), recs[0][:16+232]...), exth...)
	be32(rec0[0x80:], 0x40)
	recs[0] = rec0

	newer := buildMOBI([]byte("<html><body><p>New</p></body></html>"), nil, nil)
	m := int(newer[76])<<8 | int(newer[77])
	recs = append(recs, []byte("BOUNDARY"))
	for i := 0; i < m; i++ {
		off := int(be32of(newer[78+8*i:]))
		end := len(newer)
		if i+1 < m {
			end = int(be32of(newer[78+8*(i+1):]))
		}
		recs = append(recs, newer[off:end])
	}

	d, err := Load(palmDB("BOOKMOBI", recs))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	b, _ := d.Read("index.html")
	if !strings.Contains(string(b), "New") || strings.Contains(string(b), "Old") {
		t.Fatalf("the book reads %q, want the newer half", b)
	}
}

func be32of(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// TestMOBIEncrypted checks that a book nobody may read says so rather than
// handing back what the compression made of the cipher.
func TestMOBIEncrypted(t *testing.T) {
	b := buildMOBI([]byte("<html><body><p>Hello</p></body></html>"), nil, nil)
	off := int(be32of(b[78:]))
	be16(b[off+12:], 2)
	if _, err := Load(b); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an encrypted book opened with %v", err)
	}
}

// TestPlainTextBook covers a file that is text rather than markup, which is a
// book of one part and has to lay out like any other.
func TestPlainTextBook(t *testing.T) {
	d, err := Load([]byte("One line.\n\nAnother line.\n"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	s, err := d.Text()
	if err != nil || !strings.Contains(s, "Another line.") {
		t.Fatalf("Text = %q, %v", s, err)
	}
	n, err := d.Layout(&LayoutOptions{Width: 400, Height: 600, Margin: 20})
	if err != nil || n != 1 {
		t.Fatalf("Layout = %d, %v", n, err)
	}
	p, err := d.Page(0)
	if err != nil {
		t.Fatal(err)
	}
	// The lines of a text file are kept, which is what pre asks for.
	if got := p.Text(); !strings.Contains(got, "One line.\n") {
		t.Fatalf("the page reads %q", got)
	}
}
