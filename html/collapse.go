package html

// grid holds the border every segment of a collapsed table resolved to: one
// vertical border per column edge and row, one horizontal per row and column.
type grid struct {
	ncols, nrows int
	vert         []Border
	horz         []Border
}

func newGrid(ncols, nrows int) *grid {
	return &grid{ncols: ncols, nrows: nrows,
		vert: make([]Border, (ncols+1)*nrows),
		horz: make([]Border, (nrows+1)*ncols)}
}

func (g *grid) v(col, row int) *Border { return &g.vert[col*g.nrows+row] }
func (g *grid) h(row, col int) *Border { return &g.horz[row*g.ncols+col] }

// The order two borders of the same width are ranked in, which is the style
// priority of CSS 2.1 with the three dimensional styles drawn solid.
func borderRank(s BorderStyle) int {
	switch s {
	case BorderDouble:
		return 4
	case BorderSolid:
		return 3
	case BorderDashed:
		return 2
	case BorderDotted:
		return 1
	}
	return 0
}

// stronger is the border that wins an edge two boxes share. The nearer owner
// comes first, and wins a tie.
func stronger(near, far Border) Border {
	if near.Style == BorderHidden || far.Style == BorderHidden {
		return Border{Style: BorderHidden}
	}
	nw, fw := near.Thickness(), far.Thickness()
	switch {
	case nw != fw:
		if nw > fw {
			return near
		}
		return far
	case borderRank(near.Style) < borderRank(far.Style):
		return far
	}
	return near
}

// collapse resolves the borders of a table whose cells share them, and gives
// every box they replace half the room its own would have taken.
func collapse(t *box) {
	rows, groups := tableRows(t)
	cells, ncols := buildGrid(rows)
	if ncols == 0 || len(cells) == 0 {
		return
	}
	g := collapseEdges(t, rows, groups, cells, ncols)
	t.collapsed = g
	for _, c := range cells {
		in := g.inset(c, len(rows))
		c.box.inset, c.box.skipBorders = &in, true
	}
	for i, r := range rows {
		r.skipBorders = true
		if groups[i] != nil {
			groups[i].skipBorders = true
		}
	}
	in := g.outerInset()
	t.inset, t.skipBorders = &in, true
}

// outerInset is half of the widest border along each edge of the table, which
// is the room the table box gives the half of it that falls inside.
func (g *grid) outerInset() [4]float32 {
	var out [4]float32
	for r := range g.nrows {
		out[3] = max(out[3], g.v(0, r).Thickness()/2)
		out[1] = max(out[1], g.v(g.ncols, r).Thickness()/2)
	}
	for col := range g.ncols {
		out[0] = max(out[0], g.h(0, col).Thickness()/2)
		out[2] = max(out[2], g.h(g.nrows, col).Thickness()/2)
	}
	return out
}

// collapseEdges resolves every segment of a table's grid from the borders of
// the cells, the rows, the row groups and the table itself, nearest first.
func collapseEdges(t *box, rows, groups []*box, cells []*cell, ncols int) *grid {
	g := newGrid(ncols, len(rows))
	outer := func(b Border, r int, pick func(*Style) Border, table bool) Border {
		b = stronger(b, pick(rows[r].style))
		if groups[r] != nil {
			b = stronger(b, pick(groups[r].style))
		}
		if table {
			b = stronger(b, pick(t.style))
		}
		return b
	}
	left := func(s *Style) Border { return s.BorderLeft }
	right := func(s *Style) Border { return s.BorderRight }
	top := func(s *Style) Border { return s.BorderTop }
	bottom := func(s *Style) Border { return s.BorderBottom }

	for _, c := range cells {
		s := c.box.style
		last := min(c.row+c.rows, len(rows))
		end := min(c.col+c.cols, ncols)
		for r := c.row; r < last; r++ {
			b := s.BorderLeft
			if c.col == 0 {
				b = outer(b, r, left, true)
			}
			*g.v(c.col, r) = stronger(*g.v(c.col, r), b)

			b = s.BorderRight
			if end == ncols {
				b = outer(b, r, right, true)
			}
			*g.v(end, r) = stronger(*g.v(end, r), b)
		}
		for col := c.col; col < end; col++ {
			b := outer(s.BorderTop, c.row, top, c.row == 0)
			*g.h(c.row, col) = stronger(*g.h(c.row, col), b)

			b = outer(s.BorderBottom, last-1, bottom, last == len(rows))
			*g.h(last, col) = stronger(*g.h(last, col), b)
		}
	}
	return g
}

// inset is the room a cell of a collapsed table gives its four borders, which
// is half of the widest segment along each of its edges.
func (g *grid) inset(c *cell, nrows int) [4]float32 {
	var out [4]float32
	last := min(c.row+c.rows, nrows)
	right := min(c.col+c.cols, g.ncols)
	for r := c.row; r < last; r++ {
		out[3] = max(out[3], g.v(c.col, r).Thickness()/2)
		out[1] = max(out[1], g.v(right, r).Thickness()/2)
	}
	for col := c.col; col < right; col++ {
		out[0] = max(out[0], g.h(c.row, col).Thickness()/2)
		out[2] = max(out[2], g.h(last, col).Thickness()/2)
	}
	return out
}

// lines turns the resolved grid into the segments the table paints, each
// centred on the line between two cells.
func (g *grid) lines(x float32, edge, rowTop []float32) []edgeLine {
	var out []edgeLine
	for col := 0; col <= g.ncols; col++ {
		for r := range g.nrows {
			b := *g.v(col, r)
			w := b.Thickness()
			if w <= 0 {
				continue
			}
			y0, y1 := rowTop[r], rowTop[r+1]
			if y1 <= y0 {
				continue
			}
			out = append(out, edgeLine{e: b, x: x + edge[col] - w/2, y: y0, w: w, h: y1 - y0})
		}
	}
	for row := 0; row <= g.nrows; row++ {
		for col := range g.ncols {
			b := *g.h(row, col)
			w := b.Thickness()
			if w <= 0 {
				continue
			}
			x0, x1 := edge[col], edge[col+1]
			if x1 <= x0 {
				continue
			}
			out = append(out, edgeLine{e: b, x: x + x0, y: rowTop[row] - w/2,
				w: x1 - x0, h: w, horizontal: true})
		}
	}
	return out
}
