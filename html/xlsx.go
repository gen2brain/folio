package html

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

const (
	xlsxSheet    = "sheets.css"
	xlsxMaxSheet = 1 << 10
	xlsxMaxCols  = 1 << 12
	xlsxMaxRows  = 1 << 20
	// xlsxColWidth is how wide one unit of a column is, in CSS pixels.
	xlsxColWidth = 7
)

// xlsxStyle is what one entry of the cell style table says about a cell.
type xlsxStyle struct {
	format string
	css    string
}

// xlsxSpan is a merged range: the cell it starts at and how far it reaches.
type xlsxSpan struct{ cols, rows int }

type xlsx struct {
	o      *ooxml
	shared []string
	styles []xlsxStyle
	// epoch is the day a serial number counts from, which may move to 1904.
	epoch time.Time
	pages map[string][]byte
	sheet []byte
}

func openXLSX(o *ooxml) (*Document, error) {
	root, err := o.part("xl/workbook.xml")
	if err != nil {
		return nil, err
	}
	c := &xlsx{o: o, epoch: time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC),
		pages: map[string][]byte{}}
	if pr := root.child("workbookPr"); pr != nil && pr.at("date1904") == "1" {
		c.epoch = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	c.readShared()
	c.readStyles()

	d := &Document{kind: KindXLSX}
	d.meta = c.metadata()
	c.sheet = []byte("body{margin:0}\ntable{border-collapse:collapse}\n" +
		"td{border:1px solid #bbb;padding:1px 4px;vertical-align:bottom;" +
		"white-space:pre;font-size:11px}\nh1{font-size:14px;margin:8px 0 4px}\n")

	sheets := root.child("sheets")
	for i, s := range sheets.all("sheet") {
		if i >= xlsxMaxSheet {
			break
		}
		part := o.target("xl/workbook.xml", s.rel("id"))
		if part == "" || !o.has(part) {
			continue
		}
		name := s.at("name")
		if name == "" {
			name = "Sheet " + strconv.Itoa(i+1)
		}
		path := fmt.Sprintf("sheet%d.xhtml", i+1)
		c.pages[path] = c.sheetPart(part, name)
		item := Item{Path: path, Type: "application/xhtml+xml", Linear: true}
		d.spine = append(d.spine, item)
		d.manifest = append(d.manifest, item)
		d.outline = append(d.outline, Outline{Title: name, Path: path})
	}
	if len(d.spine) == 0 {
		return nil, fmt.Errorf("%w: the workbook has no sheets", ErrInvalid)
	}
	d.manifest = append(d.manifest, Item{Path: xlsxSheet, Type: "text/css"})
	d.read = func(p string) ([]byte, error) {
		if b, ok := c.pages[p]; ok {
			return b, nil
		}
		if p == xlsxSheet {
			return c.sheet, nil
		}
		return nil, fmt.Errorf("%w: %s", ErrNotFound, p)
	}
	return d, nil
}

func (c *xlsx) metadata() Metadata {
	var m Metadata
	root, err := c.o.part("docProps/core.xml")
	if err != nil {
		return m
	}
	for _, k := range root.kids {
		v := strings.TrimSpace(k.text)
		switch k.name {
		case "title":
			m.Title = v
		case "creator":
			m.Author = v
		case "description":
			m.Description = v
		}
	}
	return m
}

// readShared holds the strings the cells refer to by index, each of which may
// be written as several runs.
func (c *xlsx) readShared() {
	root, err := c.o.part("xl/sharedStrings.xml")
	if err != nil {
		return
	}
	for _, si := range root.all("si") {
		c.shared = append(c.shared, siText(si))
	}
}

func siText(si *xnode) string {
	if t := si.child("t"); t != nil {
		return t.text
	}
	var b strings.Builder
	for _, r := range si.all("r") {
		if t := r.child("t"); t != nil {
			b.WriteString(t.text)
		}
	}
	return b.String()
}

