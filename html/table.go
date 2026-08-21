package html

import "strconv"

// The bounds a table grid is built inside, which a document may ask to
// exceed and which nothing real needs.
const (
	maxTableSpan  = 1000
	maxTableCols  = 512
	maxTableRows  = 4096
	maxTableSlots = 1 << 18
)

// cell is one cell of a table, with the place in the grid it takes and how
// wide its content wants to be.
type cell struct {
	box      *box
	row, col int
	rows     int
	cols     int
	min, max float32
	x, w     float32
	// content is how tall what the cell holds came out, and base how far the
	// first baseline in it sits below the top of the cell.
	content, base float32
}

// tableRows collects the rows of a table, through the row groups a document
// writes them in, and the group each row came out of.
func tableRows(t *box) ([]*box, []*box) {
	var out, groups []*box
	var walk func(b, group *box)
	walk = func(b, group *box) {
		for _, k := range b.kids {
			switch k.style.Display {
			case DisplayTableRow:
				if len(out) < maxTableRows {
					out = append(out, k)
					groups = append(groups, group)
				}
			case DisplayTableRowGroup:
				walk(k, k)
			}
		}
	}
	walk(t, nil)
	return out, groups
}

// buildGrid gives every cell a row and a column, stepping over the slots the
// cells above and beside it span into.
func buildGrid(rows []*box) ([]*cell, int) {
	taken := map[[2]int]bool{}
	var cells []*cell
	ncols := 0
	for r, row := range rows {
		c := 0
		for _, kb := range row.kids {
			if kb.style.Display != DisplayTableCell {
				continue
			}
			for taken[[2]int{r, c}] {
				c++
			}
			if c >= maxTableCols {
				break
			}
			cs := min(max(spanOf(kb, "colspan"), 1), maxTableSpan)
			rs := min(max(spanOf(kb, "rowspan"), 1), maxTableSpan)
			cs = min(cs, maxTableCols-c)
			rs = min(rs, max(len(rows)-r, 1))
			if len(taken)+rs*cs > maxTableSlots {
				return cells, ncols
			}
			for i := range rs {
				for j := range cs {
					taken[[2]int{r + i, c + j}] = true
				}
			}
			cells = append(cells, &cell{box: kb, row: r, col: c, rows: rs, cols: cs})
			c += cs
			ncols = max(ncols, c)
		}
	}
	return cells, ncols
}

func spanOf(b *box, name string) int {
	if b.node == nil {
		return 1
	}
	n, err := strconv.Atoi(Attr(b.node, name))
	if err != nil {
		return 1
	}
	return n
}

