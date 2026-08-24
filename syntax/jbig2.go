package syntax

// JBIG2 bilevel image decoding, ITU-T T.88, in the embedded form ISO 32000-1
// 7.4.7 defines: no file header, and the globals stream carries the segments
// the page stream refers to. Ported from pdf.js.

import "sync"

const (
	// maxJBPixels bounds what one JBIG2 bitmap may allocate.
	maxJBPixels = 1 << 28
	// maxJBSymbols bounds how many symbols a dictionary may carry, so that
	// a file naming itself in a cycle cannot grow one without end.
	maxJBSymbols = 1 << 20
	// maxJBBudget bounds the pixels one stream may decode altogether. It
	// starts small and the page information segment raises it to a multiple
	// of the page area, so that a handful of segment headers cannot ask for
	// unbounded work while a real page still has room for its regions, its
	// intermediate buffers and its symbols.
	maxJBBudget  = 2 * maxJBPixels
	startJBudget = 1 << 22
)

// A jbBitmap is one bit a pixel, like the page it is composited into. A region
// the size of a page is the largest thing a stream asks for, and a byte a pixel
// made it eight times what the page itself costs.
//
// xoff is where column zero sits inside the first byte of a row, which lets
// sub share the samples of a bitmap it does not start at a byte boundary of.
type jbBitmap struct {
	w, h, stride int
	xoff         int
	pix          []uint8
}

func newJBBitmap(w, h int) (*jbBitmap, error) {
	if w <= 0 || h <= 0 || w > maxJBPixels || h > maxJBPixels || int64(w)*int64(h) > maxJBPixels {
		return nil, errInvalidf("JBIG2 bitmap is %dx%d", w, h)
	}
	stride := (w + 7) / 8
	return &jbBitmap{w: w, h: h, stride: stride, pix: make([]uint8, stride*h)}, nil
}

// row returns the bytes a row occupies, which is not the same as its pixels:
// only a caller that means to work on the bits itself wants this.
func (b *jbBitmap) row(y int) []uint8 { return b.pix[y*b.stride:][:b.stride] }

func (b *jbBitmap) at(x, y int) uint8 {
	if x < 0 || x >= b.w || y < 0 || y >= b.h {
		return 0
	}
	i := b.xoff + x
	return b.pix[y*b.stride+i>>3] >> uint(7-i&7) & 1
}

// bitAt reads a pixel the caller has already bounded, which the decoders can
// do more cheaply than at because they know where the window sits.
func (b *jbBitmap) bitAt(x, y int) uint32 {
	i := b.xoff + x
	return uint32(b.pix[y*b.stride+i>>3]>>uint(7-i&7)) & 1
}

func (b *jbBitmap) set(x, y int, v uint8) {
	if x < 0 || x >= b.w || y < 0 || y >= b.h {
		return
	}
	i := b.xoff + x
	mask := uint8(0x80) >> uint(i&7)
	if v != 0 {
		b.pix[y*b.stride+i>>3] |= mask
	} else {
		b.pix[y*b.stride+i>>3] &^= mask
	}
}

func (b *jbBitmap) fill(v uint8) {
	fill := uint8(0)
	if v != 0 {
		fill = 0xff
	}
	if b.xoff == 0 && b.w == b.stride*8 {
		for i := range b.pix {
			b.pix[i] = fill
		}
		return
	}
	for y := 0; y < b.h; y++ {
		for x := 0; x < b.w; x++ {
			b.set(x, y, v)
		}
	}
}

// sub returns the columns [x0, x1) of b, sharing its samples.
func (b *jbBitmap) sub(x0, x1 int) *jbBitmap {
	return &jbBitmap{w: x1 - x0, h: b.h, stride: b.stride, xoff: b.xoff + x0, pix: b.pix}
}

const (
	jbIADH = iota
	jbIADW
	jbIAEX
	jbIAAI
	jbIADT
	jbIAFS
	jbIADS
	jbIAIT
	jbIARI
	jbIARDW
	jbIARDH
	jbIARDX
	jbIARDY
	jbNumInt
)

// jbCoder is one segment's arithmetic decoder and the context arrays that go
// with it; T.88 resets them at every segment this reads.
type jbCoder struct {
	mq     *MQ
	budget *int64
	ints   [jbNumInt][512]uint8
	iaid   []uint8
	gb     []uint8
	gr     []uint8
	// refKey and refTmpl cache the label tables of the refinement template,
	// which every symbol of a dictionary refines through the same one.
	refKey  refKey
	refTmpl *refTemplate
}

type refKey struct {
	template int
	a0, a1   jbPoint
}

func newJBCoder(data []byte, start, end int, budget *int64) *jbCoder {
	return &jbCoder{mq: NewMQ(data, start, end), budget: budget}
}

// spend takes a bitmap's area out of the stream's budget.
func (c *jbCoder) spend(w, h int) error {
	if c.budget == nil {
		return nil
	}
	*c.budget -= int64(w) * int64(h)
	if *c.budget < 0 {
		return errInvalidf("JBIG2 stream decodes more pixels than its page")
	}
	return nil
}

// restart begins a new arithmetic decoder over the block a Huffman coded
// segment byte aligns before each refinement, keeping the statistics the
// segment has accumulated so far.
func (c *jbCoder) restart(data []byte, start, end int) {
	c.mq = NewMQ(data, start, end)
}

func (c *jbCoder) generic() []uint8 {
	if c.gb == nil {
		c.gb = make([]uint8, 1<<16)
	}
	return c.gb
}

func (c *jbCoder) refine() []uint8 {
	if c.gr == nil {
		c.gr = make([]uint8, 1<<16)
	}
	return c.gr
}

// A.2 Decoding a value.
func (c *jbCoder) integer(proc int) (int32, bool) {
	ctx := c.ints[proc][:]
	prev := uint32(1)
	read := func(n int) uint32 {
		v := uint32(0)
		for i := 0; i < n; i++ {
			bit := uint32(c.mq.ReadBit(ctx, prev))
			if prev < 256 {
				prev = prev<<1 | bit
			} else {
				prev = (prev<<1|bit)&511 | 256
			}
			v = v<<1 | bit
		}
		return v
	}

	sign := read(1)
	var value uint64
	switch {
	case read(1) == 0:
		value = uint64(read(2))
	case read(1) == 0:
		value = uint64(read(4)) + 4
	case read(1) == 0:
		value = uint64(read(6)) + 20
	case read(1) == 0:
		value = uint64(read(8)) + 84
	case read(1) == 0:
		value = uint64(read(12)) + 340
	default:
		value = uint64(read(32)) + 4436
	}
	if sign == 0 {
		if value > 0x7fffffff {
			return 0, false
		}
		return int32(value), true
	}
	if value == 0 || value > 0x80000000 {
		return 0, false
	}
	return int32(-int64(value)), true
}

// A.3 The IAID decoding procedure.
func (c *jbCoder) id(codeLength int) uint32 {
	if n := 1 << uint(codeLength+1); len(c.iaid) < n {
		c.iaid = make([]uint8, n)
	}
	prev := uint32(1)
	for i := 0; i < codeLength; i++ {
		prev = prev<<1 | uint32(c.mq.ReadBit(c.iaid, prev))
	}
	if codeLength < 31 {
		return prev & (1<<uint(codeLength) - 1)
	}
	return prev & 0x7fffffff
}

type jbPoint struct{ x, y int8 }

var jbCodingTemplates = [4][]jbPoint{
	{{-1, -2}, {0, -2}, {1, -2}, {-2, -1}, {-1, -1}, {0, -1}, {1, -1}, {2, -1}, {-4, 0}, {-3, 0}, {-2, 0}, {-1, 0}},
	{{-1, -2}, {0, -2}, {1, -2}, {2, -2}, {-2, -1}, {-1, -1}, {0, -1}, {1, -1}, {2, -1}, {-3, 0}, {-2, 0}, {-1, 0}},
	{{-1, -2}, {0, -2}, {1, -2}, {-2, -1}, {-1, -1}, {0, -1}, {1, -1}, {-2, 0}, {-1, 0}},
	{{-3, -1}, {-2, -1}, {-1, -1}, {0, -1}, {1, -1}, {-4, 0}, {-3, 0}, {-2, 0}, {-1, 0}},
}

var jbRefinementTemplates = [2]struct{ coding, reference []jbPoint }{
	{
		coding:    []jbPoint{{0, -1}, {1, -1}, {-1, 0}},
		reference: []jbPoint{{0, -1}, {1, -1}, {-1, 0}, {0, 0}, {1, 0}, {-1, 1}, {0, 1}, {1, 1}},
	},
	{
		coding:    []jbPoint{{-1, -1}, {0, -1}, {1, -1}, {-1, 0}},
		reference: []jbPoint{{0, -1}, {-1, 0}, {0, 0}, {1, 0}, {0, 1}, {1, 1}},
	},
}

var jbReusedContexts = [4]uint32{0x9b25, 0x0795, 0x00e5, 0x0195}

var jbRefinementReusedContexts = [2]uint32{0x0020, 0x0008}

func jbU32(data []byte, i int) uint32 {
	if i < 0 || i+4 > len(data) {
		return 0
	}
	return uint32(data[i])<<24 | uint32(data[i+1])<<16 | uint32(data[i+2])<<8 | uint32(data[i+3])
}

func jbU16(data []byte, i int) uint16 {
	if i < 0 || i+2 > len(data) {
		return 0
	}
	return uint16(data[i])<<8 | uint16(data[i+1])
}

func jbByte(data []byte, i int) uint8 {
	if i < 0 || i >= len(data) {
		return 0
	}
	return data[i]
}

func jbI8(data []byte, i int) int8 {
	if i < 0 || i >= len(data) {
		return 0
	}
	return int8(data[i])
}

func jbLog2(x int) int {
	n := 0
	for 1<<uint(n) < x {
		n++
	}
	return n
}

// jbNominalTemplate0 reports whether at is the nominal adaptive template of a
// template 0 generic region, which has a loop of its own.
func jbNominalTemplate0(at []jbPoint) bool {
	return len(at) == 4 &&
		at[0] == jbPoint{3, -1} && at[1] == jbPoint{-3, -1} &&
		at[2] == jbPoint{2, -2} && at[3] == jbPoint{-2, -2}
}

func (c *jbCoder) bitmapTemplate0(w, h int) (*jbBitmap, error) {
	bm, err := newJBBitmap(w, h)
	if err != nil {
		return nil, err
	}
	const keep = 0x7bf7
	ctx := c.generic()
	// The two rows above are read through their own bytes rather than through
	// at: the label carries the rest of the window along by shifting, so the
	// only pixels this asks for are the two arriving at the right of it.
	get := func(r []uint8, x int) uint32 {
		if r == nil || x >= w {
			return 0
		}
		return uint32(r[x>>3]>>uint(7-x&7)) & 1
	}
	for i := 0; i < h; i++ {
		row := bm.row(i)
		var row1, row2 []uint8
		if i >= 1 {
			row1 = bm.row(i - 1)
		}
		if i >= 2 {
			row2 = bm.row(i - 2)
		}
		label := get(row2, 0)<<13 | get(row2, 1)<<12 | get(row2, 2)<<11 |
			get(row1, 0)<<7 | get(row1, 1)<<6 | get(row1, 2)<<5 | get(row1, 3)<<4
		for j := 0; j < w; j++ {
			pixel := uint32(c.mq.ReadBit(ctx, label))
			if pixel != 0 {
				row[j>>3] |= 0x80 >> uint(j&7)
			}
			label = (label&keep)<<1 | get(row2, j+3)<<11 | get(row1, j+4)<<4 | pixel
		}
	}
	return bm, nil
}

