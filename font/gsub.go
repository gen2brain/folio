package font

// The GSUB lookup types.
const (
	gsubSingle = iota + 1
	gsubMultiple
	gsubAlternate
	gsubLigature
	gsubContext
	gsubChain
	gsubExtension
	gsubReverse
)

// substitute runs every stage of the plan over the buffer.
func (p *shapePlan) substitute(b *buffer) {
	for _, st := range p.gsub {
		for _, l := range st.lookups {
			if b.mayMatch(l.digest) {
				b.applySub(l.lookup, l.mask, 0)
			}
		}
		if st.pause != nil {
			st.pause(b)
		}
	}
}

// applySub walks the buffer once for one lookup, replacing what it matches.
func (b *buffer) applySub(l *otLookup, mask uint32, depth int) {
	if l == nil || depth > maxLookupNesting {
		return
	}
	t := b.f.gsub
	for i := 0; i < len(b.items); {
		if b.items[i].mask&mask == 0 || b.skip(l, i) {
			i++
			continue
		}
		n := 0
		for _, sub := range l.subs {
			if n = b.subAt(t, l, sub, i, depth); n != 0 {
				break
			}
		}
		if n <= 0 {
			i++
			continue
		}
		i += n
	}
}

// subAt tries one subtable at one place and returns how far the buffer moves
// on, or zero when nothing matched.
func (b *buffer) subAt(t *otLayout, l *otLookup, off, at, depth int) int {
	d := t.data
	if off+2 > len(d) {
		return 0
	}
	switch l.kind {
	case gsubSingle:
		return b.singleSub(d, off, at)
	case gsubMultiple:
		return b.multipleSub(d, off, at)
	case gsubAlternate:
		// With nothing asked for, the first alternate is the one.
		return b.alternateSub(d, off, at)
	case gsubLigature:
		return b.ligatureSub(d, l, off, at)
	case gsubContext:
		return b.contextSub(d, l, off, at, depth, false)
	case gsubChain:
		return b.contextSub(d, l, off, at, depth, true)
	case gsubExtension:
		kind, sub, ok := extension(d, off)
		if !ok {
			return 0
		}
		return b.subAt(t, &otLookup{kind: kind, flag: l.flag, markSet: l.markSet}, sub, at, depth)
	case gsubReverse:
		return 0
	}
	return 0
}

// extension unwraps the subtable an extension lookup points at.
func extension(d []byte, off int) (int, int, bool) {
	if off+8 > len(d) || be16(d, off) != 1 {
		return 0, 0, false
	}
	return be16(d, off+2), off + be32(d, off+4), true
}

func (b *buffer) singleSub(d []byte, off, at int) int {
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 {
		return 0
	}
	switch be16(d, off) {
	case 1:
		b.items[at].gid = (b.items[at].gid + be16s(d, off+4)) & 0xffff
		return 1
	case 2:
		if off+6+2*i+2 > len(d) || i >= be16(d, off+4) {
			return 0
		}
		b.items[at].gid = be16(d, off+6+2*i)
		return 1
	}
	return 0
}

func (b *buffer) multipleSub(d []byte, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 || i >= be16(d, off+4) {
		return 0
	}
	seq := off + be16(d, off+6+2*i)
	if seq+2 > len(d) {
		return 0
	}
	n := be16(d, seq)
	if seq+2+2*n > len(d) {
		return 0
	}
	// A sequence of nothing deletes the glyph, which is how a font drops one
	// it has drawn as part of another.
	if n == 0 {
		b.items = append(b.items[:at], b.items[at+1:]...)
		return 0
	}
	// The first glyph replaces the one matched and the rest are inserted
	// after it, all of them in the same cluster.
	it := b.items[at]
	grow := make([]item, n)
	for k := range n {
		grow[k] = it
		grow[k].gid = be16(d, seq+2+2*k)
	}
	b.items = append(b.items[:at], append(grow, b.items[at+1:]...)...)
	return n
}

