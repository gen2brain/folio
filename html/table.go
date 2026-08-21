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
}

// tableRows collects the rows of a table, through the row groups a document
// writes them in.
func tableRows(t *box) []*box {
	var out []*box
	var walk func(*box)
	walk = func(b *box) {
		for _, k := range b.kids {
			switch k.style.Display {
			case DisplayTableRow:
				if len(out) < maxTableRows {
					out = append(out, k)
				}
			case DisplayTableRowGroup:
				walk(k)
			}
		}
	}
	walk(t)
	return out
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
	rows := tableRows(b)
	cells, ncols := buildGrid(rows)
	if ncols == 0 || len(cells) == 0 {
		for _, k := range b.kids {
			l.block(k, x, avail)
		}
		return
	}
	widths := l.columnWidths(b, cells, ncols, avail)
	edge := make([]float32, ncols+1)
	for i, w := range widths {
		edge[i+1] = edge[i] + w
	}
	for _, c := range cells {
		c.x, c.w = edge[c.col], edge[min(c.col+c.cols, ncols)]-edge[c.col]
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
	rowTop[0] = top
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
			if c.rows == 1 {
				h = max(h, sub.y-rowTop[r])
			}
		}
		rowTop[r+1] = rowTop[r] + h
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

	// Every cell fills the height of the rows it spans: its border and its
	// background reach the ones beside it.
	for _, c := range cells {
		c.box.h = max(rowTop[min(c.row+c.rows, len(rows))]-c.box.y, c.box.h)
	}
	for r := range rows {
		rows[r].x, rows[r].y = x, rowTop[r]
		rows[r].w, rows[r].h = edge[ncols], rowTop[r+1]-rowTop[r]
		l.spans = append(l.spans, lineSpan{top: rowTop[r], bottom: rowTop[r+1], force: l.next})
		l.next = false
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
			want := w.Resolve(avail) + frameWidth(c.box.style)
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
	w := widest(b) + frameWidth(b.style)
	reset(b)
	return w
}

// frameWidth is what the padding and the border of a box add to its content.
func frameWidth(s *Style) float32 {
	return s.PaddingLeft.Resolve(0) + s.PaddingRight.Resolve(0) +
		s.BorderLeft.Thickness() + s.BorderRight.Thickness()
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