// 6.2 Generic region decoding procedure. The template is sorted into raster
// order, which is a permutation of the context index and so leaves the decoded
// bits alone while letting the inner loop shift most of a label along.
func (c *jbCoder) genericBitmap(w, h, template int, prediction bool, skip *jbBitmap, at []jbPoint) (*jbBitmap, error) {
	if err := c.spend(w, h); err != nil {
		return nil, err
	}
	if template == 0 && skip == nil && !prediction && jbNominalTemplate0(at) {
		return c.bitmapTemplate0(w, h)
	}
	bm, err := newJBBitmap(w, h)
	if err != nil {
		return nil, err
	}

	var buf [16]jbPoint
	if len(jbCodingTemplates[template])+len(at) > len(buf) {
		return nil, errInvalidf("JBIG2 template has %d pixels", len(jbCodingTemplates[template])+len(at))
	}
	tmpl := buf[:0]
	tmpl = append(tmpl, jbCodingTemplates[template]...)
	tmpl = append(tmpl, at...)
	for i := 1; i < len(tmpl); i++ {
		p := tmpl[i]
		j := i
		for ; j > 0 && (tmpl[j-1].y > p.y || tmpl[j-1].y == p.y && tmpl[j-1].x > p.x); j-- {
			tmpl[j] = tmpl[j-1]
		}
		tmpl[j] = p
	}

	n := len(tmpl)
	var reuse uint32
	minX, maxX, minY, maxY := 0, 0, 0, 0
	var changeX, changeY [16]int
	var changeBit [16]uint32
	nc := 0
	for k := 0; k < n; k++ {
		minX = min(minX, int(tmpl[k].x))
		maxX = max(maxX, int(tmpl[k].x))
		minY = min(minY, int(tmpl[k].y))
		maxY = max(maxY, int(tmpl[k].y))
		if k < n-1 && tmpl[k].y == tmpl[k+1].y && tmpl[k].x == tmpl[k+1].x-1 {
			reuse |= 1 << uint(n-1-k)
			continue
		}
		changeX[nc] = int(tmpl[k].x)
		changeY[nc] = int(tmpl[k].y)
		changeBit[nc] = 1 << uint(n-1-k)
		nc++
	}

	insideLeft, insideRight := -minX, w-maxX
	insideTop, insideBottom := -minY, h-maxY
	pseudo := jbReusedContexts[template]
	ctx := c.generic()

	ltp := 0
	var label uint32
	for i := 0; i < h; i++ {
		if prediction {
			ltp ^= c.mq.ReadBit(ctx, pseudo)
			if ltp != 0 {
				if i > 0 {
					copy(bm.row(i), bm.row(i-1))
				}
				continue
			}
		}
		row := bm.row(i)
		for j := 0; j < w; j++ {
			if skip != nil && skip.at(j, i) != 0 {
				continue
			}
			if j >= insideLeft && j < insideRight && i >= insideTop && i < insideBottom {
				label = (label << 1) & reuse
				for k := 0; k < nc; k++ {
					if bm.bitAt(j+changeX[k], i+changeY[k]) != 0 {
						label |= changeBit[k]
					}
				}
			} else {
				label = 0
				shift := uint(n - 1)
				for k := 0; k < n; k++ {
					label |= uint32(bm.at(j+int(tmpl[k].x), i+int(tmpl[k].y))) << shift
					shift--
				}
			}
			if c.mq.ReadBit(ctx, label) != 0 {
				row[j>>3] |= 0x80 >> uint(j&7)
			}
		}
	}
	return bm, nil
}

// 6.3 Generic refinement region decoding procedure.
func (c *jbCoder) refinementBitmap(w, h, template int, ref *jbBitmap, dx, dy int, prediction bool, at []jbPoint) (*jbBitmap, error) {
	if err := c.spend(w, h); err != nil {
		return nil, err
	}
	var cbuf, rbuf [16]jbPoint
	coding := jbRefinementTemplates[template].coding
	reference := jbRefinementTemplates[template].reference
	if template == 0 {
		if len(at) < 2 {
			return nil, errInvalidf("JBIG2 refinement has no adaptive pixels")
		}
		coding = append(append(cbuf[:0], coding...), at[0])
		reference = append(append(rbuf[:0], reference...), at[1])
	}
	bm, err := newJBBitmap(w, h)
	if err != nil {
		return nil, err
	}
	pseudo := jbRefinementReusedContexts[template]
	ctx := c.refine()

	// Reading a neighbour through at costs a bounds check and a multiply, and
	// the loop runs thirteen times a pixel. Where every point of the template
	// falls inside the three by three around the pixel, which is every file
	// that does not move an adaptive pixel far away, the neighbourhood is six
	// rows of three bits that slide along with j.
	if t := c.refTemplate(template, coding, reference, at); t != nil {
		c.refineNear(bm, ref, dx, dy, t, ctx, pseudo, prediction)
		return bm, nil
	}

	ltp := 0
	for i := 0; i < h; i++ {
		if prediction {
			ltp ^= c.mq.ReadBit(ctx, pseudo)
		}
		for j := 0; j < w; j++ {
			if ltp != 0 {
				// 6.3.5.6, a pixel whose reference neighborhood is
				// uniform takes that value without a decision.
				if v, ok := jbTypical(ref, j-dx, i-dy); ok {
					bm.set(j, i, v)
					continue
				}
			}
			var label uint32
			for _, p := range coding {
				label = label<<1 | uint32(bm.at(j+int(p.x), i+int(p.y)))
			}
			for _, p := range reference {
				label = label<<1 | uint32(ref.at(j+int(p.x)-dx, i+int(p.y)-dy))
			}
			bm.set(j, i, uint8(c.mq.ReadBit(ctx, label)))
		}
	}
	return bm, nil
}

func jbTypical(ref *jbBitmap, x, y int) (uint8, bool) {
	sum := 0
	for dy := -1; dy <= 1; dy++ {
		for dx := -1; dx <= 1; dx++ {
			sum += int(ref.at(x+dx, y+dy))
		}
	}
	switch sum {
	case 0:
		return 0, true
	case 9:
		return 1, true
	}
	return 0, false
}

// refTemplate is a refinement template whose every point lies in the three by
// three square around the pixel. cells holds one entry per point, as the row
// it reads and the bit of that window, and the label is those bits top down.
type refTemplate struct {
	coding, reference *[512]uint32
	rbits             uint
}

// refTemplate returns the tables for one template, building them the first
// time it is asked and once more whenever an adaptive pixel moves. A symbol
// dictionary refines every one of its symbols through the same template, and
// the tables are a thousand times the work of the smallest symbol.
func (c *jbCoder) refTemplate(template int, coding, reference []jbPoint, at []jbPoint) *refTemplate {
	key := refKey{template: template}
	if template == 0 && len(at) >= 2 {
		key.a0, key.a1 = at[0], at[1]
	}
	if c.refTmpl != nil && c.refKey == key {
		return c.refTmpl
	}
	t := newRefTemplate(coding, reference)
	c.refKey, c.refTmpl = key, t
	return t
}

// newRefTemplate turns a template into the label each state of the two three
// by three neighborhoods maps to, nil for one that reaches outside them.
func newRefTemplate(coding, reference []jbPoint) *refTemplate {
	cod, ok := refLabels(coding)
	if !ok {
		return nil
	}
	ref, ok := refLabels(reference)
	if !ok {
		return nil
	}
	return &refTemplate{coding: cod, reference: ref, rbits: uint(len(reference))}
}

// refLabels is the label a template contributes for every one of the 512
// states three rows of three bits can be in.
func refLabels(pts []jbPoint) (*[512]uint32, bool) {
	var shift [16]uint
	for k, p := range pts {
		if p.x < -1 || p.x > 1 || p.y < -1 || p.y > 1 || k >= len(shift) {
			return nil, false
		}
		// Row y sits at (1-y)*3 in the packed window and column x at 1-x
		// inside it, with the leftmost column highest.
		shift[k] = uint((1-int(p.y))*3 + 1 - int(p.x))
	}
	t := new([512]uint32)
	for w := range t {
		var v uint32
		for k := range pts {
			v = v<<1 | uint32(w)>>shift[k]&1
		}
		t[w] = v
	}
	return t, true
}

// refRows is the three rows of a bitmap a refinement template reaches, and
// the sliding window of each.
type refRows struct {
	rows [3][]uint8
	win  [3]uint32
	xoff int
	w    int
	// right is the column the low bit of each window holds.
	right int
}

func (r *refRows) at(b *jbBitmap, y, x int) {
	for k := 0; k < 3; k++ {
		r.rows[k] = nil
		if v := y + k - 1; v >= 0 && v < b.h {
			r.rows[k] = b.pix[v*b.stride:]
		}
	}
	r.xoff, r.w, r.right = b.xoff, b.w, x+1
	for k := 0; k < 3; k++ {
		r.win[k] = r.bitOf(k, x-1)<<2 | r.bitOf(k, x)<<1 | r.bitOf(k, x+1)
	}
}

func (r *refRows) bitOf(k, x int) uint32 {
	if r.rows[k] == nil || x < 0 || x >= r.w {
		return 0
	}
	i := r.xoff + x
	return uint32(r.rows[k][i>>3]>>uint(7-i&7)) & 1
}

func (r *refRows) step() {
	r.right++
	var b0, b1, b2 uint32
	// The column is the same for all three rows, so it is bounded once.
	if x := r.right; x >= 0 && x < r.w {
		i := r.xoff + x
		by, sh := i>>3, uint(7-i&7)
		if r.rows[0] != nil {
			b0 = uint32(r.rows[0][by]>>sh) & 1
		}
		if r.rows[1] != nil {
			b1 = uint32(r.rows[1][by]>>sh) & 1
		}
		if r.rows[2] != nil {
			b2 = uint32(r.rows[2][by]>>sh) & 1
		}
	}
	r.win[0] = r.win[0]<<1&7 | b0
	r.win[1] = r.win[1]<<1&7 | b1
	r.win[2] = r.win[2]<<1&7 | b2
}

// packed is the whole neighborhood as one number: the top row highest, and
// the leftmost column of each row highest inside it.
func (r *refRows) packed() uint32 { return r.win[0]<<6 | r.win[1]<<3 | r.win[2] }

// uniform reports the value 6.3.5.6 gives a pixel whose whole reference
// neighborhood is one color.
func (r *refRows) uniform() (uint8, bool) {
	switch or := r.win[0] | r.win[1] | r.win[2]; {
	case or == 0:
		return 0, true
	case or == 7 && r.win[0]&r.win[1]&r.win[2] == 7:
		return 1, true
	}
	return 0, false
}

// refineNear is 6.3 for a template that stays inside the three by three
// square, which is what every file of the corpus writes.
func (c *jbCoder) refineNear(bm, ref *jbBitmap, dx, dy int, t *refTemplate,
	ctx []uint8, pseudo uint32, prediction bool) {
	var cr, rr refRows
	ltp := 0
	for i := 0; i < bm.h; i++ {
		if prediction {
			ltp ^= c.mq.ReadBit(ctx, pseudo)
		}
		cr.at(bm, i, 0)
		rr.at(ref, i-dy, -dx)
		for j := 0; j < bm.w; j++ {
			v := uint8(0)
			if ltp != 0 {
				if u, ok := rr.uniform(); ok {
					v = u
					goto place
				}
			}
			v = uint8(c.mq.ReadBit(ctx, t.coding[cr.packed()]<<t.rbits|t.reference[rr.packed()]))
		place:
			bm.set(j, i, v)
			// The window has already read the pixel this decided, so it is
			// corrected before it slides over to become the one at x-1.
			cr.win[1] = cr.win[1]&^2 | uint32(v)<<1
			cr.step()
			rr.step()
		}
	}
}

