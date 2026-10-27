package font

import (
	"fmt"
	"sort"
)

// The joining types of the Arabic shaping file, in the tables' order.
type joinType uint8

const (
	joinNone joinType = iota
	joinLeft
	joinRight
	joinDual
	joinCausing
	joinTransparent
)

func joinTypeOf(r rune) joinType {
	lo, hi := 0, len(joinTable)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r > joinTable[mid].hi {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return joinType(joinTable[lo].val)
}

// The forms a cursive letter takes, the four features that pick them.
const (
	formNone = iota
	formIsol
	formFina
	formMedi
	formInit
)

var formFeature = [...]string{"", "isol", "fina", "medi", "init"}

// modifiedClass permutes the fixed position classes into the order the fonts expect.
var modifiedClass = func() (t [256]uint8) {
	for i := range t {
		t[i] = uint8(i)
	}
	for from, to := range map[int]uint8{
		10: 22, 11: 15, 12: 16, 13: 17, 14: 23, 15: 18, 16: 19, 17: 20,
		18: 21, 19: 14, 20: 24, 21: 12, 22: 25, 23: 13, 24: 10, 25: 11,
		27: 28, 28: 29, 29: 30, 30: 31, 31: 32, 32: 33, 33: 27,
		84: 0, 91: 0, 103: 3, 130: 132, 132: 131,
	} {
		t[from] = to
	}
	return
}()

// markOf is the combining class of a code point and whether it is a mark.
func markOf(r rune) (int, bool) {
	lo, hi := 0, len(markTable)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r > markTable[mid].hi {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	v := markTable[lo].val
	return int(modifiedClass[v&0xff]), v&0x100 != 0
}

// Glyph is one glyph of a shaped run, with its cluster, and its advance and offset in font units.
type Glyph struct {
	GID                int
	Cluster            int
	XAdvance, YAdvance int
	XOffset, YOffset   int
}

// item is one glyph while the run is being shaped.
type item struct {
	gid     int
	cluster int
	// mask says which features may touch this glyph.
	mask uint32
	// ligID and ligComp say which ligature a glyph came from and which part.
	ligID, ligComp int
	// syl is the Indic syllable from one, pos the place in it, ligated a ligature.
	syl                    int
	pos                    uint8
	ligated                bool
	xoff, yoff, xadv, yadv int
	// attach chains a mark onto what it sits on, as an offset in the buffer.
	attach     int
	attachKind uint8
}

// The kinds of attachment a positioning lookup records.
const (
	attachNone = iota
	attachMark
	attachCursive
)

// buffer is a run of text on its way to being glyphs.
type buffer struct {
	f     *Font
	items []item
	out   []item
	// ligID hands out the marker that ties a ligature to its components.
	ligID int
	rtl   bool
	// maxLen bounds how many glyphs substitution may grow the run to.
	maxLen int
	// ops counts the nested lookups the run may still apply.
	ops int
	// bases is the last glyph before each that is not a mark, -1 for none.
	bases []int
}

// maxLenFactor, maxLenMin and maxOps bound run growth and nested lookups, as HarfBuzz does.
const (
	maxLenFactor = 64
	maxLenMin    = 16384
	maxOpsFactor = 1024
	maxOpsMin    = 16384
	maxOps       = 1 << 29
)

// Shape turns a run of one script and one direction into the glyphs that draw it,
// in drawing order.
func (f *Font) Shape(text []rune, rtl bool) []Glyph {
	b := &buffer{f: f, rtl: rtl, maxLen: max(len(text)*maxLenFactor, maxLenMin),
		ops: max(min(len(text), maxOps/maxOpsFactor)*maxOpsFactor, maxOpsMin)}
	cluster := 0
	var runes []rune
	var clusters []int
	for i, r := range text {
		if _, mark := markOf(r); !mark || i == 0 {
			cluster = i
		}
		for _, p := range f.pieces(r, 0) {
			runes = append(runes, p)
			clusters = append(clusters, cluster)
		}
	}
	sortMarks(runes, clusters)
	for i, r := range runes {
		b.items = append(b.items, item{gid: f.gidOf(r), cluster: clusters[i], mask: 1})
	}
	if len(b.items) == 0 {
		return nil
	}
	script := otScript(text)
	switch script {
	case "arab":
		b.joining(runes)
	case "dev2", "deva":
		b.indicShape(runes)
	}
	p := f.plan(script, rtl)
	if p != nil {
		p.substitute(b)
	}
	b.positions()
	if len(f.kern) > 0 && (p == nil || !p.kerns) {
		b.kern()
	}
	if p != nil {
		p.position(b)
	}
	if rtl {
		for i, j := 0, len(b.items)-1; i < j; i, j = i+1, j-1 {
			b.items[i], b.items[j] = b.items[j], b.items[i]
		}
	}
	out := make([]Glyph, len(b.items))
	for i, it := range b.items {
		out[i] = Glyph{GID: it.gid, Cluster: it.cluster,
			XAdvance: it.xadv, YAdvance: it.yadv, XOffset: it.xoff, YOffset: it.yoff}
	}
	return out
}

// positions fills in the advance every glyph has of itself, before GPOS.
func (b *buffer) positions() {
	for i := range b.items {
		b.items[i].xadv = b.f.advanceUnits(b.items[i].gid)
	}
}

// sortMarks orders marks by combining class up to maxMarks, as HarfBuzz does.
func sortMarks(runes []rune, clusters []int) {
	for i := 0; i < len(runes); {
		end := i
		for end < len(runes) {
			if c, _ := markOf(runes[end]); c == 0 {
				break
			}
			end++
		}
		if end-i > 1 && end-i <= maxMarks {
			for j := i + 1; j < end; j++ {
				c, _ := markOf(runes[j])
				for k := j; k > i; k-- {
					if p, _ := markOf(runes[k-1]); p <= c {
						break
					}
					runes[k-1], runes[k] = runes[k], runes[k-1]
					clusters[k-1], clusters[k] = clusters[k], clusters[k-1]
				}
			}
		}
		i = max(end, i+1)
	}
}

// maxMarks is HB_OT_SHAPE_MAX_COMBINING_MARKS.
const maxMarks = 32

// gidOf is the glyph a character maps to, and notdef when there is none.
func (f *Font) gidOf(r rune) int {
	if g := f.GIDForRune(r); g > 0 {
		return g
	}
	return 0
}

// pieces returns the characters a font draws for one, decomposed when it has no glyph.
func (f *Font) pieces(r rune, depth int) []rune {
	if depth > 4 || f.GIDForRune(r) > 0 {
		return []rune{r}
	}
	a, c, ok := decomposeRune(r)
	if !ok {
		return []rune{r}
	}
	out := f.pieces(a, depth+1)
	if f.GIDForRune(c) <= 0 {
		return []rune{r}
	}
	return append(out, c)
}

// decomposeRune is the two characters one canonically decomposes into.
func decomposeRune(r rune) (rune, rune, bool) {
	lo, hi := 0, len(decompTable)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if decompTable[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(decompTable) && decompTable[lo].r == r {
		return decompTable[lo].a, decompTable[lo].b, true
	}
	return 0, 0, false
}

// otScript is the OpenType tag of the script a run is written in.
func otScript(text []rune) string {
	for _, r := range text {
		switch scriptOf(r) {
		case scriptArabic:
			return "arab"
		case scriptHebrew:
			return "hebr"
		case scriptDevanagari:
			return "dev2"
		case scriptThai:
			return "thai"
		case scriptHan:
			return "hani"
		case scriptKana:
			return "kana"
		case scriptHangul:
			return "hang"
		case scriptCyrillic:
			return "cyrl"
		case scriptGreek:
			return "grek"
		}
	}
	return "latn"
}

// joining marks each cursive letter with the feature that picks its form.
func (b *buffer) joining(text []rune) {
	kinds := make([]joinType, len(text))
	for i, r := range text {
		kinds[i] = joinTypeOf(r)
	}
	prev := func(i int) joinType {
		for k := i - 1; k >= 0; k-- {
			if kinds[k] != joinTransparent {
				return kinds[k]
			}
		}
		return joinNone
	}
	next := func(i int) joinType {
		for k := i + 1; k < len(kinds); k++ {
			if kinds[k] != joinTransparent {
				return kinds[k]
			}
		}
		return joinNone
	}
	for i, k := range kinds {
		back := false
		switch prev(i) {
		case joinDual, joinLeft, joinCausing:
			back = true
		}
		on := false
		switch next(i) {
		case joinDual, joinRight, joinCausing:
			on = true
		}
		form := formNone
		switch k {
		case joinDual, joinCausing:
			switch {
			case back && on:
				form = formMedi
			case back:
				form = formFina
			case on:
				form = formInit
			default:
				form = formIsol
			}
		case joinRight:
			if back {
				form = formFina
			} else {
				form = formIsol
			}
		case joinLeft:
			if on {
				form = formInit
			} else {
				form = formIsol
			}
		}
		if i < len(b.items) && form != formNone {
			b.items[i].mask |= 1 << uint(form)
		}
	}
}

// stage is one step of the plan: the lookups of a group of features.
type stage struct {
	lookups []planned
	pause   func(*buffer)
}

type planned struct {
	index  int
	mask   uint32
	lookup *otLookup
	digest digest
}

// shapePlan is the lookups a font runs for a script, and whether GPOS kerns.
type shapePlan struct {
	gsub  []stage
	gpos  []stage
	kerns bool
}

// The order the features are applied in, one stage per group.
var gsubStages = [][]string{
	{"ccmp", "locl"},
	{"isol"}, {"fina"}, {"medi"}, {"init"},
	{"rlig"},
	{"rclt", "calt"},
	{"liga", "clig", "mset"},
}

// The features of the Indic scripts, with the syllable reordering between them.
var indicStages = func() [][]string {
	out := [][]string{{"ccmp", "locl"}}
	for _, f := range indicFormFeatures {
		out = append(out, []string{f})
	}
	out = append(out, []string{indicPause})
	for _, f := range indicFinalFeatures {
		out = append(out, []string{f})
	}
	return append(out, []string{"calt", "clig", "liga"})
}()

// indicPause names the stage that reorders a syllable.
const indicPause = "\x00reorder"

var gposStages = [][]string{
	{"curs"},
	{"kern", "dist"},
	{"abvm"}, {"blwm"},
	{"mark"},
	{"mkmk"},
}

// featureMask returns the mask of the glyphs a feature may touch.
func featureMask(name string) uint32 {
	for i, f := range formFeature {
		if f == name {
			return 1 << uint(i)
		}
	}
	for i, f := range indicFormFeatures {
		if f == name {
			return 1 << uint(i+8)
		}
	}
	return 1
}

// plan works out the lookups to run for a script, once per font and script.
func (f *Font) plan(script string, rtl bool) *shapePlan {
	key := script
	if rtl {
		key += "-rtl"
	}
	f.shapeMu.Lock()
	defer f.shapeMu.Unlock()
	if p, ok := f.plans[key]; ok {
		return p
	}
	var p *shapePlan
	if f.gsub != nil || f.gpos != nil {
		stages := gsubStages
		if script == "dev2" || script == "deva" {
			stages = indicStages
		}
		p = &shapePlan{
			gsub: buildStages(f.gsub, script, stages, true),
			gpos: buildStages(f.gpos, script, gposStages, false),
		}
		if f.gpos != nil {
			lang := f.gpos.langSys(f.gpos.scriptOffset(script), "")
			p.kerns = len(f.gpos.featureLookups(lang, "kern")) > 0
		}
	}
	if f.plans == nil {
		f.plans = map[string]*shapePlan{}
	}
	f.plans[key] = p
	return p
}

// buildStages collects the lookups of each group of features, in the font's order.
func buildStages(t *otLayout, script string, groups [][]string, gsub bool) []stage {
	if t == nil {
		return nil
	}
	lang := t.langSys(t.scriptOffset(script), "")
	var out []stage
	for _, group := range groups {
		if len(group) == 1 && group[0] == indicPause {
			out = append(out, stage{pause: (*buffer).indicFinal})
			continue
		}
		masks := map[int]uint32{}
		for _, name := range group {
			m := featureMask(name)
			for _, i := range t.featureLookups(lang, name) {
				masks[i] |= m
			}
		}
		if len(masks) == 0 {
			continue
		}
		var idx []int
		for i := range masks {
			idx = append(idx, i)
		}
		sort.Ints(idx)
		var st stage
		for _, i := range idx {
			if l := t.lookup(i); l != nil {
				st.lookups = append(st.lookups, planned{index: i, mask: masks[i],
					lookup: l, digest: t.digestOf(l, gsub, 0)})
			}
		}
		if len(st.lookups) > 0 {
			out = append(out, st)
		}
	}
	return out
}

// advanceUnits returns the advance of a glyph, in font units.
func (f *Font) advanceUnits(gid int) int { return int(f.Advance(gid)) }

// Shaped reports a font that carries the tables shaping reads.
func (f *Font) Shaped() bool { return f.gsub != nil || f.gpos != nil }

// NeedsShaping reports text a font has to lay out itself.
func NeedsShaping(s string) bool {
	for _, r := range s {
		if r < 0x0300 {
			continue
		}
		switch {
		case r <= 0x036f, // combining marks
			r >= 0x0590 && r <= 0x109f, // Hebrew through Myanmar
			r >= 0x1700 && r <= 0x18af, // Tagalog through Mongolian
			r >= 0x1900 && r <= 0x1cff, // Limbu through the Vedic marks
			r >= 0x1dc0 && r <= 0x1dff, // more combining marks
			r >= 0x20d0 && r <= 0x20ff,
			r >= 0xa800 && r <= 0xabff, // Syloti through Meetei
			r >= 0xfb1d && r <= 0xfdff, // Hebrew and Arabic forms
			r >= 0xfe00 && r <= 0xfe2f, // variation selectors and half marks
			r >= 0xfe70 && r <= 0xfeff,
			r >= 0x10800 && r <= 0x10fff,
			r >= 0x11000 && r <= 0x11fff, // Brahmi and the rest
			r >= 0x1e800 && r <= 0x1efff:
			return true
		}
	}
	return false
}

// DebugLayout reports what the layout tables of a font hold.
func (f *Font) DebugLayout() string {
	out := ""
	for _, t := range []struct {
		name string
		t    *otLayout
	}{{"GSUB", f.gsub}, {"GPOS", f.gpos}} {
		if t.t == nil {
			out += t.name + ": none\n"
			continue
		}
		d := t.t.data
		n := be16(d, t.t.script)
		out += t.name + ": scripts"
		for i := range n {
			out += " " + tag(d, t.t.script+2+6*i)
		}
		out += ", features"
		nf := be16(d, t.t.feat)
		seen := map[string]bool{}
		for i := range nf {
			if v := tag(d, t.t.feat+2+6*i); !seen[v] {
				seen[v] = true
				out += " " + v
			}
		}
		out += fmt.Sprintf(", %d lookups\n", t.t.numLookups())
	}
	return out
}
