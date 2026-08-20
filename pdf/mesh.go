package pdf

import "github.com/gen2brain/pdf/raster"

// meshSplitMin and meshSplitMax bound how finely a patch is tessellated, and
// meshSplitPixels is how many device pixels one cell aims for.
const (
	meshSplitMin    = 3
	meshSplitMax    = 32
	meshSplitPixels = 3
)

// meshFull and meshEdge order the control points of a patch as they are
// written, into the row major grid the surface is evaluated over. A patch
// that shares an edge with the one before it leaves out that edge's four
// points; meshInner is the tensor patch's own four.
var (
	meshFull  = [12]int{0, 4, 8, 12, 13, 14, 15, 11, 7, 3, 2, 1}
	meshEdge  = [8]int{13, 14, 15, 11, 7, 3, 2, 1}
	meshInner = [4]int{5, 9, 10, 6}
)

// paintMesh decodes a type 4 to 7 shading and fills its triangles into px,
// which m maps the shading's own space onto. It reports whether the mesh
// decoded far enough to have drawn anything.
func (d *DrawDevice) paintMesh(sh *Shade, m raster.Matrix, px *raster.Pixmap) bool {
	if sh.stream == nil {
		return false
	}
	data, err := sh.stream.Data()
	if err != nil {
		d.doc.errorf("mesh shading: %v", err)
		return false
	}
	f := d.doc.f
	dict := sh.stream.Dict
	r := &meshReader{
		data:   data,
		coord:  int(f.GetInt(dict["BitsPerCoordinate"], 0)),
		comp:   int(f.GetInt(dict["BitsPerComponent"], 0)),
		flag:   int(f.GetInt(dict["BitsPerFlag"], 0)),
		decode: f.GetFloats(dict["Decode"]),
		sh:     sh,
		m:      m,
		dst:    px,
		n:      px.N,
		col:    make([]float32, shadeComps(sh)),
	}
	r.ncomp = len(r.col)
	if len(sh.Function) > 0 {
		r.ncomp = 1
	}
	if !meshBits(r.coord) || !meshBits(r.comp) || len(r.decode) < 4+2*r.ncomp {
		return false
	}
	switch sh.Type {
	case 4:
		if r.flag != 2 && r.flag != 4 && r.flag != 8 {
			return false
		}
		r.triangles()
	case 5:
		r.lattice(int(f.GetInt(dict["VerticesPerRow"], 0)))
	case 6, 7:
		if r.flag != 2 && r.flag != 4 && r.flag != 8 {
			return false
		}
		r.patches(sh.Type == 7)
	}
	return r.drawn
}

func meshBits(n int) bool {
	switch n {
	case 1, 2, 4, 8, 12, 16, 24, 32:
		return true
	}
	return false
}

// meshReader reads the packed vertices of a mesh shading and turns them into
// triangles of the destination's own colors.
type meshReader struct {
	data  []byte
	pos   int
	buf   uint64
	nbits uint
	eof   bool

	coord, comp, flag int
	decode            []float64
	ncomp             int

	sh   *Shade
	m    raster.Matrix
	dst  *raster.Pixmap
	n    int
	col  []float32
	grid []raster.Vertex
	rows [2][]raster.Vertex

	drawn bool
}

func (r *meshReader) bits(n int) uint32 {
	for r.nbits < uint(n) {
		if r.pos >= len(r.data) {
			r.eof = true
			return 0
		}
		r.buf = r.buf<<8 | uint64(r.data[r.pos])
		r.pos++
		r.nbits += 8
	}
	r.nbits -= uint(n)
	v := uint32(r.buf >> r.nbits)
	r.buf &= 1<<r.nbits - 1
	return v
}

// align drops the rest of the byte, which a type 4 vertex is padded to.
func (r *meshReader) align() {
	r.buf, r.nbits = 0, 0
}

func (r *meshReader) more() bool {
	return !r.eof && (r.pos < len(r.data) || r.nbits >= 8)
}

// value maps a packed number through the Decode array, whose first two pairs
// bound the coordinates and whose rest bound the color.
func (r *meshReader) value(v uint32, bits, i int) float64 {
	lo, hi := 0.0, 1.0
	if 2*i+1 < len(r.decode) {
		lo, hi = r.decode[2*i], r.decode[2*i+1]
	}
	return lo + float64(v)/float64(uint64(1)<<uint(bits)-1)*(hi-lo)
}