// table lays a table out: the columns from what the cells want, then the rows
// from what the cells become once they have their width.
func (l *layout) table(b *box, x, avail float32) {
	rows, _ := tableRows(b)
	cells, ncols := buildGrid(rows)
	if ncols == 0 || len(cells) == 0 {
		for _, k := range b.kids {
			l.block(k, x, avail)
		}
		return
	}
	g := b.collapsed
	sx, sy := b.style.SpacingX.Resolve(0), b.style.SpacingY.Resolve(0)
	if g != nil {
		sx, sy = 0, 0
	}

	widths := l.columnWidths(b, cells, ncols, max(avail-float32(ncols+1)*sx, 0))
	edge := make([]float32, ncols+1)
	edge[0] = sx
	for i, w := range widths {
		edge[i+1] = edge[i] + w + sx
	}
	for _, c := range cells {
		c.x = edge[c.col]
		c.w = edge[min(c.col+c.cols, ncols)] - c.x - sx
	}

	// A caption is a block of its own above the rows.
	for _, k := range b.kids {
		if k.style.Display == DisplayTableCaption {
			l.block(k, x, avail)
		}
	}

	l.apply()
	top := l.y
	rowTop := make([]float32, len(rows)+1)
	rowTop[0] = top + sy
	byRow := make([][]*cell, len(rows))
	for _, c := range cells {
		byRow[c.row] = append(byRow[c.row], c)
	}
	for r := range rows {
		h := float32(0)
		for _, c := range byRow[r] {
			sub := l.sub(rowTop[r])
			sub.block(c.box, x+c.x, c.w)
			sub.apply()
			l.errs = append(l.errs, sub.errs...)
			c.content = sub.y - rowTop[r]
			c.base = firstBaseline(c.box) - rowTop[r]
			if c.rows == 1 {
				h = max(h, c.content)
			}
		}
		rowTop[r+1] = rowTop[r] + h + sy
	}

	// A cell that spans rows may be taller than the rows it spans, which
	// pushes every row below it down.
	for _, c := range cells {
		if c.rows == 1 {
			continue
		}
		last := min(c.row+c.rows, len(rows))
		if grow := (c.box.y + c.box.h) - rowTop[last]; grow > 0 {
			for r := last; r <= len(rows); r++ {
				rowTop[r] += grow
			}
			for r := last; r < len(rows); r++ {
				for _, k := range byRow[r] {
					shift(k.box, 0, grow)
				}
			}
		}
	}

	alignCells(cells, byRow, rowTop, sy, len(rows))

	// Every cell fills the height of the rows it spans: its border and its
	// background reach the ones beside it.
	for _, c := range cells {
		c.box.h = max(rowTop[min(c.row+c.rows, len(rows))]-c.box.y, c.box.h)
	}
	for r := range rows {
		rows[r].x, rows[r].y = x, rowTop[r]
		rows[r].w, rows[r].h = edge[ncols], rowTop[r+1]-rowTop[r]-sy
		l.spans = append(l.spans, lineSpan{top: rowTop[r], bottom: rowTop[r+1], force: l.next})
		l.next = false
	}
	if g != nil {
		b.edges = g.lines(x, edge, rowTop)
	}
	b.natural = edge[ncols] + frameWidth(b.style)
	if b.style.Width.Auto() {
		b.w = b.natural
	}
	l.y = rowTop[len(rows)]
}

// columnWidths works out how wide each column is: what its cells need at
// their narrowest, what they would take if nothing wrapped, and where between
// the two the table has room to put them.
func (l *layout) columnWidths(t *box, cells []*cell, ncols int, avail float32) []float32 {
	lo := make([]float32, ncols)
	hi := make([]float32, ncols)
	for _, c := range cells {
		c.min, c.max = l.contentWidths(c.box)
		if w := c.box.style.Width; !w.Auto() {
			want := w.Resolve(avail) + boxFrame(c.box)
			c.min, c.max = max(c.min, want), max(c.max, want)
		}
		if c.cols != 1 {
			continue
		}
		lo[c.col] = max(lo[c.col], c.min)
		hi[c.col] = max(hi[c.col], c.max)
	}
	for _, c := range cells {
		if c.cols == 1 {
			continue
		}
		end := min(c.col+c.cols, ncols)
		spread(lo[c.col:end], c.min)
		spread(hi[c.col:end], c.max)
	}

	var sumLo, sumHi float32
	for i := range ncols {
		hi[i] = max(hi[i], lo[i])
		sumLo += lo[i]
		sumHi += hi[i]
	}
	want := min(sumHi, avail)
	if w := t.style.Width; !w.Auto() {
		want = w.Resolve(avail)
	}
	want = max(want, sumLo)

	out := make([]float32, ncols)
	switch {
	case want >= sumHi:
		copy(out, hi)
		if sumHi > 0 && want > sumHi {
			for i := range out {
				out[i] += (want - sumHi) * hi[i] / sumHi
			}
		}
	case sumHi > sumLo:
		f := (want - sumLo) / (sumHi - sumLo)
		for i := range out {
			out[i] = lo[i] + f*(hi[i]-lo[i])
		}
	default:
		copy(out, lo)
	}
	return out
}

// spread raises a run of columns until together they hold a cell that spans
// them all.
func spread(cols []float32, want float32) {
	have := float32(0)
	for _, c := range cols {
		have += c
	}
	if want <= have || len(cols) == 0 {
		return
	}
	each := (want - have) / float32(len(cols))
	for i := range cols {
		cols[i] += each
	}
}