// readStyles holds what each cell style says: the number format the value is
// written with, and the type and the fill it is drawn in.
func (c *xlsx) readStyles() {
	root, err := c.o.part("xl/styles.xml")
	if err != nil {
		return
	}
	codes := map[string]string{}
	if n := root.child("numFmts"); n != nil {
		for _, f := range n.all("numFmt") {
			codes[f.at("numFmtId")] = f.at("formatCode")
		}
	}
	var fonts, fills []string
	if n := root.child("fonts"); n != nil {
		for _, f := range n.all("font") {
			fonts = append(fonts, xlsxFont(f))
		}
	}
	if n := root.child("fills"); n != nil {
		for _, f := range n.all("fill") {
			fills = append(fills, xlsxFill(f))
		}
	}
	xfs := root.child("cellXfs")
	if xfs == nil {
		return
	}
	for _, xf := range xfs.all("xf") {
		var v xlsxStyle
		id := xf.at("numFmtId")
		if code, ok := codes[id]; ok {
			v.format = code
		} else {
			v.format = xlsxBuiltin(id)
		}
		var css []string
		if i, err := strconv.Atoi(xf.at("fontId")); err == nil && i >= 0 && i < len(fonts) {
			if fonts[i] != "" {
				css = append(css, fonts[i])
			}
		}
		if xf.at("applyFill") != "0" {
			if i, err := strconv.Atoi(xf.at("fillId")); err == nil && i >= 0 && i < len(fills) {
				if fills[i] != "" {
					css = append(css, fills[i])
				}
			}
		}
		if a := xf.child("alignment"); a != nil {
			switch a.at("horizontal") {
			case "center", "centerContinuous":
				css = append(css, "text-align:center")
			case "right":
				css = append(css, "text-align:right")
			case "left":
				css = append(css, "text-align:left")
			}
			if a.at("wrapText") == "1" {
				css = append(css, "white-space:normal")
			}
		}
		v.css = strings.Join(css, ";")
		c.styles = append(c.styles, v)
	}
}

func xlsxFont(f *xnode) string {
	var css []string
	if f.child("b") != nil {
		css = append(css, "font-weight:bold")
	}
	if f.child("i") != nil {
		css = append(css, "font-style:italic")
	}
	if f.child("u") != nil {
		css = append(css, "text-decoration:underline")
	}
	if col := f.child("color"); col != nil {
		if v := argbColor(col.at("rgb")); v != "" {
			css = append(css, "color:"+v)
		}
	}
	if n := f.child("sz"); n != nil {
		if v, err := strconv.ParseFloat(n.at("val"), 64); err == nil && v > 0 {
			css = append(css, "font-size:"+px(round2(v*4/3))+"px")
		}
	}
	if n := f.child("name"); n != nil {
		if v := n.at("val"); v != "" {
			css = append(css, "font-family:"+cssFamily(v))
		}
	}
	return strings.Join(css, ";")
}

func xlsxFill(f *xnode) string {
	p := f.child("patternFill")
	if p == nil || p.at("patternType") != "solid" {
		return ""
	}
	if fg := p.child("fgColor"); fg != nil {
		if v := argbColor(fg.at("rgb")); v != "" {
			return "background-color:" + v
		}
	}
	return ""
}

// argbColor is a colour a workbook writes as alpha and three components.
func argbColor(v string) string {
	if len(v) == 8 {
		v = v[2:]
	}
	return ooxmlColor(v)
}

// sheetPart writes one sheet as a table.
func (c *xlsx) sheetPart(part, name string) []byte {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n" +
		`<html xmlns="http://www.w3.org/1999/xhtml"><head>` +
		`<link rel="stylesheet" type="text/css" href="` + xlsxSheet + `"/>` +
		`<title>` + esc(name) + `</title></head><body><h1>` + esc(name) + "</h1>")
	root, err := c.o.part(part)
	if err != nil {
		b.WriteString("</body></html>")
		return []byte(b.String())
	}
	merged, spans := xlsxMerges(root)
	b.WriteString("<table>")
	c.columns(&b, root)
	for _, row := range rowsOf(root) {
		at, _ := strconv.Atoi(row.at("r"))
		b.WriteString("<tr>")
		col := 0
		for _, cell := range row.all("c") {
			x, y := cellRef(cell.at("r"))
			if x < 0 {
				x, y = col, at
			}
			for ; col < x && col < xlsxMaxCols; col++ {
				if !merged[[2]int{col, y}] {
					b.WriteString("<td></td>")
				}
			}
			col = x + 1
			if merged[[2]int{x, y}] {
				continue
			}
			c.cell(&b, cell, spans[[2]int{x, y}])
		}
		b.WriteString("</tr>")
	}
	b.WriteString("</table></body></html>")
	return []byte(b.String())
}