// jbReader is the bit reader the Huffman coded parts of a segment use; the
// arithmetic parts read the same bytes through mq instead.
type jbReader struct {
	data     []byte
	pos, end int
	shift    int
	cur      uint8
	eof      bool
}

func newJBReader(data []byte, start, end int) *jbReader {
	start = min(max(start, 0), len(data))
	end = min(max(end, start), len(data))
	return &jbReader{data: data, pos: start, end: end, shift: -1}
}

// jbAdvance is pos moved on by n bytes, stopping at end. The sum is taken in
// 64 bits: a segment may name a size of two thousand million, which overflows
// an int where one is 32 bits wide and lands the reader before its own data.
func jbAdvance(pos, n, end int) int {
	if n <= 0 {
		return pos
	}
	if int64(pos)+int64(n) >= int64(end) {
		return end
	}
	return pos + n
}

func (r *jbReader) bit() uint32 {
	if r.shift < 0 {
		if r.pos < 0 || r.pos >= r.end {
			r.eof = true
			return 0
		}
		r.cur = r.data[r.pos]
		r.pos++
		r.shift = 7
	}
	b := uint32(r.cur>>uint(r.shift)) & 1
	r.shift--
	return b
}

func (r *jbReader) bits(n int) uint32 {
	v := uint32(0)
	for i := 0; i < n; i++ {
		v = v<<1 | r.bit()
	}
	return v
}

func (r *jbReader) align() { r.shift = -1 }

func (r *jbReader) next() int {
	if r.pos >= r.end {
		return -1
	}
	v := int(r.data[r.pos])
	r.pos++
	return v
}

// jbHuffLine is one line of a code table, Annex B.1: a prefix code that stands
// for a range, and the range width in bits that follows the prefix.
type jbHuffLine struct {
	rangeLow   int32
	prefixLen  uint8
	rangeLen   uint8
	prefixCode uint32
	lower      bool
	oob        bool
}

type jbHuffNode struct {
	children [2]*jbHuffNode
	leaf     bool
	line     jbHuffLine
}

type jbHuffTable struct{ root *jbHuffNode }

func newJBHuffTable(lines []jbHuffLine, coded bool) *jbHuffTable {
	if !coded {
		jbAssignPrefixCodes(lines)
	}
	t := &jbHuffTable{root: &jbHuffNode{}}
	for _, line := range lines {
		if line.prefixLen > 0 {
			t.root.insert(line, int(line.prefixLen)-1)
		}
	}
	return t
}

func (n *jbHuffNode) insert(line jbHuffLine, shift int) {
	bit := (line.prefixCode >> uint(shift)) & 1
	if shift <= 0 {
		n.children[bit] = &jbHuffNode{leaf: true, line: line}
		return
	}
	if n.children[bit] == nil {
		n.children[bit] = &jbHuffNode{}
	}
	n.children[bit].insert(line, shift-1)
}

// decode returns the next value, or ok false at an out of band code, at the end
// of the data, or when the segment named a table it does not carry.
func (t *jbHuffTable) decode(r *jbReader) (int32, bool) {
	if t == nil || t.root == nil {
		return 0, false
	}
	n := t.root
	for !n.leaf {
		n = n.children[r.bit()]
		if n == nil || r.eof {
			return 0, false
		}
	}
	if n.line.oob {
		return 0, false
	}
	off := int64(r.bits(int(n.line.rangeLen)))
	v := int64(n.line.rangeLow)
	if n.line.lower {
		v -= off
	} else {
		v += off
	}
	return int32(min(max(v, -0x80000000), 0x7fffffff)), true
}

// jbAssignPrefixCodes is Annex B.3.
func jbAssignPrefixCodes(lines []jbHuffLine) {
	maxLen := 0
	for _, line := range lines {
		maxLen = max(maxLen, int(line.prefixLen))
	}
	hist := make([]uint32, maxLen+1)
	for _, line := range lines {
		hist[line.prefixLen]++
	}
	hist[0] = 0
	firstCode := uint32(0)
	for length := 1; length <= maxLen; length++ {
		firstCode = (firstCode + hist[length-1]) << 1
		code := firstCode
		for i := range lines {
			if int(lines[i].prefixLen) == length {
				lines[i].prefixCode = code
				code++
			}
		}
	}
}

// B.2 Code table structure, a Tables segment.
func jbTablesSegment(data []byte, start, end int) (*jbHuffTable, error) {
	if start+9 > end {
		return nil, errInvalidf("JBIG2 table segment is %d bytes", end-start)
	}
	flags := data[start]
	low := int32(jbU32(data, start+1))
	high := int32(jbU32(data, start+5))
	r := newJBReader(data, start+9, end)

	prefixBits := int((flags>>1)&7) + 1
	rangeBits := int((flags>>4)&7) + 1

	var lines []jbHuffLine
	cur := int64(low)
	for {
		prefixLen := uint8(r.bits(prefixBits))
		rangeLen := uint8(r.bits(rangeBits))
		lines = append(lines, jbHuffLine{rangeLow: int32(cur), prefixLen: prefixLen, rangeLen: rangeLen})
		cur += int64(1) << uint(min(int(rangeLen), 62))
		if cur >= int64(high) || r.eof || len(lines) > 1<<12 {
			break
		}
	}
	lines = append(lines, jbHuffLine{rangeLow: low - 1, prefixLen: uint8(r.bits(prefixBits)), rangeLen: 32, lower: true})
	lines = append(lines, jbHuffLine{rangeLow: high, prefixLen: uint8(r.bits(prefixBits)), rangeLen: 32})
	if flags&1 != 0 {
		lines = append(lines, jbHuffLine{prefixLen: uint8(r.bits(prefixBits)), oob: true})
	}
	if r.eof {
		return nil, errInvalidf("JBIG2 table segment ends early")
	}
	return newJBHuffTable(lines, false), nil
}

