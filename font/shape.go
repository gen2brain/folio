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

// joinTypeOf is the joining type of a code point.
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

// modifiedClass permutes the fixed position classes into the order the fonts
// of those scripts are built for.
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

// Glyph is one glyph of a shaped run: the glyph, the character it came from,
// how far the pen moves after it and how far it is drawn from the pen. The
// last four are in font units.
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
	// syl is which syllable of an Indic run the glyph is in, counting from
	// one, pos where in it, and ligated says the font made it out of several.
	syl     int
	pos     uint8
	ligated bool
	// The position GPOS works out.
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
	// rtl is a run that is laid out right to left, which is reversed once
	// the glyphs are known.
	rtl bool
}

// Shape turns a run of text into the glyphs that draw it, through the
// OpenType layout tables the font carries. The run is one script and one
// direction, and the glyphs come back in the order they are drawn.
func (f *Font) Shape(text []rune, rtl bool) []Glyph {
	b := &buffer{f: f, rtl: rtl}
	// A mark belongs to the character it follows, so the two are one cluster
	// and stay one however the glyphs are reordered.
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
	// A font with no kerning of its own in GPOS falls back to the old table.
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

// sortMarks puts the marks after a character into combining class order.
func sortMarks(runes []rune, clusters []int) {
	for i := 1; i < len(runes); i++ {
		c, _ := markOf(runes[i])
		if c == 0 {
			continue
		}
		for k := i; k > 0; k-- {
			if p, _ := markOf(runes[k-1]); p == 0 || p <= c {
				break
			}
			runes[k-1], runes[k] = runes[k], runes[k-1]
			clusters[k-1], clusters[k] = clusters[k], clusters[k-1]
		}
	}
}

// gidOf is the glyph a character maps to, and notdef when there is none.
func (f *Font) gidOf(r rune) int {
	if g := f.GIDForRune(r); g > 0 {
		return g
	}
	return 0
}

// pieces is the characters a font draws for one: the character itself when it
// has a glyph for it, and what it decomposes to when it has not.
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

// joining works out the form each cursive letter takes from what it can join
// to either side, and marks the glyph with the feature that picks it.
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
		// What stands either side decides the form: a letter joins back to
		// one that reaches forward, and on to one that reaches back.
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

// stage is one step of the plan: the lookups of a group of features, in the
// order the font lists them, and what the shaper does once they have run.
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

// shapePlan is the lookups a font runs for a script, worked out once. kerns
// says GPOS kerns of its own, so the old table is not read.
type shapePlan struct {
	gsub  []stage
	gpos  []stage
	kerns bool
}

// The order the features are applied in. Each group is a step of its own: a
// substitution one group makes is what the next group sees.
var gsubStages = [][]string{
	{"ccmp", "locl"},
	{"isol"}, {"fina"}, {"medi"}, {"init"},
	{"rlig"},
	{"rclt", "calt"},
	{"liga", "clig", "mset"},
}

// The Indic scripts run a list of their own: the features that pick the form
// of each part of a syllable, the reordering that puts the parts where the
// font has made them fit, then the ones that join the parts together.
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

// indicPause names the step that reorders a syllable once the font has said
// what each part of it looks like. It is not a feature.
const indicPause = "\x00reorder"

var gposStages = [][]string{
	{"curs"},
	{"kern", "dist"},
	{"abvm"}, {"blwm"},
	{"mark"},
	{"mkmk"},
}

// featureMask is which glyphs a feature may touch: the four cursive forms
// only the letters that take them, the Indic form features only the parts of
// a syllable that ask for them, and everything else the whole run.
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

// buildStages collects the lookups of each group of features, sorted by the
// order the font lists them, which is the order they apply in.
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

// advanceUnits is how far the pen moves after a glyph, in font units.
func (f *Font) advanceUnits(gid int) int { return int(f.Advance(gid)) }

// Shaped reports a font that carries the tables shaping reads. One that does
// not is drawn from its character map alone.
func (f *Font) Shaped() bool { return f.gsub != nil || f.gpos != nil }

// NeedsShaping reports text a font has to lay out itself: anything with a
// combining mark in it, and the scripts whose letters join, reorder or carry
// their vowels above and below. Everything else, Han and Kana among it, is
// one glyph a character and takes the shorter path.
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

// DebugLayout reports what the layout tables of a font hold, for a
// comparison against another shaper.
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
