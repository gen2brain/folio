package font

// The GPOS lookup types.
const (
	gposSingle = iota + 1
	gposPair
	gposCursive
	gposMarkBase
	gposMarkLig
	gposMarkMark
	gposContext
	gposChain
	gposExtension
)

// position runs every stage of the plan over the buffer and then settles the
// marks onto what they were attached to.
func (p *shapePlan) position(b *buffer) {
	for _, st := range p.gpos {
		for _, l := range st.lookups {
			b.applyPos(l.lookup, l.mask, 0)
		}
	}
	// A mark carries the advance of whatever glyph it happens to be, which
	// is not what it takes on the line: it takes none.
	for i := range b.items {
		if b.f.gdef.class(b.items[i].gid) == classMark {
			b.items[i].xadv, b.items[i].yadv = 0, 0
		}
	}
	for i := range b.items {
		b.settle(i, 0)
	}
}

// applyPos walks the buffer once for one lookup.
func (b *buffer) applyPos(l *otLookup, mask uint32, depth int) {
	if l == nil || depth > maxLookupNesting {
		return
	}
	for i := 0; i < len(b.items); {
		if b.items[i].mask&mask == 0 || b.skip(l, i) {
			i++
			continue
		}
		n := 0
		for _, sub := range l.subs {
			if n = b.posAt(l, sub, i, depth); n != 0 {
				break
			}
		}
		i += max(n, 1)
	}
}

// posAt tries one subtable at one place.
func (b *buffer) posAt(l *otLookup, off, at, depth int) int {
	d := b.f.gpos.data
	if off+2 > len(d) {
		return 0
	}
	switch l.kind {
	case gposSingle:
		return b.singlePos(d, off, at)
	case gposPair:
		return b.pairPos(d, l, off, at)
	case gposCursive:
		return b.cursivePos(d, l, off, at)
	case gposMarkBase:
		return b.markBasePos(d, l, off, at)
	case gposMarkLig:
		return b.markLigPos(d, l, off, at)
	case gposMarkMark:
		return b.markMarkPos(d, l, off, at)
	case gposContext:
		return b.contextPos(d, l, off, at, depth, false)
	case gposChain:
		return b.contextPos(d, l, off, at, depth, true)
	case gposExtension:
		kind, sub, ok := extension(d, off)
		if !ok {
			return 0
		}
		return b.posAt(&otLookup{kind: kind, flag: l.flag, markSet: l.markSet}, sub, at, depth)
	}
	return 0
}

// valueSize is how many bytes a value record of a format takes.
func valueSize(format int) int {
	n := 0
	for f := format & 0xff; f != 0; f >>= 1 {
		n += f & 1
	}
	return 2 * n
}

// applyValue moves a glyph by what a value record says. The device tables are
// read past: they only matter at a size this does not work in.
func (b *buffer) applyValue(d []byte, off, format, at int) {
	p := off
	take := func(bit int) int {
		if format&bit == 0 {
			return 0
		}
		v := be16s(d, p)
		p += 2
		return v
	}
	x := take(0x0001)
	y := take(0x0002)
	xa := take(0x0004)
	ya := take(0x0008)
	b.items[at].xoff += x
	b.items[at].yoff += y
	b.items[at].xadv += xa
	b.items[at].yadv += ya
}

func (b *buffer) singlePos(d []byte, off, at int) int {
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 {
		return 0
	}
	format := be16(d, off+4)
	switch be16(d, off) {
	case 1:
		b.applyValue(d, off+6, format, at)
		return 1
	case 2:
		if i >= be16(d, off+6) {
			return 0
		}
		b.applyValue(d, off+8+valueSize(format)*i, format, at)
		return 1
	}
	return 0
}

func (b *buffer) pairPos(d []byte, l *otLookup, off, at int) int {
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 {
		return 0
	}
	nxt := b.next(l, at)
	if nxt >= len(b.items) {
		return 0
	}
	f1, f2 := be16(d, off+4), be16(d, off+6)
	s1, s2 := valueSize(f1), valueSize(f2)
	switch be16(d, off) {
	case 1:
		if i >= be16(d, off+8) {
			return 0
		}
		set := off + be16(d, off+10+2*i)
		if set+2 > len(d) {
			return 0
		}
		n := be16(d, set)
		rec := 2 + s1 + s2
		for k := range n {
			p := set + 2 + rec*k
			if p+2 > len(d) || be16(d, p) != b.items[nxt].gid {
				continue
			}
			b.applyValue(d, p+2, f1, at)
			b.applyValue(d, p+2+s1, f2, nxt)
			if s2 == 0 {
				return 1
			}
			return nxt - at + 1
		}
	case 2:
		c1 := classOf(d, off+be16(d, off+8), b.items[at].gid)
		c2 := classOf(d, off+be16(d, off+10), b.items[nxt].gid)
		n1, n2 := be16(d, off+12), be16(d, off+14)
		if c1 >= n1 || c2 >= n2 {
			return 0
		}
		rec := s1 + s2
		p := off + 16 + rec*(c1*n2+c2)
		if p+rec > len(d) {
			return 0
		}
		b.applyValue(d, p, f1, at)
		b.applyValue(d, p+s1, f2, nxt)
		if s2 == 0 {
			return 1
		}
		return nxt - at + 1
	}
	return 0
}