// Annex B.5, the standard code tables.
var jbStandardTables = [16][]jbHuffLine{
	1: {
		{rangeLow: 0, prefixLen: 1, rangeLen: 4, prefixCode: 0x0},
		{rangeLow: 16, prefixLen: 2, rangeLen: 8, prefixCode: 0x2},
		{rangeLow: 272, prefixLen: 3, rangeLen: 16, prefixCode: 0x6},
		{rangeLow: 65808, prefixLen: 3, rangeLen: 32, prefixCode: 0x7},
	},
	2: {
		{rangeLow: 0, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 1, prefixLen: 2, rangeLen: 0, prefixCode: 0x2},
		{rangeLow: 2, prefixLen: 3, rangeLen: 0, prefixCode: 0x6},
		{rangeLow: 3, prefixLen: 4, rangeLen: 3, prefixCode: 0xe},
		{rangeLow: 11, prefixLen: 5, rangeLen: 6, prefixCode: 0x1e},
		{rangeLow: 75, prefixLen: 6, rangeLen: 32, prefixCode: 0x3e},
		{prefixLen: 6, prefixCode: 0x3f, oob: true},
	},
	3: {
		{rangeLow: -256, prefixLen: 8, rangeLen: 8, prefixCode: 0xfe},
		{rangeLow: 0, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 1, prefixLen: 2, rangeLen: 0, prefixCode: 0x2},
		{rangeLow: 2, prefixLen: 3, rangeLen: 0, prefixCode: 0x6},
		{rangeLow: 3, prefixLen: 4, rangeLen: 3, prefixCode: 0xe},
		{rangeLow: 11, prefixLen: 5, rangeLen: 6, prefixCode: 0x1e},
		{rangeLow: -257, prefixLen: 8, rangeLen: 32, prefixCode: 0xff, lower: true},
		{rangeLow: 75, prefixLen: 7, rangeLen: 32, prefixCode: 0x7e},
		{prefixLen: 6, prefixCode: 0x3e, oob: true},
	},
	4: {
		{rangeLow: 1, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 2, rangeLen: 0, prefixCode: 0x2},
		{rangeLow: 3, prefixLen: 3, rangeLen: 0, prefixCode: 0x6},
		{rangeLow: 4, prefixLen: 4, rangeLen: 3, prefixCode: 0xe},
		{rangeLow: 12, prefixLen: 5, rangeLen: 6, prefixCode: 0x1e},
		{rangeLow: 76, prefixLen: 5, rangeLen: 32, prefixCode: 0x1f},
	},
	5: {
		{rangeLow: -255, prefixLen: 7, rangeLen: 8, prefixCode: 0x7e},
		{rangeLow: 1, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 2, rangeLen: 0, prefixCode: 0x2},
		{rangeLow: 3, prefixLen: 3, rangeLen: 0, prefixCode: 0x6},
		{rangeLow: 4, prefixLen: 4, rangeLen: 3, prefixCode: 0xe},
		{rangeLow: 12, prefixLen: 5, rangeLen: 6, prefixCode: 0x1e},
		{rangeLow: -256, prefixLen: 7, rangeLen: 32, prefixCode: 0x7f, lower: true},
		{rangeLow: 76, prefixLen: 6, rangeLen: 32, prefixCode: 0x3e},
	},
	6: {
		{rangeLow: -2048, prefixLen: 5, rangeLen: 10, prefixCode: 0x1c},
		{rangeLow: -1024, prefixLen: 4, rangeLen: 9, prefixCode: 0x8},
		{rangeLow: -512, prefixLen: 4, rangeLen: 8, prefixCode: 0x9},
		{rangeLow: -256, prefixLen: 4, rangeLen: 7, prefixCode: 0xa},
		{rangeLow: -128, prefixLen: 5, rangeLen: 6, prefixCode: 0x1d},
		{rangeLow: -64, prefixLen: 5, rangeLen: 5, prefixCode: 0x1e},
		{rangeLow: -32, prefixLen: 4, rangeLen: 5, prefixCode: 0xb},
		{rangeLow: 0, prefixLen: 2, rangeLen: 7, prefixCode: 0x0},
		{rangeLow: 128, prefixLen: 3, rangeLen: 7, prefixCode: 0x2},
		{rangeLow: 256, prefixLen: 3, rangeLen: 8, prefixCode: 0x3},
		{rangeLow: 512, prefixLen: 4, rangeLen: 9, prefixCode: 0xc},
		{rangeLow: 1024, prefixLen: 4, rangeLen: 10, prefixCode: 0xd},
		{rangeLow: -2049, prefixLen: 6, rangeLen: 32, prefixCode: 0x3e, lower: true},
		{rangeLow: 2048, prefixLen: 6, rangeLen: 32, prefixCode: 0x3f},
	},
	7: {
		{rangeLow: -1024, prefixLen: 4, rangeLen: 9, prefixCode: 0x8},
		{rangeLow: -512, prefixLen: 3, rangeLen: 8, prefixCode: 0x0},
		{rangeLow: -256, prefixLen: 4, rangeLen: 7, prefixCode: 0x9},
		{rangeLow: -128, prefixLen: 5, rangeLen: 6, prefixCode: 0x1a},
		{rangeLow: -64, prefixLen: 5, rangeLen: 5, prefixCode: 0x1b},
		{rangeLow: -32, prefixLen: 4, rangeLen: 5, prefixCode: 0xa},
		{rangeLow: 0, prefixLen: 4, rangeLen: 5, prefixCode: 0xb},
		{rangeLow: 32, prefixLen: 5, rangeLen: 5, prefixCode: 0x1c},
		{rangeLow: 64, prefixLen: 5, rangeLen: 6, prefixCode: 0x1d},
		{rangeLow: 128, prefixLen: 4, rangeLen: 7, prefixCode: 0xc},
		{rangeLow: 256, prefixLen: 3, rangeLen: 8, prefixCode: 0x1},
		{rangeLow: 512, prefixLen: 3, rangeLen: 9, prefixCode: 0x2},
		{rangeLow: 1024, prefixLen: 3, rangeLen: 10, prefixCode: 0x3},
		{rangeLow: -1025, prefixLen: 5, rangeLen: 32, prefixCode: 0x1e, lower: true},
		{rangeLow: 2048, prefixLen: 5, rangeLen: 32, prefixCode: 0x1f},
	},
	8: {
		{rangeLow: -15, prefixLen: 8, rangeLen: 3, prefixCode: 0xfc},
		{rangeLow: -7, prefixLen: 9, rangeLen: 1, prefixCode: 0x1fc},
		{rangeLow: -5, prefixLen: 8, rangeLen: 1, prefixCode: 0xfd},
		{rangeLow: -3, prefixLen: 9, rangeLen: 0, prefixCode: 0x1fd},
		{rangeLow: -2, prefixLen: 7, rangeLen: 0, prefixCode: 0x7c},
		{rangeLow: -1, prefixLen: 4, rangeLen: 0, prefixCode: 0xa},
		{rangeLow: 0, prefixLen: 2, rangeLen: 1, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 5, rangeLen: 0, prefixCode: 0x1a},
		{rangeLow: 3, prefixLen: 6, rangeLen: 0, prefixCode: 0x3a},
		{rangeLow: 4, prefixLen: 3, rangeLen: 4, prefixCode: 0x4},
		{rangeLow: 20, prefixLen: 6, rangeLen: 1, prefixCode: 0x3b},
		{rangeLow: 22, prefixLen: 4, rangeLen: 4, prefixCode: 0xb},
		{rangeLow: 38, prefixLen: 4, rangeLen: 5, prefixCode: 0xc},
		{rangeLow: 70, prefixLen: 5, rangeLen: 6, prefixCode: 0x1b},
		{rangeLow: 134, prefixLen: 5, rangeLen: 7, prefixCode: 0x1c},
		{rangeLow: 262, prefixLen: 6, rangeLen: 7, prefixCode: 0x3c},
		{rangeLow: 390, prefixLen: 7, rangeLen: 8, prefixCode: 0x7d},
		{rangeLow: 646, prefixLen: 6, rangeLen: 10, prefixCode: 0x3d},
		{rangeLow: -16, prefixLen: 9, rangeLen: 32, prefixCode: 0x1fe, lower: true},
		{rangeLow: 1670, prefixLen: 9, rangeLen: 32, prefixCode: 0x1ff},
		{prefixLen: 2, prefixCode: 0x1, oob: true},
	},
	9: {
		{rangeLow: -31, prefixLen: 8, rangeLen: 4, prefixCode: 0xfc},
		{rangeLow: -15, prefixLen: 9, rangeLen: 2, prefixCode: 0x1fc},
		{rangeLow: -11, prefixLen: 8, rangeLen: 2, prefixCode: 0xfd},
		{rangeLow: -7, prefixLen: 9, rangeLen: 1, prefixCode: 0x1fd},
		{rangeLow: -5, prefixLen: 7, rangeLen: 1, prefixCode: 0x7c},
		{rangeLow: -3, prefixLen: 4, rangeLen: 1, prefixCode: 0xa},
		{rangeLow: -1, prefixLen: 3, rangeLen: 1, prefixCode: 0x2},
		{rangeLow: 1, prefixLen: 3, rangeLen: 1, prefixCode: 0x3},
		{rangeLow: 3, prefixLen: 5, rangeLen: 1, prefixCode: 0x1a},
		{rangeLow: 5, prefixLen: 6, rangeLen: 1, prefixCode: 0x3a},
		{rangeLow: 7, prefixLen: 3, rangeLen: 5, prefixCode: 0x4},
		{rangeLow: 39, prefixLen: 6, rangeLen: 2, prefixCode: 0x3b},
		{rangeLow: 43, prefixLen: 4, rangeLen: 5, prefixCode: 0xb},
		{rangeLow: 75, prefixLen: 4, rangeLen: 6, prefixCode: 0xc},
		{rangeLow: 139, prefixLen: 5, rangeLen: 7, prefixCode: 0x1b},
		{rangeLow: 267, prefixLen: 5, rangeLen: 8, prefixCode: 0x1c},
		{rangeLow: 523, prefixLen: 6, rangeLen: 8, prefixCode: 0x3c},
		{rangeLow: 779, prefixLen: 7, rangeLen: 9, prefixCode: 0x7d},
		{rangeLow: 1291, prefixLen: 6, rangeLen: 11, prefixCode: 0x3d},
		{rangeLow: -32, prefixLen: 9, rangeLen: 32, prefixCode: 0x1fe, lower: true},
		{rangeLow: 3339, prefixLen: 9, rangeLen: 32, prefixCode: 0x1ff},
		{prefixLen: 2, prefixCode: 0x0, oob: true},
	},
	10: {
		{rangeLow: -21, prefixLen: 7, rangeLen: 4, prefixCode: 0x7a},
		{rangeLow: -5, prefixLen: 8, rangeLen: 0, prefixCode: 0xfc},
		{rangeLow: -4, prefixLen: 7, rangeLen: 0, prefixCode: 0x7b},
		{rangeLow: -3, prefixLen: 5, rangeLen: 0, prefixCode: 0x18},
		{rangeLow: -2, prefixLen: 2, rangeLen: 2, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 5, rangeLen: 0, prefixCode: 0x19},
		{rangeLow: 3, prefixLen: 6, rangeLen: 0, prefixCode: 0x36},
		{rangeLow: 4, prefixLen: 7, rangeLen: 0, prefixCode: 0x7c},
		{rangeLow: 5, prefixLen: 8, rangeLen: 0, prefixCode: 0xfd},
		{rangeLow: 6, prefixLen: 2, rangeLen: 6, prefixCode: 0x1},
		{rangeLow: 70, prefixLen: 5, rangeLen: 5, prefixCode: 0x1a},
		{rangeLow: 102, prefixLen: 6, rangeLen: 5, prefixCode: 0x37},
		{rangeLow: 134, prefixLen: 6, rangeLen: 6, prefixCode: 0x38},
		{rangeLow: 198, prefixLen: 6, rangeLen: 7, prefixCode: 0x39},
		{rangeLow: 326, prefixLen: 6, rangeLen: 8, prefixCode: 0x3a},
		{rangeLow: 582, prefixLen: 6, rangeLen: 9, prefixCode: 0x3b},
		{rangeLow: 1094, prefixLen: 6, rangeLen: 10, prefixCode: 0x3c},
		{rangeLow: 2118, prefixLen: 7, rangeLen: 11, prefixCode: 0x7d},
		{rangeLow: -22, prefixLen: 8, rangeLen: 32, prefixCode: 0xfe, lower: true},
		{rangeLow: 4166, prefixLen: 8, rangeLen: 32, prefixCode: 0xff},
		{prefixLen: 2, prefixCode: 0x2, oob: true},
	},
	11: {
		{rangeLow: 1, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 2, rangeLen: 1, prefixCode: 0x2},
		{rangeLow: 4, prefixLen: 4, rangeLen: 0, prefixCode: 0xc},
		{rangeLow: 5, prefixLen: 4, rangeLen: 1, prefixCode: 0xd},
		{rangeLow: 7, prefixLen: 5, rangeLen: 1, prefixCode: 0x1c},
		{rangeLow: 9, prefixLen: 5, rangeLen: 2, prefixCode: 0x1d},
		{rangeLow: 13, prefixLen: 6, rangeLen: 2, prefixCode: 0x3c},
		{rangeLow: 17, prefixLen: 7, rangeLen: 2, prefixCode: 0x7a},
		{rangeLow: 21, prefixLen: 7, rangeLen: 3, prefixCode: 0x7b},
		{rangeLow: 29, prefixLen: 7, rangeLen: 4, prefixCode: 0x7c},
		{rangeLow: 45, prefixLen: 7, rangeLen: 5, prefixCode: 0x7d},
		{rangeLow: 77, prefixLen: 7, rangeLen: 6, prefixCode: 0x7e},
		{rangeLow: 141, prefixLen: 7, rangeLen: 32, prefixCode: 0x7f},
	},
	12: {
		{rangeLow: 1, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 2, rangeLen: 0, prefixCode: 0x2},
		{rangeLow: 3, prefixLen: 3, rangeLen: 1, prefixCode: 0x6},
		{rangeLow: 5, prefixLen: 5, rangeLen: 0, prefixCode: 0x1c},
		{rangeLow: 6, prefixLen: 5, rangeLen: 1, prefixCode: 0x1d},
		{rangeLow: 8, prefixLen: 6, rangeLen: 1, prefixCode: 0x3c},
		{rangeLow: 10, prefixLen: 7, rangeLen: 0, prefixCode: 0x7a},
		{rangeLow: 11, prefixLen: 7, rangeLen: 1, prefixCode: 0x7b},
		{rangeLow: 13, prefixLen: 7, rangeLen: 2, prefixCode: 0x7c},
		{rangeLow: 17, prefixLen: 7, rangeLen: 3, prefixCode: 0x7d},
		{rangeLow: 25, prefixLen: 7, rangeLen: 4, prefixCode: 0x7e},
		{rangeLow: 41, prefixLen: 8, rangeLen: 5, prefixCode: 0xfe},
		{rangeLow: 73, prefixLen: 8, rangeLen: 32, prefixCode: 0xff},
	},
	13: {
		{rangeLow: 1, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 2, prefixLen: 3, rangeLen: 0, prefixCode: 0x4},
		{rangeLow: 3, prefixLen: 4, rangeLen: 0, prefixCode: 0xc},
		{rangeLow: 4, prefixLen: 5, rangeLen: 0, prefixCode: 0x1c},
		{rangeLow: 5, prefixLen: 4, rangeLen: 1, prefixCode: 0xd},
		{rangeLow: 7, prefixLen: 3, rangeLen: 3, prefixCode: 0x5},
		{rangeLow: 15, prefixLen: 6, rangeLen: 1, prefixCode: 0x3a},
		{rangeLow: 17, prefixLen: 6, rangeLen: 2, prefixCode: 0x3b},
		{rangeLow: 21, prefixLen: 6, rangeLen: 3, prefixCode: 0x3c},
		{rangeLow: 29, prefixLen: 6, rangeLen: 4, prefixCode: 0x3d},
		{rangeLow: 45, prefixLen: 6, rangeLen: 5, prefixCode: 0x3e},
		{rangeLow: 77, prefixLen: 7, rangeLen: 6, prefixCode: 0x7e},
		{rangeLow: 141, prefixLen: 7, rangeLen: 32, prefixCode: 0x7f},
	},
	14: {
		{rangeLow: -2, prefixLen: 3, rangeLen: 0, prefixCode: 0x4},
		{rangeLow: -1, prefixLen: 3, rangeLen: 0, prefixCode: 0x5},
		{rangeLow: 0, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 1, prefixLen: 3, rangeLen: 0, prefixCode: 0x6},
		{rangeLow: 2, prefixLen: 3, rangeLen: 0, prefixCode: 0x7},
	},
	15: {
		{rangeLow: -24, prefixLen: 7, rangeLen: 4, prefixCode: 0x7c},
		{rangeLow: -8, prefixLen: 6, rangeLen: 2, prefixCode: 0x3c},
		{rangeLow: -4, prefixLen: 5, rangeLen: 1, prefixCode: 0x1c},
		{rangeLow: -2, prefixLen: 4, rangeLen: 0, prefixCode: 0xc},
		{rangeLow: -1, prefixLen: 3, rangeLen: 0, prefixCode: 0x4},
		{rangeLow: 0, prefixLen: 1, rangeLen: 0, prefixCode: 0x0},
		{rangeLow: 1, prefixLen: 3, rangeLen: 0, prefixCode: 0x5},
		{rangeLow: 2, prefixLen: 4, rangeLen: 0, prefixCode: 0xd},
		{rangeLow: 3, prefixLen: 5, rangeLen: 1, prefixCode: 0x1d},
		{rangeLow: 5, prefixLen: 6, rangeLen: 2, prefixCode: 0x3d},
		{rangeLow: 9, prefixLen: 7, rangeLen: 4, prefixCode: 0x7d},
		{rangeLow: -25, prefixLen: 7, rangeLen: 32, prefixCode: 0x7e, lower: true},
		{rangeLow: 25, prefixLen: 7, rangeLen: 32, prefixCode: 0x7f},
	},
}

