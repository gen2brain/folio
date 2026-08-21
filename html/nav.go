package html

import (
	"encoding/xml"
	"path"
	"strings"
)

// maxNavDepth bounds a table of contents that nests forever, which a damaged
// book is free to carry.
const maxNavDepth = 32

// readNav reads the table of contents, from the EPUB 3 navigation document
// when the manifest declares one and from the EPUB 2 NCX otherwise. A book
// may carry both, and the navigation document is the one it means.
func (d *Document) readNav() {
	for _, item := range d.manifest {
		if hasProperty(item, "nav") {
			if o := d.readNavDoc(item.Path); len(o) > 0 {
				d.outline = o
				return
			}
			break
		}
	}
	for _, item := range d.manifest {
		if item.ID == d.tocID || item.Type == "application/x-dtbncx+xml" {
			if o := d.readNCX(item.Path); len(o) > 0 {
				d.outline = o
				return
			}
		}
	}
}

func hasProperty(item Item, name string) bool {
	for _, p := range item.Properties {
		if p == name {
			return true
		}
	}
	return false
}

// navList is one level of the navigation document's nested lists.
type navList struct {
	Items []navItem `xml:"li"`
}

type navItem struct {
	Anchor struct {
		Href string `xml:"href,attr"`
		Text inner  `xml:",any"`
		Own  string `xml:",chardata"`
	} `xml:"a"`
	Span inner   `xml:"span"`
	List navList `xml:"ol"`
}

// inner is the text of an element and of everything nested in it, which a
// table of contents needs because a title is as likely to be wrapped in a
// span as to stand on its own.
type inner struct {
	Own      string  `xml:",chardata"`
	Children []inner `xml:",any"`
}

func (t inner) String() string {
	s := t.Own
	for _, c := range t.Children {
		s += c.String()
	}
	return s
}

// readNavDoc reads the EPUB 3 navigation document, which is XHTML holding a
// nav element of nested ordered lists. The nav may be anywhere in the body,
// wrapped in whatever sections a designer felt like, so the document is
// scanned for it rather than matched against a shape.
func (d *Document) readNavDoc(p string) []Outline {
	b, err := d.Read(p)
	if err != nil {
		return nil
	}
	dec := lenient(b)
	var first navList
	found := false
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "nav" {
			continue
		}
		var nav struct {
			List navList `xml:"ol"`
		}
		if err := dec.DecodeElement(&nav, &start); err != nil {
			break
		}
		// The type attribute is epub:type, which the decoder hands over
		// under its local name.
		if hasWord(attr(start, "type"), "toc") {
			return d.navEntries(nav.List, p, 0)
		}
		if !found {
			first, found = nav.List, true
		}
	}
	if found {
		return d.navEntries(first, p, 0)
	}
	return nil
}

// attr returns an attribute by local name, whatever namespace it is in.
func attr(e xml.StartElement, name string) string {
	for _, a := range e.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// hasWord reports whether a whitespace separated list holds a word, which is
// how epub:type and the manifest properties are written.
func hasWord(list, want string) bool {
	for _, w := range strings.Fields(list) {
		if w == want {
			return true
		}
	}
	return false
}

func (d *Document) navEntries(list navList, base string, depth int) []Outline {
	if depth > maxNavDepth {
		return nil
	}
	var out []Outline
	for _, li := range list.Items {
		e := Outline{Title: squash(li.Anchor.Own + li.Anchor.Text.String())}
		if e.Title == "" {
			e.Title = squash(li.Span.String())
		}
		if li.Anchor.Href != "" {
			e.Path, e.Fragment = d.link(base, li.Anchor.Href)
		}
		e.Children = d.navEntries(li.List, base, depth+1)
		if e.Title == "" && len(e.Children) == 0 {
			continue
		}
		out = append(out, e)
	}
	return out
}

// ncxPoint is one entry of the EPUB 2 table of contents.
type ncxPoint struct {
	Label struct {
		Text string `xml:"text"`
	} `xml:"navLabel"`
	Content struct {
		Src string `xml:"src,attr"`
	} `xml:"content"`
	Points []ncxPoint `xml:"navPoint"`
}

// readNCX reads the EPUB 2 table of contents.
func (d *Document) readNCX(p string) []Outline {
	b, err := d.Read(p)
	if err != nil {
		return nil
	}
	var doc struct {
		Points []ncxPoint `xml:"navMap>navPoint"`
	}
	if err := unmarshal(b, &doc); err != nil {
		return nil
	}
	return d.ncxEntries(doc.Points, p, 0)
}

func (d *Document) ncxEntries(points []ncxPoint, base string, depth int) []Outline {
	if depth > maxNavDepth {
		return nil
	}
	var out []Outline
	for _, pt := range points {
		e := Outline{Title: squash(pt.Label.Text)}
		if pt.Content.Src != "" {
			e.Path, e.Fragment = d.link(base, pt.Content.Src)
		}
		e.Children = d.ncxEntries(pt.Points, base, depth+1)
		out = append(out, e)
	}
	return out
}

// link resolves a reference inside a part into the path of another and the
// anchor in it.
func (d *Document) link(base, ref string) (string, string) {
	href, frag := splitFragment(ref)
	if href == "" {
		return base, frag
	}
	if strings.Contains(href, "://") {
		return href, frag
	}
	return path.Join(path.Dir(base), href), frag
}

// squash collapses the whitespace a table of contents is indented with.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