func (r *meshReader) point() raster.Point {
	x := r.value(r.bits(r.coord), r.coord, 0)
	y := r.value(r.bits(r.coord), r.coord, 1)
	return r.m.Apply(raster.Point{X: float32(x), Y: float32(y)})
}

func (r *meshReader) color() [4]uint8 {
	var t float64
	for i := 0; i < r.ncomp; i++ {
		v := r.value(r.bits(r.comp), r.comp, 2+i)
		if i == 0 {
			t = v
		}
		if i < len(r.col) {
			r.col[i] = float32(v)
		}
	}
	if len(r.sh.Function) > 0 {
		shadeColor(r.sh, r.col, t)
	}
	var out [4]uint8
	convertColor(r.sh.CS, r.col, out[:r.n])
	return out
}

func (r *meshReader) vertex() (raster.Vertex, bool) {
	p := r.point()
	c := r.color()
	if r.eof {
		return raster.Vertex{}, false
	}
	return raster.Vertex{X: p.X, Y: p.Y, Color: c}, true
}

func (r *meshReader) emit(a, b, c raster.Vertex) {
	r.dst.FillTriangle(a, b, c)
	r.drawn = true
}

// triangles reads a type 4 free form mesh, where a flag before each vertex
// says which two of the last three it joins.
func (r *meshReader) triangles() {
	var a, b, c raster.Vertex
	pending := 0
	for r.more() {
		f := r.bits(r.flag)
		v, ok := r.vertex()
		if !ok {
			return
		}
		r.align()
		switch {
		case pending == 2:
			b, pending = v, 1
		case pending == 1:
			c, pending = v, 0
			r.emit(a, b, c)
		case f == 0:
			a, pending = v, 2
		case f == 1:
			a, b, c = b, c, v
			r.emit(a, b, c)
		case f == 2:
			b, c = c, v
			r.emit(a, b, c)
		default:
			return
		}
	}
}

// lattice reads a type 5 mesh, rows of the same length with no flags.
func (r *meshReader) lattice(per int) {
	if per < 2 || per > 1<<16 {
		return
	}
	prev, row := r.rows[0][:0], r.rows[1][:0]
	for r.more() {
		row = row[:0]
		for i := 0; i < per; i++ {
			v, ok := r.vertex()
			if !ok {
				return
			}
			row = append(row, v)
		}
		if len(prev) == per {
			for i := 0; i+1 < per; i++ {
				r.emit(prev[i], prev[i+1], row[i])
				r.emit(prev[i+1], row[i+1], row[i])
			}
		}
		prev, row = row, prev
	}
	r.rows[0], r.rows[1] = prev, row
}

// patches reads a type 6 or 7 mesh, each patch either standing alone or
// sharing an edge and two colors with the one before it.
func (r *meshReader) patches(tensor bool) {
	var p [16]raster.Point
	var c [4][4]uint8
	first := true
	for r.more() {
		f := r.bits(r.flag)
		if f > 3 || (first && f != 0) {
			return
		}
		var q [16]raster.Point
		idx := meshEdge[:]
		if f == 0 {
			idx = meshFull[:]
		}
		for _, j := range idx {
			q[j] = r.point()
		}
		if tensor {
			for _, j := range meshInner {
				q[j] = r.point()
			}
		}
		var rc [4][4]uint8
		nc := 2
		if f == 0 {
			nc = 4
		}
		for i := 0; i < nc; i++ {
			rc[i] = r.color()
		}
		if r.eof {
			return
		}
		p, c = meshJoin(p, c, q, rc, f)
		if !tensor {
			meshInterior(&p)
		}
		r.tessellate(&p, &c)
		first = false
	}
}

// meshJoin puts the points and colors just read into the grid, keeping the
// edge and the two colors the flag says this patch shares with the one
// before it.
func meshJoin(p [16]raster.Point, c [4][4]uint8, q [16]raster.Point, rc [4][4]uint8, f uint32) ([16]raster.Point, [4][4]uint8) {
	var nc [4][4]uint8
	switch f {
	case 0:
		nc[0], nc[1], nc[2], nc[3] = rc[0], rc[3], rc[1], rc[2]
		return q, nc
	case 1:
		q[0], q[4], q[8], q[12] = p[12], p[13], p[14], p[15]
		nc[0], nc[2] = c[2], c[3]
	case 2:
		q[0], q[4], q[8], q[12] = p[15], p[11], p[7], p[3]
		nc[0], nc[2] = c[3], c[1]
	case 3:
		q[0], q[4], q[8], q[12] = p[3], p[2], p[1], p[0]
		nc[0], nc[2] = c[1], c[0]
	}
	nc[1], nc[3] = rc[1], rc[0]
	return q, nc
}