// jbStandardTrees builds the fifteen standard tables once, because they are
// the same for every file and two files may be decoding at once.
var jbStandardTrees = sync.OnceValue(func() [16]*jbHuffTable {
	var t [16]*jbHuffTable
	for n := 1; n <= 15; n++ {
		t[n] = newJBHuffTable(jbStandardTables[n], true)
	}
	return t
})

func jbStandardTable(n int) (*jbHuffTable, error) {
	if n < 1 || n > 15 {
		return nil, errInvalidf("JBIG2 standard table B.%d", n)
	}
	return jbStandardTrees()[n], nil
}

// jbCombine composites the columns [x0, x1) of row sy of src onto row dy of
// dst, starting at column x + x0.
func jbCombine(dst, src *jbBitmap, x, dy, sy, x0, x1, op int) {
	for j := x0; j < x1; j++ {
		v := src.at(j, sy)
		d := dst.at(x+j, dy)
		switch op {
		case 0:
			v = d | v
		case 1:
			v = d & v
		case 2:
			v = d ^ v
		case 3:
			v = ^(d ^ v) & 1
		}
		dst.set(x+j, dy, v)
	}
}

// draw combines src into dst at (x, y), clipped to dst.
func (dst *jbBitmap) draw(src *jbBitmap, x, y, op int) {
	x0, x1 := max(0, -x), min(src.w, dst.w-x)
	if x0 >= x1 {
		return
	}
	for i := 0; i < src.h; i++ {
		ty := y + i
		if ty < 0 || ty >= dst.h {
			continue
		}
		jbCombine(dst, src, x, ty, i, x0, x1, op)
	}
}

type jbTextParams struct {
	huffman, refinement bool
	w, h                int
	defPixel            uint8
	numInstances        int
	stripSize           int
	logStripSize        int
	symCodeLen          int
	transposed          bool
	dsOffset            int
	refCorner           int
	combOp              int
	refTemplate         int
	refAt               []jbPoint
}

type jbTextTables struct {
	symbolID                  *jbHuffTable
	firstS, deltaS, deltaT    *jbHuffTable
	rdw, rdh, rdx, rdy, rsize *jbHuffTable
}

// 6.4 Text region decoding procedure.
func jbTextRegion(p *jbTextParams, syms []*jbBitmap, t *jbTextTables, c *jbCoder, r *jbReader) (*jbBitmap, error) {
	bm, err := newJBBitmap(p.w, p.h)
	if err != nil {
		return nil, err
	}
	if p.defPixel != 0 {
		bm.fill(1)
	}
	if len(syms) == 0 {
		return bm, nil
	}

	value := func(table *jbHuffTable, proc int) (int32, bool) {
		if p.huffman {
			return table.decode(r)
		}
		return c.integer(proc)
	}

	stripT := 0
	if v, ok := value(t.deltaT, jbIADT); ok { // 6.4.6
		stripT = -int(v)
	}
	firstS, guard := 0, p.numInstances+1024
	for i := 0; i < p.numInstances; {
		v, ok := value(t.deltaT, jbIADT)
		if !ok || r.eof {
			break
		}
		stripT += int(v)

		v, ok = value(t.firstS, jbIAFS) // 6.4.7
		if !ok {
			break
		}
		firstS += int(v)
		currentS := firstS
		for {
			currentT := 0 // 6.4.9
			if p.stripSize > 1 {
				if p.huffman {
					currentT = int(r.bits(p.logStripSize))
				} else {
					v, _ := c.integer(jbIAIT)
					currentT = int(v)
				}
			}
			tt := p.stripSize*stripT + currentT

			var id uint32
			switch {
			case p.huffman && t.symbolID == nil:
				// 6.5.8.2.3, an aggregate names its symbols with
				// plain fixed width codes.
				id = r.bits(p.symCodeLen)
			case p.huffman:
				v, ok := t.symbolID.decode(r)
				if !ok {
					return bm, nil
				}
				id = uint32(v)
			default:
				id = c.id(p.symCodeLen)
			}

			refine := false
			if p.refinement {
				if p.huffman {
					refine = r.bit() != 0
				} else {
					v, _ := c.integer(jbIARI)
					refine = v != 0
				}
			}
			if int(id) >= len(syms) {
				return nil, errInvalidf("JBIG2 text region names symbol %d of %d", id, len(syms))
			}
			sym := syms[id]
			symW, symH := sym.w, sym.h
			if refine {
				rdw, _ := value(t.rdw, jbIARDW) // 6.4.11.1
				rdh, _ := value(t.rdh, jbIARDH) // 6.4.11.2
				rdx, _ := value(t.rdx, jbIARDX) // 6.4.11.3
				rdy, _ := value(t.rdy, jbIARDY) // 6.4.11.4
				size := int32(0)
				if p.huffman {
					size, _ = t.rsize.decode(r)
					r.align()
					c.restart(r.data, r.pos, r.end)
				}
				symW += int(rdw)
				symH += int(rdh)
				sym, err = c.refinementBitmap(symW, symH, p.refTemplate, sym,
					int(rdw>>1)+int(rdx), int(rdh>>1)+int(rdy), false, p.refAt)
				if err != nil {
					return nil, err
				}
				if p.huffman {
					if size > 0 {
						r.pos += int(size)
					} else {
						r.pos = min(c.mq.bp+1, r.end)
					}
					r.align()
				}
			}

			// 6.4.5 3(c), where the reference corner says which
			// way along S the symbol grows and so whether the
			// coordinate advances before or after it is placed.
			if p.transposed && p.refCorner&1 == 0 {
				currentS += symH - 1
			} else if !p.transposed && p.refCorner&2 != 0 {
				currentS += symW - 1
			}
			x, y := currentS, tt
			if p.transposed {
				x, y = tt, currentS
				if p.refCorner&1 == 0 {
					y -= symH - 1
				}
				if p.refCorner&2 != 0 {
					x -= symW - 1
				}
			} else {
				if p.refCorner&2 != 0 {
					x -= symW - 1
				}
				if p.refCorner&1 == 0 {
					y -= symH - 1
				}
			}
			bm.draw(sym, x, y, p.combOp)
			if p.transposed && p.refCorner&1 != 0 {
				currentS += symH - 1
			} else if !p.transposed && p.refCorner&2 == 0 {
				currentS += symW - 1
			}
			i++

			v, ok := value(t.deltaS, jbIADS) // 6.4.8
			if !ok {
				break
			}
			currentS += int(v) + p.dsOffset
			guard--
			if guard < 0 || r.eof {
				return bm, nil
			}
		}
	}
	return bm, nil
}

type jbSymbolDict struct {
	huffman, refinement bool
	huffDH, huffDW      int
	bitmapSizeSel       int
	aggInstSel          int
	template            int
	refTemplate         int
	at, refAt           []jbPoint
	numExported, numNew int
}

type jbSymbolTables struct {
	deltaHeight, deltaWidth, bitmapSize, aggInstances *jbHuffTable
}

// jbAggregateTables is the fixed table set 6.5.8.2.3 gives the text region a
// Huffman coded symbol dictionary aggregates through.
func jbAggregateTables() (*jbTextTables, error) {
	t := &jbTextTables{}
	for _, p := range []struct {
		dst **jbHuffTable
		n   int
	}{
		{&t.firstS, 6}, {&t.deltaS, 8}, {&t.deltaT, 11},
		{&t.rdw, 15}, {&t.rdh, 15}, {&t.rdx, 15}, {&t.rdy, 15}, {&t.rsize, 1},
	} {
		table, err := jbStandardTable(p.n)
		if err != nil {
			return nil, err
		}
		*p.dst = table
	}
	return t, nil
}

// 6.5 Symbol dictionary decoding procedure.
func jbSymbolDictionary(d *jbSymbolDict, input []*jbBitmap, t *jbSymbolTables, c *jbCoder, r *jbReader) ([]*jbBitmap, error) {
	if d.numNew < 0 || d.numNew > maxJBSymbols || d.numExported < 0 || d.numExported > maxJBSymbols {
		return nil, errInvalidf("JBIG2 symbol dictionary has %d symbols", d.numNew)
	}
	newSymbols := make([]*jbBitmap, 0, min(d.numNew, 1<<12))
	symCodeLen := jbLog2(len(input) + d.numNew)

	var tableB1 *jbHuffTable
	var symbolWidths []int
	var aggTables *jbTextTables
	var err error
	if d.huffman {
		if tableB1, err = jbStandardTable(1); err != nil {
			return nil, err
		}
		symCodeLen = max(symCodeLen, 1) // 6.5.8.2.3
	}
	if d.refinement {
		if aggTables, err = jbAggregateTables(); err != nil {
			return nil, err
		}
	}

	currentHeight := 0
	// A height class must produce at least one symbol, so a stream that
	// keeps opening them without ever closing one is not going anywhere.
	classes := d.numNew + 1024
	for len(newSymbols) < d.numNew && classes > 0 {
		classes--
		var deltaHeight int32
		var ok bool
		if d.huffman {
			deltaHeight, ok = t.deltaHeight.decode(r)
		} else {
			deltaHeight, ok = c.integer(jbIADH)
		}
		if !ok || r.eof {
			break
		}
		currentHeight += int(deltaHeight)
		currentWidth, totalWidth := 0, 0
		firstSymbol := len(symbolWidths)
		for widths := d.numNew + 1024; widths > 0; widths-- {
			var deltaWidth int32
			if d.huffman {
				deltaWidth, ok = t.deltaWidth.decode(r)
			} else {
				deltaWidth, ok = c.integer(jbIADW)
			}
			if !ok {
				break // 6.5.7, out of band ends the height class
			}
			currentWidth += int(deltaWidth)
			totalWidth += currentWidth
			if len(newSymbols) >= d.numNew || r.eof {
				break
			}
			switch {
			case d.refinement: // 6.5.8.2
				bm, err := jbRefinedSymbol(d, t, aggTables, input, newSymbols,
					currentWidth, currentHeight, symCodeLen, c, r)
				if err != nil {
					return nil, err
				}
				newSymbols = append(newSymbols, bm)
			case d.huffman:
				symbolWidths = append(symbolWidths, currentWidth)
			default: // 6.5.8.1
				bm, err := c.genericBitmap(currentWidth, currentHeight, d.template, false, nil, d.at)
				if err != nil {
					return nil, err
				}
				newSymbols = append(newSymbols, bm)
			}
		}
		if d.huffman && !d.refinement { // 6.5.9 height class collective bitmap
			size, _ := t.bitmapSize.decode(r)
			r.align()
			var collective *jbBitmap
			if size == 0 {
				collective, err = jbUncompressedBitmap(r, totalWidth, currentHeight)
			} else {
				end := jbAdvance(r.pos, int(size), r.end)
				sub := newJBReader(r.data, r.pos, end)
				collective, err = jbMMRBitmap(c, sub, totalWidth, currentHeight, false)
				r.pos = end
				r.align()
			}
			if err != nil {
				return nil, err
			}
			if firstSymbol == len(symbolWidths)-1 {
				newSymbols = append(newSymbols, collective)
			} else {
				xMin := 0
				for i := firstSymbol; i < len(symbolWidths); i++ {
					xMax := xMin + symbolWidths[i]
					if xMax > collective.w {
						break
					}
					newSymbols = append(newSymbols, collective.sub(xMin, xMax))
					xMin = xMax
				}
			}
		}
	}

	// 6.5.10 Exported symbols.
	total := len(input) + d.numNew
	flags := make([]bool, 0, total)
	current, guard := false, total+1024
	for len(flags) < total {
		var run int32
		var ok bool
		if d.huffman {
			run, ok = tableB1.decode(r)
		} else {
			run, ok = c.integer(jbIAEX)
		}
		if !ok || run < 0 || r.eof {
			break
		}
		for ; run > 0 && len(flags) < total; run-- {
			flags = append(flags, current)
		}
		current = !current
		guard--
		if guard < 0 {
			break
		}
	}

	exported := make([]*jbBitmap, 0, d.numExported)
	i := 0
	for ; i < len(input) && i < len(flags); i++ {
		if flags[i] {
			exported = append(exported, input[i])
		}
	}
	for j := 0; j < len(newSymbols) && i < len(flags); i, j = i+1, j+1 {
		if flags[i] {
			exported = append(exported, newSymbols[j])
		}
	}
	return exported, nil
}