// anchor reads an anchor table, which is a point in glyph space.
func anchor(d []byte, off int) (int, int, bool) {
	if off <= 0 || off+6 > len(d) {
		return 0, 0, false
	}
	return be16s(d, off+2), be16s(d, off+4), true
}

func (b *buffer) cursivePos(d []byte, l *otLookup, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 || i >= be16(d, off+4) {
		return 0
	}
	nxt := b.next(l, at)
	if nxt >= len(b.items) {
		return 0
	}
	j := coverageIndex(d, cov, b.items[nxt].gid)
	if j < 0 {
		return 0
	}
	exitOff := be16(d, off+6+4*i+2)
	entryOff := be16(d, off+6+4*j)
	if exitOff == 0 || entryOff == 0 {
		return 0
	}
	ex, ey, ok1 := anchor(d, off+exitOff)
	en, eny, ok2 := anchor(d, off+entryOff)
	if !ok1 || !ok2 {
		return 0
	}
	// The exit of one glyph is put where the entry of the next is, which is
	// what joins a cursive script.
	if b.rtl {
		v := ex + b.items[at].xoff
		b.items[at].xadv -= v
		b.items[at].xoff -= v
		b.items[nxt].xadv = en + b.items[nxt].xoff
	} else {
		b.items[at].xadv = ex + b.items[at].xoff
		v := en + b.items[nxt].xoff
		b.items[nxt].xadv -= v
		b.items[nxt].xoff -= v
	}
	// One of the two hangs off the other, and which way round depends on the
	// direction the lookup itself declares.
	child, parent := at, nxt
	dy := eny - ey
	if l.flag&flagRTL == 0 {
		child, parent = nxt, at
		dy = -dy
	}
	b.items[child].yoff = dy
	b.items[child].attach = parent - child
	b.items[child].attachKind = attachCursive
	return 1
}

// markAnchor is the class of a mark and where it is attached by.
func markAnchor(d []byte, array, i int) (int, int, int, bool) {
	if array+2 > len(d) || i >= be16(d, array) {
		return 0, 0, 0, false
	}
	p := array + 2 + 4*i
	if p+4 > len(d) {
		return 0, 0, 0, false
	}
	x, y, ok := anchor(d, array+be16(d, p+2))
	return be16(d, p), x, y, ok
}

// attachMarkTo puts a mark where an anchor of what it sits on says.
func (b *buffer) attachMarkTo(mx, my, bx, by, at, to int) int {
	b.items[at].xoff = bx - mx
	b.items[at].yoff = by - my
	b.items[at].attach = to - at
	b.items[at].attachKind = attachMark
	return 1
}

func (b *buffer) markBasePos(d []byte, l *otLookup, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	mi := coverageIndex(d, off+be16(d, off+2), b.items[at].gid)
	if mi < 0 {
		return 0
	}
	base := b.baseBefore(at)
	if base < 0 {
		return 0
	}
	bi := coverageIndex(d, off+be16(d, off+4), b.items[base].gid)
	if bi < 0 {
		return 0
	}
	classes := be16(d, off+6)
	cls, mx, my, ok := markAnchor(d, off+be16(d, off+8), mi)
	if !ok || cls >= classes {
		return 0
	}
	array := off + be16(d, off+10)
	if array+2 > len(d) || bi >= be16(d, array) {
		return 0
	}
	p := array + 2 + 2*(bi*classes+cls)
	if p+2 > len(d) {
		return 0
	}
	bx, by, ok := anchor(d, array+be16(d, p))
	if !ok {
		return 0
	}
	return b.attachMarkTo(mx, my, bx, by, at, base)
}

