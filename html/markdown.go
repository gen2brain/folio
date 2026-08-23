package html

import (
	"strconv"
	"strings"

	xhtml "golang.org/x/net/html"
)

// mdMaxDepth bounds how deep a tree the writer walks.
const mdMaxDepth = 256

// Markdown returns the document as Markdown: its headings, lists, tables,
// links, pictures and emphasis. A construct Markdown has no syntax for is
// written as the HTML it already is.
func (d *Document) Markdown() (string, error) {
	var b strings.Builder
	var first error
	for _, item := range d.Spine() {
		var part string
		switch {
		case item.IsChapter():
			root, err := d.ParsePart(item.Path)
			if err != nil {
				if first == nil {
					first = err
				}
				continue
			}
			part = NodeMarkdown(root)
		default:
			s, err := d.drawnText(item)
			if err != nil && first == nil {
				first = err
			}
			part = strings.TrimSpace(s)
		}
		if part == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(part)
	}
	return b.String(), first
}

// NodeMarkdown writes one tree as Markdown.
func NodeMarkdown(n *Node) string {
	w := &md{}
	w.blocks(n, 0)
	return strings.TrimLeft(w.b.String(), "\n")
}

// md writes Markdown a block at a time.
type md struct {
	b      strings.Builder
	prefix string
	tight  bool
}

func (w *md) block(s string) {
	s = strings.Trim(s, "\n")
	if s == "" {
		return
	}
	if w.b.Len() > 0 && !strings.HasSuffix(w.b.String(), "\n\n") &&
		!strings.HasSuffix(w.b.String(), strings.TrimRight(w.prefix, " ")+"\n") {
		w.b.WriteString(strings.TrimRight(w.prefix, " ") + "\n")
	}
	for _, line := range strings.Split(s, "\n") {
		if line == "" {
			w.b.WriteString(strings.TrimRight(w.prefix, " ") + "\n")
			continue
		}
		w.b.WriteString(w.prefix + line + "\n")
	}
	if !w.tight {
		w.b.WriteString(strings.TrimRight(w.prefix, " ") + "\n")
	}
}

func (w *md) blocks(n *Node, depth int) {
	if n == nil || depth > mdMaxDepth {
		return
	}
	var run strings.Builder
	flush := func() {
		w.block(mdSpace(run.String()))
		run.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.TextNode {
			run.WriteString(c.Data)
			continue
		}
		if c.Type != xhtml.ElementNode {
			continue
		}
		if mdSkip[c.Data] {
			continue
		}
		if !mdBlock[c.Data] {
			run.WriteString(w.inline(c, depth+1))
			continue
		}
		flush()
		w.element(c, depth+1)
	}
	flush()
}

var mdSkip = map[string]bool{
	"head": true, "script": true, "style": true, "template": true,
	"noscript": true, "title": true, "meta": true, "link": true,
}

var mdBlock = map[string]bool{
	"address": true, "article": true, "aside": true, "blockquote": true,
	"body": true, "center": true, "dd": true, "details": true, "div": true,
	"dl": true, "dt": true, "figcaption": true, "figure": true,
	"footer": true, "form": true, "h1": true, "h2": true, "h3": true,
	"h4": true, "h5": true, "h6": true, "header": true, "hgroup": true,
	"hr": true, "html": true, "li": true, "main": true, "nav": true,
	"ol": true, "p": true, "pre": true, "section": true, "table": true,
	"ul": true,
}

func (w *md) element(n *Node, depth int) {
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		level, _ := strconv.Atoi(n.Data[1:])
		if s := strings.TrimSpace(mdSpace(w.inline(n, depth))); s != "" {
			w.block(strings.Repeat("#", level) + " " + oneLine(s))
		}
	case "p":
		w.block(mdLead(strings.TrimSpace(mdSpace(w.inline(n, depth)))))
	case "hr":
		w.block("---")
	case "pre":
		w.block("```\n" + strings.Trim(NodeText(n), "\n") + "\n```")
	case "blockquote":
		w.nested("> ", "> ", n, depth)
	case "ul", "ol":
		w.list(n, depth)
	case "dl":
		w.blocks(n, depth)
	case "dt":
		if s := strings.TrimSpace(mdSpace(w.inline(n, depth))); s != "" {
			w.block("**" + oneLine(s) + "**")
		}
	case "dd":
		w.nested("  ", "  ", n, depth)
	case "table":
		w.table(n, depth)
	case "li":
		w.blocks(n, depth)
	default:
		w.blocks(n, depth)
	}
}

func (w *md) nested(first, rest string, n *Node, depth int) {
	sub := &md{prefix: rest, tight: w.tight}
	sub.blocks(n, depth)
	s := trimBlank(sub.b.String(), rest)
	if s == "" {
		return
	}
	if first != rest && strings.HasPrefix(s, rest) {
		s = first + s[len(rest):]
	}
	w.block(s)
}