// jbRefinedSymbol is 6.5.8.2, a symbol coded as a refinement of one already
// decoded or as a small text region over several of them.
func jbRefinedSymbol(d *jbSymbolDict, t *jbSymbolTables, agg *jbTextTables, input, newSymbols []*jbBitmap, w, h, symCodeLen int, c *jbCoder, r *jbReader) (*jbBitmap, error) {
	var instances int32 = 1
	if d.huffman {
		instances, _ = t.aggInstances.decode(r)
	} else {
		instances, _ = c.integer(jbIAAI)
	}
	syms := make([]*jbBitmap, 0, len(input)+len(newSymbols))
	syms = append(syms, input...)
	syms = append(syms, newSymbols...)

	if instances > 1 {
		p := &jbTextParams{
			huffman: d.huffman, refinement: true,
			w: w, h: h, numInstances: int(instances),
			stripSize: 1, symCodeLen: symCodeLen,
			refCorner: 1, refTemplate: d.refTemplate, refAt: d.refAt,
		}
		return jbTextRegion(p, syms, agg, c, r)
	}

	var id uint32
	var rdx, rdy, size int32
	if d.huffman {
		id = r.bits(symCodeLen)
		table15, err := jbStandardTable(15)
		if err != nil {
			return nil, err
		}
		rdx, _ = table15.decode(r)
		rdy, _ = table15.decode(r)
		size, _ = agg.rsize.decode(r)
		r.align()
	} else {
		id = c.id(symCodeLen)
		rdx, _ = c.integer(jbIARDX) // 6.4.11.3
		rdy, _ = c.integer(jbIARDY) // 6.4.11.4
	}
	if int(id) >= len(syms) {
		return nil, errInvalidf("JBIG2 symbol dictionary names symbol %d of %d", id, len(syms))
	}
	if d.huffman {
		c.restart(r.data, r.pos, r.end)
	}
	bm, err := c.refinementBitmap(w, h, d.refTemplate, syms[id], int(rdx), int(rdy), false, d.refAt)
	if err != nil {
		return nil, err
	}
	if d.huffman {
		if size > 0 {
			r.pos = jbAdvance(r.pos, int(size), r.end)
		} else {
			r.pos = min(c.mq.bp+1, r.end)
		}
		r.align()
	}
	return bm, nil
}

func jbUncompressedBitmap(r *jbReader, w, h int) (*jbBitmap, error) {
	bm, err := newJBBitmap(w, h)
	if err != nil {
		return nil, err
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			bm.set(x, y, uint8(r.bit()))
		}
		r.align()
	}
	return bm, nil
}

// jbMMRBitmap decodes an MMR region, CCITT Group 4 with black as one.
func jbMMRBitmap(c *jbCoder, r *jbReader, w, h int, endOfBlock bool) (*jbBitmap, error) {
	if err := c.spend(w, h); err != nil {
		return nil, err
	}
	bm, err := newJBBitmap(w, h)
	if err != nil {
		return nil, err
	}
	g := newGroup4(r.next, w, h, endOfBlock)
	eof := false
	// Group 4 hands back a byte of eight pixels, which is the bitmap's own
	// format, so a row is copied rather than taken apart and put back.
	trailing := uint8(0xff)
	if r := w & 7; r != 0 {
		trailing = ^uint8(0) << uint(8-r)
	}
	for y := 0; y < h; y++ {
		row := bm.row(y)
		for i := range row {
			v := g.next()
			if v < 0 {
				v, eof = 0, true
			}
			row[i] = uint8(v)
		}
		row[len(row)-1] &= trailing
	}
	if endOfBlock && !eof {
		for i := 0; i < 5; i++ {
			if g.next() < 0 {
				break
			}
		}
	}
	return bm, nil
}

// 6.7 Pattern dictionary decoding procedure.
func jbPatternDictionary(mmr bool, pw, ph, maxIndex, template int, c *jbCoder, r *jbReader) ([]*jbBitmap, error) {
	if pw <= 0 || ph <= 0 || maxIndex < 0 || maxIndex > 1<<16 {
		return nil, errInvalidf("JBIG2 pattern dictionary is %dx%d", pw, ph)
	}
	var at []jbPoint
	if !mmr {
		at = append(at, jbPoint{int8(max(-pw, -128)), 0})
		if template == 0 {
			at = append(at, jbPoint{-3, -1}, jbPoint{2, -2}, jbPoint{-2, -2})
		}
	}
	collective, err := jbGenericRegion(mmr, (maxIndex+1)*pw, ph, template, false, nil, at, c, r)
	if err != nil {
		return nil, err
	}
	patterns := make([]*jbBitmap, 0, maxIndex+1)
	for i := 0; i <= maxIndex; i++ {
		patterns = append(patterns, collective.sub(pw*i, pw*(i+1)))
	}
	return patterns, nil
}

func jbGenericRegion(mmr bool, w, h, template int, prediction bool, skip *jbBitmap, at []jbPoint, c *jbCoder, r *jbReader) (*jbBitmap, error) {
	if mmr {
		return jbMMRBitmap(c, r, w, h, false)
	}
	return c.genericBitmap(w, h, template, prediction, skip, at)
}

type jbHalftone struct {
	mmr              bool
	template         int
	skip             bool
	combOp           int
	defPixel         uint8
	gridW, gridH     int
	gridX, gridY     int32
	vectorX, vectorY int
	regionW, regionH int
}

// 6.6 Halftone region decoding procedure.
func jbHalftoneRegion(p *jbHalftone, patterns []*jbBitmap, c *jbCoder, r *jbReader) (*jbBitmap, error) {
	if len(patterns) == 0 {
		return nil, errInvalidf("JBIG2 halftone region has no patterns")
	}
	if p.gridW <= 0 || p.gridH <= 0 || p.gridW > maxJBPixels || p.gridH > maxJBPixels ||
		int64(p.gridW)*int64(p.gridH) > maxJBPixels {
		return nil, errInvalidf("JBIG2 halftone grid is %dx%d", p.gridW, p.gridH)
	}
	region, err := newJBBitmap(p.regionW, p.regionH)
	if err != nil {
		return nil, err
	}
	if p.defPixel != 0 {
		region.fill(1)
	}

	patW, patH := patterns[0].w, patterns[0].h
	planes := jbLog2(len(patterns))

	// 6.6.5.1, the cells the region never sees are not coded at all.
	var skip *jbBitmap
	if p.skip {
		if skip, err = newJBBitmap(p.gridW, p.gridH); err != nil {
			return nil, err
		}
		for m := 0; m < p.gridH; m++ {
			for n := 0; n < p.gridW; n++ {
				x := (int(p.gridX) + m*p.vectorY + n*p.vectorX) >> 8
				y := (int(p.gridY) + m*p.vectorX - n*p.vectorY) >> 8
				if x+patW <= 0 || x >= p.regionW || y+patH <= 0 || y >= p.regionH {
					skip.set(n, m, 1)
				}
			}
		}
	}

	var at []jbPoint
	if !p.mmr {
		at = append(at, jbPoint{3, -1})
		if p.template > 1 {
			at[0].x = 2
		}
		if p.template == 0 {
			at = append(at, jbPoint{-3, -1}, jbPoint{2, -2}, jbPoint{-2, -2})
		}
	}

	// Annex C, the gray-scale image the planes encode. MMR planes run on in
	// one stream and each ends with an end of block code.
	gray := make([]*jbBitmap, planes)
	for i := planes - 1; i >= 0; i-- {
		var plane *jbBitmap
		if p.mmr {
			plane, err = jbMMRBitmap(c, r, p.gridW, p.gridH, true)
		} else {
			plane, err = c.genericBitmap(p.gridW, p.gridH, p.template, false, skip, at)
		}
		if err != nil {
			return nil, err
		}
		gray[i] = plane
	}

	// 6.6.5.2 Rendering the patterns.
	for m := 0; m < p.gridH; m++ {
		for n := 0; n < p.gridW; n++ {
			if skip != nil && skip.at(n, m) != 0 {
				continue
			}
			bit, index := uint8(0), 0
			for j := planes - 1; j >= 0; j-- {
				bit ^= gray[j].at(n, m)
				index |= int(bit) << uint(j)
			}
			if index >= len(patterns) {
				index = len(patterns) - 1
			}
			x := (int(p.gridX) + m*p.vectorY + n*p.vectorX) >> 8
			y := (int(p.gridY) + m*p.vectorX - n*p.vectorY) >> 8
			region.draw(patterns[index], x, y, p.combOp)
		}
	}
	return region, nil
}

type jbRegionInfo struct {
	w, h, x, y int
	combOp     int
}

const jbRegionInfoLen = 17

func jbReadRegionInfo(data []byte, start int) jbRegionInfo {
	return jbRegionInfo{
		w:      int(jbU32(data, start)),
		h:      int(jbU32(data, start+4)),
		x:      int(jbU32(data, start+8)),
		y:      int(jbU32(data, start+12)),
		combOp: int(jbByte(data, start+16) & 7),
	}
}

type jbSegment struct {
	number     uint32
	kind       int
	referredTo []uint32
	start, end int
	rows       uint32
	unknown    bool
}