func rowsOf(root *xnode) []*xnode {
	data := root.child("sheetData")
	if data == nil {
		return nil
	}
	rows := data.all("row")
	if len(rows) > xlsxMaxRows {
		rows = rows[:xlsxMaxRows]
	}
	return rows
}

// columns writes the widths the sheet asks for, which a column repeats over a
// range rather than giving one at a time.
func (c *xlsx) columns(b *strings.Builder, root *xnode) {
	cols := root.child("cols")
	if cols == nil {
		return
	}
	var out strings.Builder
	n := 0
	for _, col := range cols.all("col") {
		from, _ := strconv.Atoi(col.at("min"))
		to, _ := strconv.Atoi(col.at("max"))
		w, err := strconv.ParseFloat(col.at("width"), 64)
		if from <= 0 || to < from || err != nil || w <= 0 {
			continue
		}
		for i := from; i <= to && n < xlsxMaxCols; i, n = i+1, n+1 {
			fmt.Fprintf(&out, `<col style="width:%spx"/>`, px(round2(w*xlsxColWidth)))
		}
	}
	if n > 0 {
		b.WriteString("<colgroup>" + out.String() + "</colgroup>")
	}
}

// xlsxMerges is which cells a merged range covers and how far the first goes.
func xlsxMerges(root *xnode) (map[[2]int]bool, map[[2]int]xlsxSpan) {
	covered := map[[2]int]bool{}
	spans := map[[2]int]xlsxSpan{}
	m := root.child("mergeCells")
	if m == nil {
		return covered, spans
	}
	for _, r := range m.all("mergeCell") {
		from, to, ok := strings.Cut(r.at("ref"), ":")
		if !ok {
			continue
		}
		x0, y0 := cellRef(from)
		x1, y1 := cellRef(to)
		if x0 < 0 || x1 < x0 || y1 < y0 || x1-x0 >= xlsxMaxCols {
			continue
		}
		spans[[2]int{x0, y0}] = xlsxSpan{cols: x1 - x0 + 1, rows: y1 - y0 + 1}
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if x != x0 || y != y0 {
					covered[[2]int{x, y}] = true
				}
			}
		}
	}
	return covered, spans
}

func (c *xlsx) cell(b *strings.Builder, cell *xnode, span xlsxSpan) {
	style := xlsxStyle{}
	if i, err := strconv.Atoi(cell.at("s")); err == nil && i >= 0 && i < len(c.styles) {
		style = c.styles[i]
	}
	b.WriteString("<td")
	if span.cols > 1 {
		b.WriteString(` colspan="` + strconv.Itoa(span.cols) + `"`)
	}
	if span.rows > 1 {
		b.WriteString(` rowspan="` + strconv.Itoa(span.rows) + `"`)
	}
	css := style.css
	if v := cell.at("t"); v == "" || v == "n" {
		if css != "" {
			css += ";"
		}
		css += "text-align:right"
	}
	if css != "" {
		b.WriteString(` style="` + css + `"`)
	}
	b.WriteString(">" + esc(c.value(cell, style.format)) + "</td>")
}

// value is what a cell says, which is a shared string, an inline one, a
// boolean, an error or a number written the way its format asks.
func (c *xlsx) value(cell *xnode, format string) string {
	switch cell.at("t") {
	case "s":
		v := cell.child("v")
		if v == nil {
			return ""
		}
		i, err := strconv.Atoi(strings.TrimSpace(v.text))
		if err != nil || i < 0 || i >= len(c.shared) {
			return ""
		}
		return c.shared[i]
	case "inlineStr":
		if is := cell.child("is"); is != nil {
			return siText(is)
		}
		return ""
	case "str":
		if v := cell.child("v"); v != nil {
			return v.text
		}
		return ""
	case "b":
		if v := cell.child("v"); v != nil && strings.TrimSpace(v.text) == "1" {
			return "TRUE"
		}
		return "FALSE"
	case "e":
		if v := cell.child("v"); v != nil {
			return v.text
		}
		return ""
	}
	v := cell.child("v")
	if v == nil {
		return ""
	}
	raw := strings.TrimSpace(v.text)
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	return c.number(n, format)
}