func (w *md) list(n *Node, depth int) {
	ordered := n.Data == "ol"
	at := 1
	if v, err := strconv.Atoi(Attr(n, "start")); err == nil && v > 0 {
		at = v
	}
	tight := !mdLoose(n)
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode {
			continue
		}
		if c.Data == "ul" || c.Data == "ol" {
			sub := &md{prefix: "  ", tight: tight}
			sub.list(c, depth+1)
			if v := trimBlank(sub.b.String(), sub.prefix); v != "" {
				b.WriteString(v + "\n")
			}
			continue
		}
		if c.Data != "li" {
			continue
		}
		marker := "- "
		if ordered {
			marker = strconv.Itoa(at) + ". "
			at++
		}
		sub := &md{prefix: strings.Repeat(" ", len(marker)), tight: tight}
		sub.blocks(c, depth+1)
		item := trimBlank(sub.b.String(), sub.prefix)
		if item == "" {
			item = strings.Repeat(" ", len(marker))
		}
		if strings.HasPrefix(item, sub.prefix) {
			item = marker + item[len(sub.prefix):]
		} else {
			item = marker + item
		}
		b.WriteString(item + "\n")
		if !tight {
			b.WriteString("\n")
		}
	}
	w.block(b.String())
}

// mdLoose reports a list whose items hold more than one block.
func mdLoose(n *Node) bool {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != xhtml.ElementNode || c.Data != "li" {
			continue
		}
		blocks := 0
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			if k.Type == xhtml.ElementNode && mdBlock[k.Data] {
				blocks++
			}
		}
		if blocks > 1 {
			return true
		}
	}
	return false
}

// table writes a pipe table, and the HTML itself for one Markdown cannot hold.
func (w *md) table(n *Node, depth int) {
	rows := mdRows(n, depth)
	if rows == nil {
		w.block(strings.TrimSpace(renderNode(n)))
		return
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return
	}
	var b strings.Builder
	for i, r := range rows {
		b.WriteString("|")
		for j := 0; j < cols; j++ {
			cell := ""
			if j < len(r) {
				cell = r[j]
			}
			b.WriteString(" " + cell + " |")
		}
		b.WriteString("\n")
		if i == 0 {
			b.WriteString("|")
			for range cols {
				b.WriteString(" --- |")
			}
			b.WriteString("\n")
		}
	}
	w.block(b.String())
}

// mdRows is the cells of a table, nothing for one a pipe table cannot hold.
func mdRows(n *Node, depth int) [][]string {
	var rows [][]string
	ok := true
	var walk func(*Node, int)
	walk = func(v *Node, d int) {
		if !ok || d > mdMaxDepth {
			return
		}
		for c := v.FirstChild; c != nil; c = c.NextSibling {
			if c.Type != xhtml.ElementNode {
				continue
			}
			switch c.Data {
			case "tr":
				var row []string
				for k := c.FirstChild; k != nil; k = k.NextSibling {
					if k.Type != xhtml.ElementNode || (k.Data != "td" && k.Data != "th") {
						continue
					}
					if spans(k) {
						ok = false
						return
					}
					cell, fits := mdCell(k, d+1)
					if !fits {
						ok = false
						return
					}
					row = append(row, cell)
				}
				rows = append(rows, row)
			case "thead", "tbody", "tfoot", "colgroup", "caption":
				walk(c, d+1)
			case "table":
				ok = false
				return
			}
		}
	}
	walk(n, depth)
	if !ok || len(rows) == 0 {
		return nil
	}
	return rows
}

func spans(n *Node) bool {
	for _, name := range [2]string{"colspan", "rowspan"} {
		if v, err := strconv.Atoi(Attr(n, name)); err == nil && v > 1 {
			return true
		}
	}
	return false
}

// mdCell is what one cell says on the single line a pipe table gives it.
func mdCell(n *Node, depth int) (string, bool) {
	var parts []string
	w := &md{}
	var run strings.Builder
	flush := func() {
		if v := oneLine(strings.TrimSpace(run.String())); v != "" {
			parts = append(parts, v)
		}
		run.Reset()
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == xhtml.TextNode:
			run.WriteString(mdEscape(c.Data))
		case c.Type != xhtml.ElementNode, mdSkip[c.Data]:
		case c.Data == "p", c.Data == "div":
			flush()
			if v := oneLine(strings.TrimSpace(w.inline(c, depth+1))); v != "" {
				parts = append(parts, v)
			}
		case mdBlock[c.Data]:
			return "", false
		default:
			run.WriteString(w.inline(c, depth+1))
		}
	}
	flush()
	return strings.ReplaceAll(strings.Join(parts, "<br>"), "|", "\\|"), true
}

func (w *md) inline(n *Node, depth int) string {
	var b strings.Builder
	w.runs(&b, n, depth)
	return b.String()
}