// contentWidths is what a cell takes when every break is used and when none
// is, which the column widths are worked out from.
func (l *layout) contentWidths(b *box) (float32, float32) {
	mn := l.probe(b, 0)
	mx := l.probe(b, maxContentWidth)
	return mn, max(mx, mn)
}

// maxContentWidth is the width a cell is measured at to find out what it
// takes when nothing wraps.
const maxContentWidth = 1 << 16

func (l *layout) probe(b *box, avail float32) float32 {
	if !l.spend() {
		return 0
	}
	p := l.sub(0)
	p.flow(b, 0, avail)
	w := widest(b) + boxFrame(b)
	reset(b)
	return w
}

// frameWidth is what the padding and the border of a box add to its content.
func frameWidth(s *Style) float32 {
	return s.PaddingLeft.Resolve(0) + s.PaddingRight.Resolve(0) +
		s.BorderLeft.Thickness() + s.BorderRight.Thickness()
}

// boxFrame is frameWidth for a box, which in a collapsed table gives its
// borders half the room a cell beside it gives the same edge.
func boxFrame(b *box) float32 {
	if b.inset == nil {
		return frameWidth(b.style)
	}
	return b.style.PaddingLeft.Resolve(0) + b.style.PaddingRight.Resolve(0) +
		b.inset[3] + b.inset[1]
}

// alignCells places what each cell holds inside the height its rows came to,
// which is what vertical-align means on a table cell. Its initial value lines
// the first baseline of every cell in a row up with the lowest of them, and
// that is what a table with no style of its own gets.
func alignCells(cells []*cell, byRow [][]*cell, rowTop []float32, sy float32, nrows int) {
	base := make([]float32, len(byRow))
	for r, row := range byRow {
		for _, c := range row {
			if c.rows == 1 && cellAlign(c) == AlignBaseline {
				base[r] = max(base[r], c.base)
			}
		}
	}
	for _, c := range cells {
		room := rowTop[min(c.row+c.rows, nrows)] - sy - rowTop[c.row]
		dy := float32(0)
		switch cellAlign(c) {
		case AlignBaseline:
			dy = base[c.row] - c.base
		case AlignMiddle:
			dy = (room - c.content) / 2
		case AlignBottom:
			dy = room - c.content
		}
		if dy > 0 {
			shiftContent(c.box, dy)
		}
	}
}

// cellAlign is the alignment a cell asks for, where the four a table cell
// takes are not the ones an inline box takes: everything else is its top.
func cellAlign(c *cell) VerticalAlign {
	switch v := c.box.style.VerticalAlign; v {
	case AlignBaseline, AlignMiddle, AlignBottom:
		return v
	}
	return AlignTop
}

// firstBaseline is where the first line of a subtree sits. A cell with no
// line at all has no baseline, and CSS 2.1 17.5.4 puts it at the bottom of
// what the cell holds.
func firstBaseline(b *box) float32 {
	if v, ok := lineBaseline(b); ok {
		return v
	}
	return b.y + b.h
}

func lineBaseline(b *box) (float32, bool) {
	if len(b.lines) > 0 {
		return b.lines[0].y + b.lines[0].baseline, true
	}
	for _, k := range b.kids {
		if v, ok := lineBaseline(k); ok {
			return v, true
		}
	}
	return 0, false
}

// shiftContent moves what a box holds without moving the box, which is how a
// cell's border and background stay where the row put them.
func shiftContent(b *box, dy float32) {
	for i := range b.lines {
		b.lines[i].y += dy
	}
	for _, k := range b.kids {
		shift(k, 0, dy)
	}
}

// shift moves a laid out subtree.
func shift(b *box, dx, dy float32) {
	b.x += dx
	b.y += dy
	for i := range b.lines {
		b.lines[i].y += dy
		for j := range b.lines[i].frags {
			b.lines[i].frags[j].x += dx
		}
	}
	for _, k := range b.kids {
		shift(k, dx, dy)
	}
}