// meshInterior computes the four inner control points a Coons patch leaves
// implied, which is what makes it a tensor patch.
func meshInterior(p *[16]raster.Point) {
	p[5] = coons(p[0], p[15], p[4], p[1], p[12], p[3], p[13], p[7])
	p[6] = coons(p[3], p[12], p[2], p[7], p[0], p[15], p[4], p[14])
	p[9] = coons(p[12], p[3], p[8], p[13], p[0], p[15], p[11], p[1])
	p[10] = coons(p[15], p[0], p[11], p[14], p[12], p[3], p[2], p[8])
}

func coons(a, b, c, d, e, f, g, h raster.Point) raster.Point {
	return raster.Point{
		X: (-4*a.X - b.X + 6*(c.X+d.X) - 2*(e.X+f.X) + 3*(g.X+h.X)) / 9,
		Y: (-4*a.Y - b.Y + 6*(c.Y+d.Y) - 2*(e.Y+f.Y) + 3*(g.Y+h.Y)) / 9,
	}
}

// tessellate walks the patch surface on a grid fine enough that its cells are
// a few pixels across, and emits two triangles for each cell.
func (r *meshReader) tessellate(p *[16]raster.Point, c *[4][4]uint8) {
	nrow := meshSplit(chord(p[0], p[12]), chord(p[3], p[15]))
	ncol := meshSplit(chord(p[0], p[3]), chord(p[12], p[15]))

	if n := (nrow + 1) * (ncol + 1); cap(r.grid) < n {
		r.grid = make([]raster.Vertex, n)
	} else {
		r.grid = r.grid[:n]
	}
	for i := 0; i <= nrow; i++ {
		br := bernstein(float32(i) / float32(nrow))
		for j := 0; j <= ncol; j++ {
			bc := bernstein(float32(j) / float32(ncol))
			v := &r.grid[i*(ncol+1)+j]
			v.X, v.Y = 0, 0
			for a := 0; a < 4; a++ {
				for b := 0; b < 4; b++ {
					w := float32(br[a] * bc[b])
					v.X += float32(p[a*4+b].X * w)
					v.Y += float32(p[a*4+b].Y * w)
				}
			}
			v.Color = meshCorner(c, float32(i)/float32(nrow), float32(j)/float32(ncol))
		}
	}
	for i := 0; i < nrow; i++ {
		for j := 0; j < ncol; j++ {
			a := r.grid[i*(ncol+1)+j]
			b := r.grid[i*(ncol+1)+j+1]
			cc := r.grid[(i+1)*(ncol+1)+j]
			dd := r.grid[(i+1)*(ncol+1)+j+1]
			r.emit(a, b, cc)
			r.emit(b, dd, cc)
		}
	}
}

// meshCorner interpolates the patch's four corner colors bilinearly.
func meshCorner(c *[4][4]uint8, u, v float32) [4]uint8 {
	var out [4]uint8
	for i := range out {
		top := float32(float32(c[0][i])*(1-v)) + float32(float32(c[1][i])*v)
		bot := float32(float32(c[2][i])*(1-v)) + float32(float32(c[3][i])*v)
		out[i] = uint8(float32(top*(1-u)) + float32(bot*u) + 0.5)
	}
	return out
}

func bernstein(t float32) [4]float32 {
	s := 1 - t
	return [4]float32{
		float32(s * s * s),
		float32(3 * t * float32(s*s)),
		float32(3 * float32(t*t) * s),
		float32(t * t * t),
	}
}

func chord(a, b raster.Point) float32 {
	dx, dy := absf32(a.X-b.X), absf32(a.Y-b.Y)
	if dx > dy {
		return dx + dy/2
	}
	return dy + dx/2
}

func meshSplit(a, b float32) int {
	n := a
	if b > n {
		n = b
	}
	return clampInt(int(n/meshSplitPixels)+1, meshSplitMin, meshSplitMax)
}