// number writes a value the way its format asks: as a date, as a percentage,
// or with the separators and the decimals the code names. A code this does
// not read leaves the number as it is.
func (c *xlsx) number(n float64, format string) string {
	format = strings.TrimSpace(format)
	if i := strings.IndexByte(format, ';'); i >= 0 {
		format = format[:i]
	}
	if format == "" || format == "General" || format == "@" {
		return trimFloat(n)
	}
	if layout, ok := dateLayout(format); ok {
		return c.epoch.Add(time.Duration(n * float64(24*time.Hour))).Format(layout)
	}
	digits, percent, group := numberShape(format)
	if percent {
		n *= 100
	}
	s := trimFloat(n)
	if digits >= 0 {
		// A spreadsheet rounds half away from zero and Go rounds half to even.
		if p := math.Pow(10, float64(digits)); p > 0 && !math.IsInf(p, 0) {
			n = math.Round(n*p) / p
		}
		s = strconv.FormatFloat(n, 'f', digits, 64)
	}
	if group {
		s = groupThousands(s)
	}
	if percent {
		s += "%"
	}
	if v := literalPrefix(format); v != "" {
		s = v + s
	}
	return s
}

func trimFloat(n float64) string {
	if n == math.Trunc(n) && math.Abs(n) < 1e15 {
		return strconv.FormatInt(int64(n), 10)
	}
	return strconv.FormatFloat(n, 'g', -1, 64)
}

// dateLayout turns a format code into the layout its tokens name, and reports
// whether the code is a date at all.
func dateLayout(format string) (string, bool) {
	var b strings.Builder
	date, quoted := false, false
	for i := 0; i < len(format); {
		ch := format[i]
		if quoted {
			if ch == '"' {
				quoted = false
			} else {
				b.WriteByte(ch)
			}
			i++
			continue
		}
		switch ch {
		case '"':
			quoted = true
			i++
			continue
		case '[':
			for i < len(format) && format[i] != ']' {
				i++
			}
			i++
			continue
		case '\\':
			if i+1 < len(format) {
				b.WriteByte(format[i+1])
			}
			i += 2
			continue
		case '0', '#', '?', '%', 'E', 'e':
			return "", false
		}
		run := 1
		for i+run < len(format) && lowerByte(format[i+run]) == lowerByte(ch) {
			run++
		}
		switch lowerByte(ch) {
		case 'y':
			if run >= 3 {
				b.WriteString("2006")
			} else {
				b.WriteString("06")
			}
			date = true
		case 'd':
			switch {
			case run >= 4:
				b.WriteString("Monday")
			case run == 3:
				b.WriteString("Mon")
			case run == 2:
				b.WriteString("02")
			default:
				b.WriteString("2")
			}
			date = true
		case 'h':
			if run >= 2 {
				b.WriteString("15")
			} else {
				b.WriteString("3")
			}
			date = true
		case 's':
			if run >= 2 {
				b.WriteString("05")
			} else {
				b.WriteString("5")
			}
			date = true
		case 'm':
			// m is a month beside a year or a day and a minute beside an
			// hour or a second, which is the one ambiguity in the grammar.
			if minuteAt(format, i) {
				if run >= 2 {
					b.WriteString("04")
				} else {
					b.WriteString("4")
				}
			} else {
				switch {
				case run >= 4:
					b.WriteString("January")
				case run == 3:
					b.WriteString("Jan")
				case run == 2:
					b.WriteString("01")
				default:
					b.WriteString("1")
				}
			}
			date = true
		case 'a':
			if strings.HasPrefix(strings.ToUpper(format[i:]), "AM/PM") {
				b.WriteString("PM")
				i += 5
				continue
			}
			b.WriteByte(ch)
			run = 1
		default:
			for k := 0; k < run; k++ {
				b.WriteByte(ch)
			}
		}
		i += run
	}
	return b.String(), date
}