// 7.2 Segment header syntax.
func jbSegmentHeader(data []byte, start int) (jbSegment, int, error) {
	var s jbSegment
	if start+11 > len(data) {
		return s, 0, errInvalidf("JBIG2 segment header is truncated")
	}
	s.number = jbU32(data, start)
	flags := data[start+4]
	s.kind = int(flags & 0x3f)
	pageIs4 := flags&0x40 != 0

	pos := start + 5
	count := int(data[pos] >> 5)
	if count == 7 {
		count = int(jbU32(data, pos) & 0x1fffffff)
		if count > 1<<20 {
			return s, 0, errInvalidf("JBIG2 segment refers to %d segments", count)
		}
		pos += 4 + (count+8)/8
	} else {
		pos++
	}

	size := 4
	switch {
	case s.number <= 256:
		size = 1
	case s.number <= 65536:
		size = 2
	}
	if pos+count*size+4 > len(data) {
		return s, 0, errInvalidf("JBIG2 segment header is truncated")
	}
	for i := 0; i < count; i++ {
		switch size {
		case 1:
			s.referredTo = append(s.referredTo, uint32(data[pos]))
		case 2:
			s.referredTo = append(s.referredTo, uint32(jbU16(data, pos)))
		default:
			s.referredTo = append(s.referredTo, jbU32(data, pos))
		}
		pos += size
	}
	if pageIs4 {
		pos += 4
	} else {
		pos++
	}
	if pos+4 > len(data) {
		return s, 0, errInvalidf("JBIG2 segment header is truncated")
	}
	length := jbU32(data, pos)
	pos += 4

	if length == 0xffffffff {
		// 7.2.7, a generic region may run to a terminating sequence and
		// declare its real height in the four bytes that follow it,
		// rather than say how long it is up front.
		if s.kind != 36 && s.kind != 38 && s.kind != 39 {
			return s, 0, errInvalidf("JBIG2 segment %d has no length", s.number)
		}
		var t0, t1 byte = 0xff, 0xac
		if jbByte(data, pos+jbRegionInfoLen)&1 != 0 {
			t0, t1 = 0x00, 0x00
		}
		end := -1
		for i := pos + jbRegionInfoLen; i+6 <= len(data); i++ {
			if data[i] == t0 && data[i+1] == t1 {
				end = i + 6
				s.rows, s.unknown = jbU32(data, i+2), true
				break
			}
		}
		if end < 0 {
			return s, 0, errInvalidf("JBIG2 segment %d does not end", s.number)
		}
		length = uint32(end - pos)
	}

	s.start = pos
	if int64(pos)+int64(length) > int64(len(data)) {
		s.end = len(data)
	} else {
		s.end = pos + int(length)
	}
	return s, s.end, nil
}

// jbPage is the page bitmap, packed a bit a pixel with one for black, which is
// the form the image dictionary describes.
type jbPage struct {
	w, h     int
	stride   int
	buf      []uint8
	defPixel uint8
	combOp   int
	override bool
	started  bool
	budget   *int64
}

func (p *jbPage) grow(h int) {
	if h <= p.h || p.stride <= 0 || h > maxJBPixels || int64(h)*int64(p.stride) > maxJBPixels/8 {
		return
	}
	if p.budget != nil {
		cost := int64(h-p.h) * int64(p.w)
		if cost > *p.budget {
			return
		}
		*p.budget -= cost
	}
	old := len(p.buf)
	p.buf = append(p.buf, make([]uint8, (h-p.h)*p.stride)...)
	if p.defPixel != 0 {
		for i := old; i < len(p.buf); i++ {
			p.buf[i] = 0xff
		}
	}
	p.h = h
}

// op is the combination operator a region may override the page's with.
func (p *jbPage) op(info jbRegionInfo) int {
	if p.override {
		return info.combOp
	}
	return p.combOp
}

func (p *jbPage) draw(info jbRegionInfo, bm *jbBitmap, op int) {
	p.grow(info.y + bm.h)
	for i := 0; i < bm.h; i++ {
		y := info.y + i
		if y < 0 || y >= p.h {
			continue
		}
		base := y * p.stride
		for j := 0; j < bm.w; j++ {
			x := info.x + j
			if x < 0 || x >= p.w {
				continue
			}
			off := base + x>>3
			mask := uint8(0x80) >> uint(x&7)
			set := bm.at(j, i) != 0
			switch op {
			case 0:
				if set {
					p.buf[off] |= mask
				}
			case 1:
				if !set {
					p.buf[off] &^= mask
				}
			case 2:
				if set {
					p.buf[off] ^= mask
				}
			case 3:
				if (p.buf[off]&mask != 0) == set {
					p.buf[off] |= mask
				} else {
					p.buf[off] &^= mask
				}
			default:
				if set {
					p.buf[off] |= mask
				} else {
					p.buf[off] &^= mask
				}
			}
		}
	}
}

// extract copies the page under a region back out, which is what a refinement
// region that names no intermediate region refines.
func (p *jbPage) extract(info jbRegionInfo) (*jbBitmap, error) {
	bm, err := newJBBitmap(info.w, info.h)
	if err != nil {
		return nil, err
	}
	for i := 0; i < info.h; i++ {
		y := info.y + i
		if y < 0 || y >= p.h {
			continue
		}
		base := y * p.stride
		for j := 0; j < info.w; j++ {
			x := info.x + j
			if x < 0 || x >= p.w {
				continue
			}
			if p.buf[base+x>>3]&(uint8(0x80)>>uint(x&7)) != 0 {
				bm.set(j, i, 1)
			}
		}
	}
	return bm, nil
}

type jbig2Decoder struct {
	page     jbPage
	symbols  map[uint32][]*jbBitmap
	patterns map[uint32][]*jbBitmap
	tables   map[uint32]*jbHuffTable
	regions  map[uint32]*jbBitmap
	budget   int64
	// maxBudget caps what a page segment may raise the budget to.
	maxBudget int64
}

// emit stores an intermediate region for a later segment to refer to, or draws
// an immediate one onto the page.
func (d *jbig2Decoder) emit(seg jbSegment, info jbRegionInfo, bm *jbBitmap) {
	switch seg.kind {
	case 4, 20, 36, 40:
		if d.regions == nil {
			d.regions = map[uint32]*jbBitmap{}
		}
		d.regions[seg.number] = bm
	default:
		d.page.draw(info, bm, d.page.op(info))
	}
}

// reference returns what a refinement region refines: an intermediate region
// it names, or the page underneath it, 7.4.7.2.
func (d *jbig2Decoder) reference(seg jbSegment, info jbRegionInfo) (*jbBitmap, bool, error) {
	for _, n := range seg.referredTo {
		if bm := d.regions[n]; bm != nil {
			return bm, false, nil
		}
	}
	bm, err := d.page.extract(info)
	return bm, true, err
}

// segments walks one chunk, which is either the globals stream or the image
// stream; both carry segments in the embedded organization, header then data.
func (d *jbig2Decoder) segments(data []byte) error {
	pos := 0
	for pos < len(data) {
		seg, next, err := jbSegmentHeader(data, pos)
		if err != nil {
			return err
		}
		if err := d.segment(data, seg); err != nil {
			return err
		}
		if next <= pos {
			break
		}
		pos = next
		if seg.kind == 51 { // end of file
			break
		}
	}
	return nil
}

// inputSymbols gathers what the segments this one refers to exported.
func (d *jbig2Decoder) inputSymbols(refs []uint32) []*jbBitmap {
	var syms []*jbBitmap
	for _, n := range refs {
		if len(syms)+len(d.symbols[n]) > maxJBSymbols {
			break
		}
		syms = append(syms, d.symbols[n]...)
	}
	return syms
}

// customTable returns the index'th Tables segment among those referred to,
// 7.4.2.1.6 and 7.4.3.1.6.
func (d *jbig2Decoder) customTable(index int, refs []uint32) (*jbHuffTable, error) {
	seen := 0
	for _, n := range refs {
		if t := d.tables[n]; t != nil {
			if seen == index {
				return t, nil
			}
			seen++
		}
	}
	return nil, errInvalidf("JBIG2 names custom table %d", index)
}

// selectTable picks a standard table for a selector below 3 and a custom one
// for 3, advancing the custom index as the tables are consumed in order.
func (d *jbig2Decoder) selectTable(sel, base int, refs []uint32, index *int) (*jbHuffTable, error) {
	if sel == 3 {
		t, err := d.customTable(*index, refs)
		*index++
		return t, err
	}
	return jbStandardTable(base + sel)
}

