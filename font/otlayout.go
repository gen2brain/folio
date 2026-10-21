package font

import "sync"

// otLayout is a parsed GSUB or GPOS table.
type otLayout struct {
	data   []byte
	script int
	feat   int
	look   int

	mu      sync.Mutex
	lookups map[int]*otLookup
}

// otLookup is one lookup: what it does, what it skips, and where it lives.
type otLookup struct {
	kind    int
	flag    int
	subs    []int
	markSet int
}

// The bits of a lookup flag.
const (
	flagRTL          = 0x0001
	flagIgnoreBase   = 0x0002
	flagIgnoreLig    = 0x0004
	flagIgnoreMarks  = 0x0008
	flagMarkFilter   = 0x0010
	flagMarkAttach   = 0xff00
	maxLookupNesting = 8
)

// The glyph classes GDEF gives.
const (
	classBase      = 1
	classLigature  = 2
	classMark      = 3
	classComponent = 4
)

func readLayout(b []byte) *otLayout {
	if len(b) < 10 {
		return nil
	}
	t := &otLayout{
		data:   b,
		script: be16(b, 4),
		feat:   be16(b, 6),
		look:   be16(b, 8),
	}
	if t.script <= 0 || t.feat <= 0 || t.look <= 0 ||
		t.script >= len(b) || t.feat >= len(b) || t.look >= len(b) {
		return nil
	}
	return t
}

func (t *otLayout) numLookups() int { return be16(t.data, t.look) }