func (b *buffer) alternateSub(d []byte, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 || i >= be16(d, off+4) {
		return 0
	}
	set := off + be16(d, off+6+2*i)
	if set+4 > len(d) || be16(d, set) == 0 {
		return 0
	}
	b.items[at].gid = be16(d, set+2)
	return 1
}

func (b *buffer) ligatureSub(d []byte, l *otLookup, off, at int) int {
	if be16(d, off) != 1 {
		return 0
	}
	cov := off + be16(d, off+2)
	i := coverageIndex(d, cov, b.items[at].gid)
	if i < 0 || i >= be16(d, off+4) {
		return 0
	}
	set := off + be16(d, off+6+2*i)
	if set+2 > len(d) {
		return 0
	}
	for k := range be16(d, set) {
		lig := set + be16(d, set+2+2*k)
		if lig+4 > len(d) {
			continue
		}
		comps := be16(d, lig+2)
		if comps == 0 || lig+4+2*(comps-1) > len(d) {
			continue
		}
		// The components after the first are matched through whatever the
		// lookup skips.
		idx := []int{at}
		p := at
		ok := true
		for c := 1; c < comps; c++ {
			p = b.next(l, p)
			if p >= len(b.items) || b.items[p].gid != be16(d, lig+4+2*(c-1)) {
				ok = false
				break
			}
			idx = append(idx, p)
		}
		if !ok {
			continue
		}
		b.ligate(be16(d, lig), idx)
		return 1
	}
	return 0
}

// ligate replaces the glyphs of a ligature with the one glyph, keeping the
// marks that sat between them and telling each which component it belongs to.
func (b *buffer) ligate(gid int, idx []int) {
	b.ligID++
	id := b.ligID
	first, last := idx[0], idx[len(idx)-1]
	b.mergeClusters(first, last+1)
	b.items[first].gid = gid
	b.items[first].ligID = id
	b.items[first].ligComp = 0
	b.items[first].ligated = true

	drop := map[int]bool{}
	for _, k := range idx[1:] {
		drop[k] = true
	}
	comp := 0
	out := b.items[:first+1]
	for k := first + 1; k <= last; k++ {
		if drop[k] {
			comp++
			continue
		}
		// A mark between two components belongs to the one before it.
		it := b.items[k]
		it.ligID = id
		it.ligComp = comp
		out = append(out, it)
	}
	b.items = append(out, b.items[last+1:]...)
}

// contextSub matches a run of glyphs and runs other lookups inside it, which
// is what the context and the chaining context lookups do.
func (b *buffer) contextSub(d []byte, l *otLookup, off, at, depth int, chain bool) int {
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
	// The records name a place in the matched run and a lookup to run there.
	idx := []int{at}
	p := at
	for len(idx) < n {
		p = b.next(l, p)
		if p >= len(b.items) {
			break
		}
		idx = append(idx, p)
	}
	before := len(b.items)
	for _, r := range recs {
		if int(r.at) >= len(idx) {
			continue
		}
		sub := b.f.gsub.lookup(int(r.lookup))
		if sub == nil {
			continue
		}
		p := idx[r.at]
		if p >= len(b.items) {
			continue
		}
		for _, s := range sub.subs {
			if b.subAt(b.f.gsub, sub, s, p, depth+1) != 0 {
				break
			}
		}
	}
	// A nested lookup may have made the run shorter or longer.
	if grew := len(b.items) - before; grew != 0 {
		n += grew
	}
	return max(n, 1)
}

// subRecord is one entry of a context's list: which glyph of the match to run
// a lookup at, and which lookup.
type subRecord struct{ at, lookup uint16 }

func readRecords(d []byte, off, n int) []subRecord {
	if off+4*n > len(d) {
		return nil
	}
	out := make([]subRecord, 0, n)
	for i := range n {
		out = append(out, subRecord{uint16(be16(d, off+4*i)), uint16(be16(d, off+4*i+2))})
	}
	return out
}