func (d *jbig2Decoder) segment(data []byte, seg jbSegment) error {
	pos, end := seg.start, seg.end
	switch seg.kind {
	case 0: // 7.4.2 Symbol dictionary
		if pos+2 > end {
			return errInvalidf("JBIG2 symbol dictionary is truncated")
		}
		flags := jbU16(data, pos)
		pos += 2
		sd := jbSymbolDict{
			huffman:       flags&1 != 0,
			refinement:    flags&2 != 0,
			huffDH:        int(flags>>2) & 3,
			huffDW:        int(flags>>4) & 3,
			bitmapSizeSel: int(flags>>6) & 1,
			aggInstSel:    int(flags>>7) & 1,
			template:      int(flags>>10) & 3,
			refTemplate:   int(flags>>12) & 1,
		}
		if !sd.huffman {
			n := 1
			if sd.template == 0 {
				n = 4
			}
			for i := 0; i < n; i++ {
				sd.at = append(sd.at, jbPoint{jbI8(data, pos), jbI8(data, pos+1)})
				pos += 2
			}
		}
		if sd.refinement && sd.refTemplate == 0 {
			for i := 0; i < 2; i++ {
				sd.refAt = append(sd.refAt, jbPoint{jbI8(data, pos), jbI8(data, pos+1)})
				pos += 2
			}
		}
		sd.numExported = int(jbU32(data, pos))
		sd.numNew = int(jbU32(data, pos+4))
		pos += 8

		var tables jbSymbolTables
		var err error
		if sd.huffman {
			index := 0
			if tables.deltaHeight, err = d.selectTable(sd.huffDH, 4, seg.referredTo, &index); err != nil {
				return err
			}
			if tables.deltaWidth, err = d.selectTable(sd.huffDW, 2, seg.referredTo, &index); err != nil {
				return err
			}
			if tables.bitmapSize, err = d.selectTable(sd.bitmapSizeSel*3, 1, seg.referredTo, &index); err != nil {
				return err
			}
			if tables.aggInstances, err = d.selectTable(sd.aggInstSel*3, 1, seg.referredTo, &index); err != nil {
				return err
			}
		}
		syms, err := jbSymbolDictionary(&sd, d.inputSymbols(seg.referredTo), &tables,
			newJBCoder(data, pos, end, &d.budget), newJBReader(data, pos, end))
		if err != nil {
			return err
		}
		if d.symbols == nil {
			d.symbols = map[uint32][]*jbBitmap{}
		}
		d.symbols[seg.number] = syms

	case 4, 6, 7: // 7.4.3 Text region
		if pos+jbRegionInfoLen+2 > end {
			return errInvalidf("JBIG2 text region is truncated")
		}
		info := jbReadRegionInfo(data, pos)
		pos += jbRegionInfoLen
		flags := jbU16(data, pos)
		pos += 2
		p := jbTextParams{
			huffman:      flags&1 != 0,
			refinement:   flags&2 != 0,
			w:            info.w,
			h:            info.h,
			logStripSize: int(flags>>2) & 3,
			refCorner:    int(flags>>4) & 3,
			transposed:   flags&64 != 0,
			combOp:       int(flags>>7) & 3,
			defPixel:     uint8(flags>>9) & 1,
			dsOffset:     int(int32(uint32(flags)<<17) >> 27),
			refTemplate:  int(flags>>15) & 1,
		}
		p.stripSize = 1 << uint(p.logStripSize)

		var huffFlags uint16
		if p.huffman {
			huffFlags = jbU16(data, pos)
			pos += 2
		}
		if p.refinement && p.refTemplate == 0 {
			for i := 0; i < 2; i++ {
				p.refAt = append(p.refAt, jbPoint{jbI8(data, pos), jbI8(data, pos+1)})
				pos += 2
			}
		}
		p.numInstances = int(jbU32(data, pos))
		pos += 4
		if p.numInstances < 0 || p.numInstances > 1<<24 {
			return errInvalidf("JBIG2 text region places %d symbols", p.numInstances)
		}

		syms := d.inputSymbols(seg.referredTo)
		p.symCodeLen = jbLog2(len(syms))
		r := newJBReader(data, pos, end)
		tables := &jbTextTables{}
		if p.huffman {
			var err error
			if tables, err = d.textTables(&p, huffFlags, seg.referredTo, len(syms), r); err != nil {
				return err
			}
			p.symCodeLen = max(p.symCodeLen, 1)
		}
		bm, err := jbTextRegion(&p, syms, tables, newJBCoder(data, r.pos, end, &d.budget), r)
		if err != nil {
			return err
		}
		d.emit(seg, info, bm)

	case 16: // 7.4.4 Pattern dictionary
		if pos+7 > end {
			return errInvalidf("JBIG2 pattern dictionary is truncated")
		}
		flags := data[pos]
		mmr := flags&1 != 0
		template := int(flags>>1) & 3
		pw, ph := int(data[pos+1]), int(data[pos+2])
		maxIndex := int(jbU32(data, pos+3))
		pos += 7
		patterns, err := jbPatternDictionary(mmr, pw, ph, maxIndex, template,
			newJBCoder(data, pos, end, &d.budget), newJBReader(data, pos, end))
		if err != nil {
			return err
		}
		if d.patterns == nil {
			d.patterns = map[uint32][]*jbBitmap{}
		}
		d.patterns[seg.number] = patterns

	case 20, 22, 23: // 7.4.5 Halftone region
		if pos+jbRegionInfoLen+21 > end {
			return errInvalidf("JBIG2 halftone region is truncated")
		}
		info := jbReadRegionInfo(data, pos)
		pos += jbRegionInfoLen
		flags := data[pos]
		pos++
		h := jbHalftone{
			mmr:      flags&1 != 0,
			template: int(flags>>1) & 3,
			skip:     flags&8 != 0,
			combOp:   int(flags>>4) & 7,
			defPixel: uint8(flags>>7) & 1,
			gridW:    int(jbU32(data, pos)),
			gridH:    int(jbU32(data, pos+4)),
			gridX:    int32(jbU32(data, pos+8)),
			gridY:    int32(jbU32(data, pos+12)),
			vectorX:  int(jbU16(data, pos+16)),
			vectorY:  int(jbU16(data, pos+18)),
			regionW:  info.w,
			regionH:  info.h,
		}
		pos += 20
		var patterns []*jbBitmap
		for _, n := range seg.referredTo {
			if p := d.patterns[n]; p != nil {
				patterns = p
				break
			}
		}
		bm, err := jbHalftoneRegion(&h, patterns, newJBCoder(data, pos, end, &d.budget), newJBReader(data, pos, end))
		if err != nil {
			return err
		}
		d.emit(seg, info, bm)

	case 36, 38, 39: // 7.4.6 Generic region
		if pos+jbRegionInfoLen+1 > end {
			return errInvalidf("JBIG2 generic region is truncated")
		}
		info := jbReadRegionInfo(data, pos)
		pos += jbRegionInfoLen
		flags := data[pos]
		pos++
		mmr := flags&1 != 0
		template := int(flags>>1) & 3
		prediction := flags&8 != 0
		var at []jbPoint
		if !mmr {
			n := 1
			if template == 0 {
				n = 4
			}
			for i := 0; i < n; i++ {
				at = append(at, jbPoint{jbI8(data, pos), jbI8(data, pos+1)})
				pos += 2
			}
		}
		if seg.unknown {
			info.h = int(seg.rows)
		}
		bm, err := jbGenericRegion(mmr, info.w, info.h, template, prediction, nil, at,
			newJBCoder(data, pos, end, &d.budget), newJBReader(data, pos, end))
		if err != nil {
			return err
		}
		d.emit(seg, info, bm)

	case 40, 42, 43: // 7.4.7 Generic refinement region
		if pos+jbRegionInfoLen+1 > end {
			return errInvalidf("JBIG2 refinement region is truncated")
		}
		info := jbReadRegionInfo(data, pos)
		pos += jbRegionInfoLen
		flags := data[pos]
		pos++
		template := int(flags) & 1
		prediction := flags&2 != 0
		var at []jbPoint
		if template == 0 {
			for i := 0; i < 2; i++ {
				at = append(at, jbPoint{jbI8(data, pos), jbI8(data, pos+1)})
				pos += 2
			}
		}
		ref, fromPage, err := d.reference(seg, info)
		if err != nil {
			return err
		}
		bm, err := newJBCoder(data, pos, end, &d.budget).refinementBitmap(info.w, info.h, template, ref, 0, 0, prediction, at)
		if err != nil {
			return err
		}
		if seg.kind == 40 {
			d.emit(seg, info, bm)
		} else if fromPage {
			d.page.draw(info, bm, 4)
		} else {
			d.page.draw(info, bm, d.page.op(info))
		}

	case 48: // 7.4.8 Page information
		if pos+19 > end {
			return errInvalidf("JBIG2 page information is truncated")
		}
		w, h := int(jbU32(data, pos)), int(jbU32(data, pos+4))
		flags := data[pos+16]
		if jbU32(data, pos+4) == 0xffffffff {
			h = 0
		}
		if w <= 0 || w > maxJBPixels || h > maxJBPixels || int64(w)*int64(max(h, 1)) > maxJBPixels {
			return errInvalidf("JBIG2 page is %dx%d", w, h)
		}
		d.budget = max(d.budget, min(8*int64(w)*int64(max(h, 1)), d.maxBudget))
		d.page = jbPage{
			budget:   &d.budget,
			w:        w,
			stride:   (w + 7) / 8,
			defPixel: uint8(flags>>2) & 1,
			combOp:   int(flags>>3) & 3,
			override: flags&64 != 0,
			started:  true,
		}
		d.page.grow(h)

	case 50: // 7.4.9 End of stripe
		d.page.grow(int(jbU32(data, pos)) + 1)

	case 53: // 7.4.13 Tables
		t, err := jbTablesSegment(data, pos, end)
		if err != nil {
			return err
		}
		if d.tables == nil {
			d.tables = map[uint32]*jbHuffTable{}
		}
		d.tables[seg.number] = t
	}
	return nil
}

// 7.4.3.1.7 Symbol ID Huffman table decoding.
func jbSymbolIDTable(r *jbReader, numSymbols int) (*jbHuffTable, error) {
	codes := make([]jbHuffLine, 0, 35)
	for i := 0; i <= 34; i++ {
		codes = append(codes, jbHuffLine{rangeLow: int32(i), prefixLen: uint8(r.bits(4))})
	}
	runCodes := newJBHuffTable(codes, false)

	codes = codes[:0]
	for i := 0; i < numSymbols; {
		v, ok := runCodes.decode(r)
		if !ok || r.eof {
			return nil, errInvalidf("JBIG2 symbol ID table ends early")
		}
		if v < 32 {
			codes = append(codes, jbHuffLine{rangeLow: int32(i), prefixLen: uint8(v)})
			i++
			continue
		}
		var repeats int
		var length uint8
		switch v {
		case 32:
			if i == 0 {
				return nil, errInvalidf("JBIG2 symbol ID table repeats nothing")
			}
			repeats = int(r.bits(2)) + 3
			length = codes[i-1].prefixLen
		case 33:
			repeats = int(r.bits(3)) + 3
		case 34:
			repeats = int(r.bits(7)) + 11
		default:
			return nil, errInvalidf("JBIG2 symbol ID table has code %d", v)
		}
		for j := 0; j < repeats; j++ {
			codes = append(codes, jbHuffLine{rangeLow: int32(i), prefixLen: length})
			i++
		}
	}
	r.align()
	return newJBHuffTable(codes, false), nil
}

// 7.4.3.1.6 Text region segment Huffman table selection.
func (d *jbig2Decoder) textTables(p *jbTextParams, flags uint16, refs []uint32, numSymbols int, r *jbReader) (*jbTextTables, error) {
	t := &jbTextTables{}
	var err error
	if t.symbolID, err = jbSymbolIDTable(r, numSymbols); err != nil {
		return nil, err
	}
	index := 0
	if t.firstS, err = d.selectTable(int(flags)&3, 6, refs, &index); err != nil {
		return nil, err
	}
	if t.deltaS, err = d.selectTable(int(flags>>2)&3, 8, refs, &index); err != nil {
		return nil, err
	}
	if t.deltaT, err = d.selectTable(int(flags>>4)&3, 11, refs, &index); err != nil {
		return nil, err
	}
	if !p.refinement {
		return t, nil
	}
	if t.rdw, err = d.selectTable(int(flags>>6)&3, 14, refs, &index); err != nil {
		return nil, err
	}
	if t.rdh, err = d.selectTable(int(flags>>8)&3, 14, refs, &index); err != nil {
		return nil, err
	}
	if t.rdx, err = d.selectTable(int(flags>>10)&3, 14, refs, &index); err != nil {
		return nil, err
	}
	if t.rdy, err = d.selectTable(int(flags>>12)&3, 14, refs, &index); err != nil {
		return nil, err
	}
	if flags&0x4000 != 0 {
		t.rsize, err = d.customTable(index, refs)
	} else {
		t.rsize, err = jbStandardTable(1)
	}
	return t, err
}

func jbCoded(f *File, s *Stream) bool {
	for _, n := range f.names(s.Dict, "Filter", "F") {
		if n == "JBIG2Decode" {
			return true
		}
	}
	return false
}

// jbig2Decode expands a JBIG2 embedded stream into packed one bit rows. JBIG2
// writes one for black and a gray image reads zero as black, so the page is
// inverted on the way out, as ISO 32000-1 7.4.7 requires.
func jbig2Decode(f *File, data []byte, parms Dict, self Ref) ([]byte, error) {
	d := jbig2Decoder{budget: startJBudget, maxBudget: maxJBBudget}
	if f != nil {
		// A globals stream carries segments, not a JBIG2 image, so it is
		// never itself JBIG2 coded; one that is, or one that names the
		// image it belongs to, is a cycle rather than a file.
		if s := f.GetStream(parms["JBIG2Globals"]); s != nil && s.Ref != self && !jbCoded(f, s) {
			globals, err := s.Data()
			if err != nil {
				f.errorf("JBIG2 globals: %v", err)
			} else if err := d.segments(globals); err != nil {
				f.errorf("%v", err)
			}
		}
	}
	if err := d.segments(data); err != nil {
		if !d.page.started {
			return nil, err
		}
		if f != nil {
			f.errorf("%v", err)
		}
	}
	if !d.page.started {
		return nil, errInvalidf("JBIG2 stream has no page")
	}
	out := d.page.buf
	for i := range out {
		out[i] ^= 0xff
	}
	return out, nil
}