func (t *otLayout) lookup(i int) *otLookup {
	if i < 0 || i >= t.numLookups() {
		return nil
	}
	off := t.look + be16(t.data, t.look+2+2*i)
	if off <= t.look || off+6 > len(t.data) {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if l, ok := t.lookups[off]; ok {
		return l
	}
	if t.lookups == nil {
		t.lookups = map[int]*otLookup{}
	}
	l := t.readLookup(off)
	t.lookups[off] = l
	return l
}

func (t *otLayout) readLookup(off int) *otLookup {
	l := &otLookup{kind: be16(t.data, off), flag: be16(t.data, off+2)}
	n := be16(t.data, off+4)
	if off+6+2*n > len(t.data) {
		return nil
	}
	l.subs = make([]int, 0, n)
	for k := range n {
		l.subs = append(l.subs, off+be16(t.data, off+6+2*k))
	}
	if l.flag&flagMarkFilter != 0 {
		l.markSet = be16(t.data, off+6+2*n)
	}
	return l
}

// scriptOffset returns the offset of a script's table, by tag and then by default.
func (t *otLayout) scriptOffset(want string) int {
	b, at := t.data, t.script
	n := be16(b, at)
	if at+2+6*n > len(b) {
		return 0
	}
	fallback := 0
	for i := range n {
		p := at + 2 + 6*i
		off := at + be16(b, p+4)
		switch tag(b, p) {
		case want:
			return off
		case "DFLT", "dflt":
			fallback = off
		}
	}
	return fallback
}

// langSys is the language system of a script, the default one for no tag.
func (t *otLayout) langSys(script int, want string) int {
	b := t.data
	if script <= 0 || script+4 > len(b) {
		return 0
	}
	n := be16(b, script+2)
	if script+4+6*n > len(b) {
		return 0
	}
	if want != "" {
		for i := range n {
			p := script + 4 + 6*i
			if tag(b, p) == want {
				return script + be16(b, p+4)
			}
		}
	}
	if d := be16(b, script); d != 0 {
		return script + d
	}
	return 0
}

// featureLookups is the lookups a feature names, nil when it names none.
func (t *otLayout) featureLookups(lang int, want string) []int {
	b := t.data
	if lang <= 0 || lang+6 > len(b) {
		return nil
	}
	n := be16(b, lang+4)
	if lang+6+2*n > len(b) {
		return nil
	}
	var out []int
	tables, have := map[int]bool{}, map[int]bool{}
	for i := range n {
		fi := be16(b, lang+6+2*i)
		at := t.featureTagAt(fi)
		if tag(b, at) != want {
			continue
		}
		if off := be16(b, at+4); tables[off] {
			continue
		} else {
			tables[off] = true
		}
		for _, l := range t.lookupsOfFeature(fi) {
			if !have[l] {
				have[l] = true
				out = append(out, l)
			}
		}
	}
	return out
}

// featureTagAt returns the offset of the tag of one feature of the list.
func (t *otLayout) featureTagAt(i int) int {
	b := t.data
	if i < 0 || i >= be16(b, t.feat) {
		return -1
	}
	return t.feat + 2 + 6*i
}

// lookupsOfFeature is the lookup indices one feature names.
func (t *otLayout) lookupsOfFeature(i int) []int {
	b := t.data
	at := t.featureTagAt(i)
	if at < 0 {
		return nil
	}
	off := t.feat + be16(b, at+4)
	if off+4 > len(b) {
		return nil
	}
	n := be16(b, off+2)
	if off+4+2*n > len(b) {
		return nil
	}
	out := make([]int, 0, n)
	for k := range n {
		out = append(out, be16(b, off+4+2*k))
	}
	return out
}

// coverageIndex returns the index of a glyph in a coverage table, -1 for none.
func coverageIndex(b []byte, off, gid int) int {
	if off <= 0 || off+4 > len(b) {
		return -1
	}
	switch be16(b, off) {
	case 1:
		n := be16(b, off+2)
		lo, hi := 0, n
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if be16(b, off+4+2*mid) < gid {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo < n && be16(b, off+4+2*lo) == gid {
			return lo
		}
	case 2:
		n := be16(b, off+2)
		lo, hi := 0, n
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if be16(b, off+4+6*mid+2) < gid {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo < n {
			p := off + 4 + 6*lo
			if start, end := be16(b, p), be16(b, p+2); gid >= start && gid <= end {
				return be16(b, p+4) + gid - start
			}
		}
	}
	return -1
}

// coverageCount returns the number of glyphs a coverage table holds.
func coverageCount(b []byte, off int) int {
	if off <= 0 || off+4 > len(b) {
		return 0
	}
	n := be16(b, off+2)
	switch be16(b, off) {
	case 1:
		return n
	case 2:
		total := 0
		for i := range n {
			p := off + 4 + 6*i
			total += be16(b, p+2) - be16(b, p) + 1
		}
		return total
	}
	return 0
}

// classOf is the class a class definition table gives a glyph, zero for none.
func classOf(b []byte, off, gid int) int {
	if off <= 0 || off+4 > len(b) {
		return 0
	}
	switch be16(b, off) {
	case 1:
		start := be16(b, off+2)
		n := be16(b, off+4)
		if gid >= start && gid < start+n {
			return be16(b, off+6+2*(gid-start))
		}
	case 2:
		n := be16(b, off+2)
		lo, hi := 0, n
		for lo < hi {
			mid := int(uint(lo+hi) >> 1)
			if be16(b, off+4+6*mid+2) < gid {
				lo = mid + 1
			} else {
				hi = mid
			}
		}
		if lo < n {
			p := off + 4 + 6*lo
			if gid >= be16(b, p) && gid <= be16(b, p+2) {
				return be16(b, p+4)
			}
		}
	}
	return 0
}

type gdef struct {
	data       []byte
	classes    int
	markAttach int
	markSets   int
}

func readGDEF(b []byte) *gdef {
	if len(b) < 12 {
		return nil
	}
	g := &gdef{data: b, classes: be16(b, 4), markAttach: be16(b, 10)}
	if be16(b, 2) >= 2 && len(b) >= 14 {
		g.markSets = be16(b, 12)
	}
	return g
}

func (g *gdef) class(gid int) int {
	if g == nil {
		return 0
	}
	return classOf(g.data, g.classes, gid)
}

// attachClass is the mark attachment class a lookup flag may filter on.
func (g *gdef) attachClass(gid int) int {
	if g == nil {
		return 0
	}
	return classOf(g.data, g.markAttach, gid)
}

// inMarkSet reports a glyph in a mark glyph set a lookup flag may filter on.
func (g *gdef) inMarkSet(set, gid int) bool {
	if g == nil || g.markSets <= 0 {
		return false
	}
	b, at := g.data, g.markSets
	if at+4 > len(b) || set >= be16(b, at+2) {
		return false
	}
	off := be32(b, at+4+4*set)
	if off <= 0 || at+off >= len(b) {
		return false
	}
	return coverageIndex(b, at+off, gid) >= 0
}

// digest is a rough set of the glyphs a lookup covers, as two bit masks.
type digest struct{ lo, hi uint64 }

func (d *digest) add(gid int) {
	d.lo |= 1 << uint(gid&63)
	d.hi |= 1 << uint((gid>>6)&63)
}

func (d *digest) addRange(from, to int) {
	if to-from >= 63 {
		d.lo = ^uint64(0)
	}
	if (to>>6)-(from>>6) >= 63 {
		d.hi = ^uint64(0)
	}
	if d.lo == ^uint64(0) && d.hi == ^uint64(0) {
		return
	}
	for g := from; g <= to && g-from < 4096; g++ {
		d.add(g)
	}
}

func (d *digest) union(o digest) {
	d.lo |= o.lo
	d.hi |= o.hi
}

// mayHave reports a glyph the digest does not rule out.
func (d digest) mayHave(gid int) bool {
	return d.lo&(1<<uint(gid&63)) != 0 && d.hi&(1<<uint((gid>>6)&63)) != 0
}

// addCoverage puts every glyph of a coverage table into a digest.
func (d *digest) addCoverage(b []byte, off int) {
	if off <= 0 || off+4 > len(b) {
		d.lo, d.hi = ^uint64(0), ^uint64(0)
		return
	}
	n := be16(b, off+2)
	switch be16(b, off) {
	case 1:
		if off+4+2*n > len(b) {
			d.lo, d.hi = ^uint64(0), ^uint64(0)
			return
		}
		for i := range n {
			d.add(be16(b, off+4+2*i))
		}
	case 2:
		if off+4+6*n > len(b) {
			d.lo, d.hi = ^uint64(0), ^uint64(0)
			return
		}
		for i := range n {
			p := off + 4 + 6*i
			d.addRange(be16(b, p), be16(b, p+2))
		}
	default:
		d.lo, d.hi = ^uint64(0), ^uint64(0)
	}
}

// digestOf is the glyphs a lookup may match, the coverage its subtables have.
func (t *otLayout) digestOf(l *otLookup, gsub bool, depth int) digest {
	var d digest
	if depth > 2 {
		return digest{^uint64(0), ^uint64(0)}
	}
	for _, off := range l.subs {
		b := t.data
		if off+2 > len(b) {
			continue
		}
		format := be16(b, off)
		switch {
		case gsub && l.kind == gsubExtension, !gsub && l.kind == gposExtension:
			kind, sub, ok := extension(b, off)
			if !ok {
				return digest{^uint64(0), ^uint64(0)}
			}
			d.union(t.digestOf(&otLookup{kind: kind, subs: []int{sub}}, gsub, depth+1))
			continue
		case gsub && (l.kind == gsubContext || l.kind == gsubChain),
			!gsub && (l.kind == gposContext || l.kind == gposChain):
			if format == 3 {
				if l.kind == gsubChain || l.kind == gposChain {
					nb := be16(b, off+2)
					d.addCoverage(b, off+be16(b, off+4+2*nb+2))
				} else {
					d.addCoverage(b, off+be16(b, off+6))
				}
				continue
			}
		}
		d.addCoverage(b, off+be16(b, off+2))
	}
	return d
}