func (w *md) runs(b *strings.Builder, n *Node, depth int) {
	if depth > mdMaxDepth {
		return
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		switch {
		case c.Type == xhtml.TextNode:
			b.WriteString(mdEscape(c.Data))
			continue
		case c.Type != xhtml.ElementNode, mdSkip[c.Data]:
			continue
		}
		switch c.Data {
		case "br":
			b.WriteString("  \n")
		case "img":
			src := Attr(c, "src")
			if src == "" {
				continue
			}
			b.WriteString("![" + mdEscape(Attr(c, "alt")) + "](" + mdURL(src) + ")")
		case "a":
			href := Attr(c, "href")
			text := w.inline(c, depth+1)
			if href == "" {
				b.WriteString(text)
				continue
			}
			if strings.TrimSpace(text) == "" {
				continue
			}
			b.WriteString("[" + oneLine(text) + "](" + mdURL(href) + ")")
		case "strong", "b":
			w.wrap(b, c, "**", depth)
		case "em", "i", "cite", "var", "dfn":
			w.wrap(b, c, "*", depth)
		case "code", "kbd", "samp", "tt":
			w.code(b, c, depth)
		case "s", "del", "strike":
			w.wrap(b, c, "~~", depth)
		case "sup", "sub", "mark", "abbr":
			b.WriteString("<" + c.Data + ">")
			w.runs(b, c, depth+1)
			b.WriteString("</" + c.Data + ">")
		default:
			w.runs(b, c, depth+1)
		}
	}
}

func (w *md) wrap(b *strings.Builder, n *Node, mark string, depth int) {
	s := w.inline(n, depth+1)
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		b.WriteString(s)
		return
	}
	lead := s[:strings.Index(s, trimmed[:1])]
	tail := s[len(lead)+len(trimmed):]
	b.WriteString(lead + mark + trimmed + mark + tail)
}

// code writes a run of code in the fewest backticks that can hold it.
func (w *md) code(b *strings.Builder, n *Node, depth int) {
	s := NodeText(n)
	if strings.TrimSpace(s) == "" {
		return
	}
	fence := "`"
	for strings.Contains(s, fence) {
		fence += "`"
	}
	pad := ""
	if strings.HasPrefix(s, "`") || strings.HasSuffix(s, "`") {
		pad = " "
	}
	b.WriteString(fence + pad + s + pad + fence)
}

// mdEscape puts a backslash in front of what Markdown would otherwise read
// as syntax. An underscore inside a word is left alone.
func mdEscape(s string) string {
	if !strings.ContainsAny(s, "\\`*_[]<") {
		return s
	}
	var b strings.Builder
	r := []rune(s)
	for i, c := range r {
		switch c {
		case '\\', '`', '*', '[', ']', '<':
			b.WriteByte('\\')
		case '_':
			if !(i > 0 && wordRune(r[i-1]) && i+1 < len(r) && wordRune(r[i+1])) {
				b.WriteByte('\\')
			}
		}
		b.WriteRune(c)
	}
	return b.String()
}

func wordRune(r rune) bool {
	return r >= '0' && r <= '9' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 0x7f
}

// mdLead escapes a character at the head of a paragraph that would otherwise
// begin a heading, a list, a quotation or a rule.
func mdLead(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '#', '>', '-', '+', '=', '|':
		return "\\" + s
	}
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			continue
		}
		if i > 0 && (s[i] == '.' || s[i] == ')') {
			return s[:i] + "\\" + s[i:]
		}
		break
	}
	return s
}

// mdURL writes a link address, wrapping one with a space in angle brackets.
func mdURL(s string) string {
	s = strings.TrimSpace(s)
	if strings.ContainsAny(s, " ()") {
		return "<" + strings.ReplaceAll(s, ">", "%3E") + ">"
	}
	return s
}

// trimBlank drops the empty lines at either end of a block.
func trimBlank(s, prefix string) string {
	bare := strings.TrimRight(prefix, " ")
	lines := strings.Split(strings.Trim(s, "\n"), "\n")
	for len(lines) > 0 && strings.TrimRight(lines[0], " ") == bare {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimRight(lines[len(lines)-1], " ") == bare {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

// mdSpace collapses the white space of a run, keeping the two spaces before
// a newline and whatever a code span holds.
func mdSpace(s string) string {
	var b strings.Builder
	space, brk := false, false
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '`' {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			space, brk = false, false
			j := mdCodeEnd(s, i)
			b.WriteString(s[i:j])
			i = j - 1
			continue
		}
		if c == ' ' && i+2 < len(s) && s[i+1] == ' ' && s[i+2] == '\n' {
			brk = true
			i += 2
			space = false
			continue
		}
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' {
			space = true
			continue
		}
		switch {
		case brk:
			b.WriteString("  \n")
		case space && b.Len() > 0:
			b.WriteByte(' ')
		}
		space, brk = false, false
		b.WriteByte(c)
	}
	return b.String()
}

// mdCodeEnd is where the code span beginning at i ends.
func mdCodeEnd(s string, i int) int {
	n := 0
	for i+n < len(s) && s[i+n] == '`' {
		n++
	}
	fence := s[i : i+n]
	if j := strings.Index(s[i+n:], fence); j >= 0 {
		return i + n + j + n
	}
	return len(s)
}

// oneLine folds a run onto one line.
func oneLine(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "  \n", " ")), " ")
}

func renderNode(n *Node) string {
	var b strings.Builder
	if err := xhtml.Render(&b, n); err != nil {
		return ""
	}
	return b.String()
}