func (b *buffer) markLigPos(d []byte, l *otLookup, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	mi := coverageIndex(d, off+be16(d, off+2), b.items[at].gid)
	if mi < 0 {
		return 0
	}
	lig := b.baseBefore(at)
	if lig < 0 {
		return 0
	}
	li := coverageIndex(d, off+be16(d, off+4), b.items[lig].gid)
	if li < 0 {
		return 0
	}
	classes := be16(d, off+6)
	cls, mx, my, ok := markAnchor(d, off+be16(d, off+8), mi)
	if !ok || cls >= classes {
		return 0
	}
	array := off + be16(d, off+10)
	if array+2 > len(d) || li >= be16(d, array) {
		return 0
	}
	attach := array + be16(d, array+2+2*li)
	if attach+2 > len(d) {
		return 0
	}
	comps := be16(d, attach)
	// A mark sits on the component of the ligature it was written after.
	comp := 0
	if b.items[at].ligID != 0 && b.items[at].ligID == b.items[lig].ligID {
		comp = b.items[at].ligComp
	} else {
		comp = comps - 1
	}
	if comp >= comps {
		comp = comps - 1
	}
	if comp < 0 {
		return 0
	}
	p := attach + 2 + 2*(comp*classes+cls)
	if p+2 > len(d) {
		return 0
	}
	bx, by, ok := anchor(d, attach+be16(d, p))
	if !ok {
		return 0
	}
	return b.attachMarkTo(mx, my, bx, by, at, lig)
}

func (b *buffer) markMarkPos(d []byte, l *otLookup, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	mi := coverageIndex(d, off+be16(d, off+2), b.items[at].gid)
	if mi < 0 {
		return 0
	}
	prev := b.prev(l, at)
	if prev < 0 {
		return 0
	}
	// The two have to belong to the same thing: the same ligature and the
	// same component of it, or neither in one.
	if b.items[at].ligID != b.items[prev].ligID ||
		(b.items[at].ligID != 0 && b.items[at].ligComp != b.items[prev].ligComp) {
		return 0
	}
	pi := coverageIndex(d, off+be16(d, off+4), b.items[prev].gid)
	if pi < 0 {
		return 0
	}
	classes := be16(d, off+6)
	cls, mx, my, ok := markAnchor(d, off+be16(d, off+8), mi)
	if !ok || cls >= classes {
		return 0
	}
	array := off + be16(d, off+10)
	if array+2 > len(d) || pi >= be16(d, array) {
		return 0
	}
	p := array + 2 + 2*(pi*classes+cls)
	if p+2 > len(d) {
		return 0
	}
	bx, by, ok := anchor(d, array+be16(d, p))
	if !ok {
		return 0
	}
	return b.attachMarkTo(mx, my, bx, by, at, prev)
}

// baseBefore is the nearest glyph before a mark that is not one itself, which
// is what the mark sits on.
func (b *buffer) baseBefore(at int) int {
	for k := at - 1; k >= 0; k-- {
		if b.f.gdef.class(b.items[k].gid) != classMark {
			return k
		}
	}
	return -1
}

// contextPos matches a run and runs other positioning lookups inside it.
func (b *buffer) contextPos(d []byte, l *otLookup, off, at, depth int, chain bool) int {
	var recs []subRecord
	var n int
	var ok bool
	if chain {
		n, recs, ok = b.matchChain(d, l, off, at)
	} else {
		n, recs, ok = b.matchContext(d, l, off, at)
	}
	if !ok {
		return 0
	}
	idx := []int{at}
	p := at
	for len(idx) < n {
		p = b.next(l, p)
		if p >= len(b.items) {
			break
		}
		idx = append(idx, p)
	}
	for _, r := range recs {
		if int(r.at) >= len(idx) {
			continue
		}
		sub := b.f.gpos.lookup(int(r.lookup))
		if sub == nil {
			continue
		}
		for _, s := range sub.subs {
			if b.posAt(sub, s, idx[r.at], depth+1) != 0 {
				break
			}
		}
	}
	return max(n, 1)
}

// settle moves a glyph the rest of the way onto what it was attached to, once
// everything it hangs off has been moved itself.
func (b *buffer) settle(i, depth int) {
	if depth > maxLookupNesting || b.items[i].attachKind == attachNone {
		return
	}
	j := i + b.items[i].attach
	kind := b.items[i].attachKind
	b.items[i].attachKind = attachNone
	if j < 0 || j >= len(b.items) {
		return
	}
	b.settle(j, depth+1)
	if kind == attachCursive {
		b.items[i].yoff += b.items[j].yoff
		return
	}
	b.items[i].xoff += b.items[j].xoff
	b.items[i].yoff += b.items[j].yoff
	// The offset is measured from where the base sits, so whatever the pen
	// moved between the two has to come back off.
	if j < i {
		if b.rtl {
			for k := j + 1; k <= i; k++ {
				b.items[i].xoff += b.items[k].xadv
				b.items[i].yoff += b.items[k].yadv
			}
		} else {
			for k := j; k < i; k++ {
				b.items[i].xoff -= b.items[k].xadv
				b.items[i].yoff -= b.items[k].yadv
			}
		}
	}
}
