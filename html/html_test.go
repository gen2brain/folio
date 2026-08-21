package html

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
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
		be32(rec0[0xf0:], 2)
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