// minuteAt reports the m at an offset standing for a minute, which it does
// when an hour or a second is what it sits beside.
func minuteAt(format string, at int) bool {
	for i := at - 1; i >= 0; i-- {
		switch lowerByte(format[i]) {
		case 'h':
			return true
		case 'y', 'd':
			return false
		case ':', ' ', '.':
		default:
			return false
		}
	}
	for i := at + 1; i < len(format); i++ {
		switch lowerByte(format[i]) {
		case 's':
			return true
		case 'y', 'd':
			return false
		case 'm', ':', ' ', '.':
		default:
			return false
		}
	}
	return false
}

func lowerByte(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + 'a' - 'A'
	}
	return c
}

// numberShape is how many decimals a code asks for, whether it is a
// percentage, and whether it separates thousands.
func numberShape(format string) (digits int, percent, group bool) {
	digits = -1
	dot := strings.IndexByte(format, '.')
	if dot >= 0 {
		digits = 0
		for i := dot + 1; i < len(format); i++ {
			if format[i] != '0' && format[i] != '#' && format[i] != '?' {
				break
			}
			digits++
		}
	} else if strings.ContainsAny(format, "0#?") {
		digits = 0
	}
	return digits, strings.Contains(format, "%"), strings.Contains(format, "#,#")
}

func groupThousands(s string) string {
	sign := ""
	if strings.HasPrefix(s, "-") {
		sign, s = "-", s[1:]
	}
	whole, rest, _ := strings.Cut(s, ".")
	if rest != "" {
		rest = "." + rest
	}
	var b strings.Builder
	for i, r := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return sign + b.String() + rest
}

// literalPrefix is the text a code puts in front of the number, which is how
// a currency is written.
func literalPrefix(format string) string {
	var b strings.Builder
	for i := 0; i < len(format); i++ {
		switch format[i] {
		case '"':
			for i++; i < len(format) && format[i] != '"'; i++ {
				b.WriteByte(format[i])
			}
		case '\\':
			if i++; i < len(format) {
				b.WriteByte(format[i])
			}
		case '0', '#', '?':
			return b.String()
		case '[':
			for i < len(format) && format[i] != ']' {
				i++
			}
		default:
			if format[i] != ' ' {
				b.WriteByte(format[i])
			}
		}
	}
	return b.String()
}

// cellRef is the column and the row a reference like AB12 names, counting
// columns from zero and rows the way the file does.
func cellRef(ref string) (col, row int) {
	i := 0
	for ; i < len(ref); i++ {
		c := lowerByte(ref[i])
		if c < 'a' || c > 'z' {
			break
		}
		col = col*26 + int(c-'a') + 1
		if col > xlsxMaxCols {
			return -1, 0
		}
	}
	if i == 0 || i >= len(ref) {
		return -1, 0
	}
	n, err := strconv.Atoi(ref[i:])
	if err != nil || n <= 0 {
		return -1, 0
	}
	return col - 1, n
}

// xlsxBuiltin is the format a code below 164 stands for, ECMA-376 18.8.30.
func xlsxBuiltin(id string) string {
	switch id {
	case "1":
		return "0"
	case "2":
		return "0.00"
	case "3":
		return "#,##0"
	case "4":
		return "#,##0.00"
	case "9":
		return "0%"
	case "10":
		return "0.00%"
	case "11":
		return "0.00E+00"
	case "14":
		return "mm-dd-yy"
	case "15":
		return "d-mmm-yy"
	case "16":
		return "d-mmm"
	case "17":
		return "mmm-yy"
	case "18":
		return "h:mm AM/PM"
	case "19":
		return "h:mm:ss AM/PM"
	case "20":
		return "h:mm"
	case "21":
		return "h:mm:ss"
	case "22":
		return "m/d/yy h:mm"
	case "37", "38":
		return "#,##0"
	case "39", "40":
		return "#,##0.00"
	case "45":
		return "mm:ss"
	case "46":
		return "[h]:mm:ss"
	case "47":
		return "mm:ss.0"
	case "48":
		return "##0.0E+0"
	case "49":
		return "@"
	}
	return ""
}
