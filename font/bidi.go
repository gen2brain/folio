package font

import "slices"

// The bidirectional character classes of UAX #9, in the order the generated
// table numbers them.
type bidiClass uint8

const (
	bidiL bidiClass = iota
	bidiR
	bidiAL
	bidiEN
	bidiES
	bidiET
	bidiAN
	bidiCS
	bidiNSM
	bidiBN
	bidiB
	bidiS
	bidiWS
	bidiON
	bidiLRE
	bidiLRO
	bidiRLE
	bidiRLO
	bidiPDF
	bidiLRI
	bidiRLI
	bidiFSI
	bidiPDI
)

// bidiClasses names the classes, in the order they are numbered.
var bidiClasses = [...]string{
	"L", "R", "AL", "EN", "ES", "ET", "AN", "CS", "NSM", "BN",
	"B", "S", "WS", "ON", "LRE", "LRO", "RLE", "RLO", "PDF",
	"LRI", "RLI", "FSI", "PDI",
}

// bidiMaxDepth is how deep the explicit levels may nest, UAX #9 3.3.2.
const bidiMaxDepth = 125

// bidiClassOf is the class of a code point.
func bidiClassOf(r rune) bidiClass {
	lo, hi := 0, len(bidiTable)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r > bidiTable[mid].hi {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return bidiClass(bidiTable[lo].val)
}

// isolate reports the three characters that open an isolate.
func (c bidiClass) isolate() bool { return c == bidiLRI || c == bidiRLI || c == bidiFSI }

// removedByX9 is what the weak and neutral rules never see.
func (c bidiClass) removedByX9() bool {
	switch c {
	case bidiRLE, bidiLRE, bidiRLO, bidiLRO, bidiPDF, bidiBN:
		return true
	}
	return false
}

// neutralOrIsolate is the NI of the rules.
func (c bidiClass) neutralOrIsolate() bool {
	switch c {
	case bidiB, bidiS, bidiWS, bidiON, bidiFSI, bidiLRI, bidiRLI, bidiPDI:
		return true
	}
	return false
}

// BidiLevels is the embedding level of every character of one paragraph, the
// bidirectional algorithm of UAX #9 run end to end. base is 0 for a paragraph
// that runs left to right, 1 for one that runs right to left, and negative to
// take the direction from the text itself.
func BidiLevels(text []rune, base int) []byte {
	return bidiResolve(text, base).line(0, len(text))
}

// BidiOrder is the order a line of the given levels is drawn in, left to
// right, rule L2.
func BidiOrder(levels []byte) []int {
	return bidiOrder(levels)
}

// BidiMirror is what rule L4 draws in place of a character that runs right to
// left, and the character itself where there is no other form.
func BidiMirror(r rune) rune {
	return bidiMirror(r)
}

// NeedsBidi reports text with a character in it that does not run left to
// right, which is what the algorithm has anything to say about.
func NeedsBidi(s string) bool {
	for _, r := range s {
		if r < 0x0590 {
			continue
		}
		switch bidiClassOf(r) {
		case bidiR, bidiAL, bidiAN, bidiRLE, bidiRLO, bidiRLI,
			bidiLRE, bidiLRO, bidiLRI, bidiFSI, bidiPDF, bidiPDI:
			return true
		}
	}
	return false
}

// bidiText is a paragraph with a level worked out for each of its characters.
type bidiText struct {
	text []rune
	// orig is the class each character started as, which rule L1 reads.
	orig []bidiClass
	// levels is the level of each character, para that of the paragraph.
	levels []byte
	para   byte
	// gone marks what rule X9 removed, which has no level of its own.
	gone []bool
}

// bidiResolve runs the algorithm over one paragraph. base is 0 or 1 for the
// two directions and negative to take it from the text, P2 and P3.
func bidiResolve(text []rune, base int) *bidiText {
	b := &bidiText{
		text:   text,
		orig:   make([]bidiClass, len(text)),
		levels: make([]byte, len(text)),
		gone:   make([]bool, len(text)),
	}
	for i, r := range text {
		b.orig[i] = bidiClassOf(r)
	}
	switch {
	case base == 0 || base == 1:
		b.para = byte(base)
	default:
		b.para = firstStrong(b.orig, 0, len(text))
	}
	class := append([]bidiClass(nil), b.orig...)
	b.explicit(class)
	for _, seq := range b.sequences(class) {
		b.weak(class, seq)
		b.brackets(class, seq)
		b.neutral(class, seq)
	}
	b.implicit(class)
	return b
}

// matchingPDI closes the isolate a character opens, BD9, and is the end of
// the text when nothing does.
func matchingPDI(class []bidiClass, at int) int {
	depth := 1
	for i := at + 1; i < len(class); i++ {
		switch {
		case class[i].isolate():
			depth++
		case class[i] == bidiPDI:
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(class)
}

// firstStrong is the level P2 and P3 give a range. What an isolate holds is
// not looked at.
func firstStrong(class []bidiClass, from, to int) byte {
	for i := from; i < to; i++ {
		switch c := class[i]; {
		case c.isolate():
			i = matchingPDI(class[:to], i)
		case c == bidiL:
			return 0
		case c == bidiR || c == bidiAL:
			return 1
		}
	}
	return 0
}

// status is one entry of the directional status stack of rule X1.
type status struct {
	level    byte
	override bidiClass
	isolate  bool
}

// explicit levels every character from the embeddings around it, X1 to X8.
func (b *bidiText) explicit(class []bidiClass) {
	stack := make([]status, 1, bidiMaxDepth+3)
	stack[0] = status{level: b.para, override: bidiON}
	var overflowIsolate, overflowEmbedding, validIsolate int

	next := func(odd bool) byte {
		l := stack[len(stack)-1].level
		if odd {
			return l + 1 + (l & 1)
		}
		return l + 2 - (l & 1)
	}
	top := func() status { return stack[len(stack)-1] }

	for i, c := range class {
		switch c {
		case bidiRLE, bidiLRE, bidiRLO, bidiLRO:
			b.levels[i] = top().level
			l := next(c == bidiRLE || c == bidiRLO)
			if l <= bidiMaxDepth && overflowIsolate == 0 && overflowEmbedding == 0 {
				o := bidiON
				if c == bidiRLO {
					o = bidiR
				} else if c == bidiLRO {
					o = bidiL
				}
				stack = append(stack, status{level: l, override: o})
				continue
			}
			if overflowIsolate == 0 {
				overflowEmbedding++
			}

		case bidiRLI, bidiLRI, bidiFSI:
			odd := c == bidiRLI
			if c == bidiFSI {
				odd = firstStrong(class, i+1, matchingPDI(class, i)) == 1
			}
			b.levels[i] = top().level
			if o := top().override; o != bidiON {
				class[i] = o
			}
			l := next(odd)
			if l <= bidiMaxDepth && overflowIsolate == 0 && overflowEmbedding == 0 {
				validIsolate++
				stack = append(stack, status{level: l, override: bidiON, isolate: true})
				continue
			}
			overflowIsolate++

		case bidiPDI:
			switch {
			case overflowIsolate > 0:
				overflowIsolate--
			case validIsolate == 0:
			default:
				overflowEmbedding = 0
				for !top().isolate {
					stack = stack[:len(stack)-1]
				}
				stack = stack[:len(stack)-1]
				validIsolate--
			}
			b.levels[i] = top().level
			if o := top().override; o != bidiON {
				class[i] = o
			}

		case bidiPDF:
			b.levels[i] = top().level
			switch {
			case overflowIsolate > 0:
			case overflowEmbedding > 0:
				overflowEmbedding--
			case !top().isolate && len(stack) >= 2:
				stack = stack[:len(stack)-1]
			}

		case bidiB:
			// X8.
			stack = stack[:1]
			overflowIsolate, overflowEmbedding, validIsolate = 0, 0, 0
			b.levels[i] = b.para

		default:
			b.levels[i] = top().level
			if o := top().override; o != bidiON && c != bidiBN {
				class[i] = o
			}
		}
	}
	for i, c := range b.orig {
		if c.removedByX9() {
			b.gone[i] = true
		}
	}
}

// runSeq is one isolating run sequence and the directions X10 bounds it
// with.
type runSeq struct {
	pos      []int
	sos, eos bidiClass
}

// sequences cuts the paragraph into the isolating run sequences the weak and
// neutral rules run on, X10.
func (b *bidiText) sequences(class []bidiClass) []runSeq {
	var keep []int
	for i := range b.text {
		if !b.gone[i] {
			keep = append(keep, i)
		}
	}
	if len(keep) == 0 {
		return nil
	}
	var runs [][]int
	for i := 0; i < len(keep); {
		j := i
		for j+1 < len(keep) && b.levels[keep[j+1]] == b.levels[keep[i]] {
			j++
		}
		runs = append(runs, keep[i:j+1])
		i = j + 1
	}
	// A run starting with a matched PDI continues a sequence, X10.
	runOf := make(map[int]int, len(runs))
	for i, r := range runs {
		runOf[r[0]] = i
	}
	used := make([]bool, len(runs))
	var out []runSeq
	for i, r := range runs {
		if used[i] {
			continue
		}
		if class[r[0]] == bidiPDI && b.matched(class, r[0]) {
			continue
		}
		var seq []int
		for j := i; ; {
			used[j] = true
			seq = append(seq, runs[j]...)
			last := runs[j][len(runs[j])-1]
			if !class[last].isolate() {
				break
			}
			at := matchingPDI(class, last)
			k, ok := runOf[at]
			if !ok || used[k] {
				break
			}
			j = k
		}
		out = append(out, b.ends(class, seq))
	}
	return out
}

// matched reports a PDI that closes an initiator.
func (b *bidiText) matched(class []bidiClass, at int) bool {
	depth := 0
	for i := at - 1; i >= 0; i-- {
		switch {
		case class[i] == bidiPDI:
			depth++
		case class[i].isolate():
			if depth == 0 {
				return true
			}
			depth--
		}
	}
	return false
}

// ends is the sos and eos of a sequence, X10.
func (b *bidiText) ends(class []bidiClass, seq []int) runSeq {
	level := b.levels[seq[0]]
	before := b.para
	for i := seq[0] - 1; i >= 0; i-- {
		if !b.gone[i] {
			before = b.levels[i]
			break
		}
	}
	last := seq[len(seq)-1]
	after := b.para
	// An isolate nothing closes takes the level of the paragraph.
	if !(class[last].isolate() && matchingPDI(class, last) == len(class)) {
		for i := last + 1; i < len(b.text); i++ {
			if !b.gone[i] {
				after = b.levels[i]
				break
			}
		}
	}
	return runSeq{pos: seq, sos: dirOf(max(level, before)), eos: dirOf(max(b.levels[last], after))}
}

// dirOf is the direction a level runs in.
func dirOf(level byte) bidiClass {
	if level&1 != 0 {
		return bidiR
	}
	return bidiL
}

// weak resolves the weak classes of one sequence, W1 to W7.
func (b *bidiText) weak(class []bidiClass, seq runSeq) {
	p := seq.pos
	// W1.
	prev := seq.sos
	for _, i := range p {
		if class[i] == bidiNSM {
			if prev.isolate() || prev == bidiPDI {
				class[i] = bidiON
			} else {
				class[i] = prev
			}
		}
		prev = class[i]
	}
	// W2.
	strong := seq.sos
	for _, i := range p {
		switch class[i] {
		case bidiL, bidiR, bidiAL:
			strong = class[i]
		case bidiEN:
			if strong == bidiAL {
				class[i] = bidiAN
			}
		}
	}
	// W3.
	for _, i := range p {
		if class[i] == bidiAL {
			class[i] = bidiR
		}
	}
	// W4.
	for k := 1; k+1 < len(p); k++ {
		a, c, d := class[p[k-1]], class[p[k]], class[p[k+1]]
		if a != d {
			continue
		}
		if (c == bidiES || c == bidiCS) && a == bidiEN {
			class[p[k]] = bidiEN
		}
		if c == bidiCS && a == bidiAN {
			class[p[k]] = bidiAN
		}
	}
	// W5.
	for k := 0; k < len(p); k++ {
		if class[p[k]] != bidiET {
			continue
		}
		j := k
		for j < len(p) && class[p[j]] == bidiET {
			j++
		}
		beforeEN := k > 0 && class[p[k-1]] == bidiEN
		afterEN := j < len(p) && class[p[j]] == bidiEN
		if beforeEN || afterEN {
			for m := k; m < j; m++ {
				class[p[m]] = bidiEN
			}
		}
		k = j - 1
	}
	// W6.
	for _, i := range p {
		switch class[i] {
		case bidiET, bidiES, bidiCS:
			class[i] = bidiON
		}
	}
	// W7.
	strong = seq.sos
	for _, i := range p {
		switch class[i] {
		case bidiL, bidiR:
			strong = class[i]
		case bidiEN:
			if strong == bidiL {
				class[i] = bidiL
			}
		}
	}
}

// neutral resolves the neutral classes of one sequence, N1 and N2.
func (b *bidiText) neutral(class []bidiClass, seq runSeq) {
	p := seq.pos
	e := dirOf(b.levels[p[0]])
	for k := 0; k < len(p); k++ {
		if !class[p[k]].neutralOrIsolate() {
			continue
		}
		j := k
		for j < len(p) && class[p[j]].neutralOrIsolate() {
			j++
		}
		before := seq.sos
		if k > 0 {
			before = strongDir(class[p[k-1]])
		}
		after := seq.eos
		if j < len(p) {
			after = strongDir(class[p[j]])
		}
		// N1 when the two sides agree, N2 when they do not.
		d := e
		if before == after && (before == bidiL || before == bidiR) {
			d = before
		}
		for m := k; m < j; m++ {
			class[p[m]] = d
		}
		k = j - 1
	}
}

// strongDir is the direction the neutral rules see a class as.
func strongDir(c bidiClass) bidiClass {
	switch c {
	case bidiL:
		return bidiL
	case bidiR, bidiEN, bidiAN:
		return bidiR
	}
	return c
}

// implicit raises what runs against its embedding, I1 and I2.
func (b *bidiText) implicit(class []bidiClass) {
	for i := range b.text {
		if b.gone[i] {
			continue
		}
		even := b.levels[i]&1 == 0
		switch c := class[i]; {
		case even && c == bidiR:
			b.levels[i]++
		case even && (c == bidiAN || c == bidiEN):
			b.levels[i] += 2
		case !even && (c == bidiL || c == bidiEN || c == bidiAN):
			b.levels[i]++
		}
	}
}

// line applies rule L1 to one line and returns the levels it is drawn at.
func (b *bidiText) line(from, to int) []byte {
	out := append([]byte(nil), b.levels[from:to]...)
	reset := true
	for i := to - 1; i >= from; i-- {
		switch c := b.orig[i]; {
		case c == bidiS || c == bidiB:
			out[i-from] = b.para
			reset = true
		case reset && (c == bidiWS || c.isolate() || c == bidiPDI):
			out[i-from] = b.para
		case reset && c.removedByX9():
		default:
			reset = false
		}
	}
	// What X9 removed is drawn at the level of what precedes it.
	last := b.para
	for i := from; i < to; i++ {
		if b.gone[i] {
			out[i-from] = last
			continue
		}
		last = out[i-from]
	}
	return out
}

// bidiOrder is the order a line is drawn in, left to right, L2.
func bidiOrder(levels []byte) []int {
	order := make([]int, len(levels))
	for i := range order {
		order[i] = i
	}
	if len(levels) == 0 {
		return order
	}
	high, lowOdd := byte(0), byte(bidiMaxDepth+2)
	for _, l := range levels {
		high = max(high, l)
		if l&1 != 0 {
			lowOdd = min(lowOdd, l)
		}
	}
	for l := high; l >= lowOdd && l > 0; l-- {
		for i := 0; i < len(levels); i++ {
			if levels[i] < l {
				continue
			}
			j := i
			for j+1 < len(levels) && levels[j+1] >= l {
				j++
			}
			for a, z := i, j; a < z; a, z = a+1, z-1 {
				order[a], order[z] = order[z], order[a]
			}
			i = j
		}
	}
	return order
}

// bidiMirror is what rule L4 draws in place of a character, or the character
// itself.
func bidiMirror(r rune) rune {
	lo, hi := 0, len(bidiMirrored)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if bidiMirrored[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(bidiMirrored) && bidiMirrored[lo].r == r {
		return bidiMirrored[lo].to
	}
	return r
}

// bracketOf is the other half of a pair and which half this one is, BD14.
func bracketOf(r rune) (rune, bool, bool) {
	lo, hi := 0, len(bidiBrackets)
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if bidiBrackets[mid].r < r {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < len(bidiBrackets) && bidiBrackets[lo].r == r {
		return bidiBrackets[lo].to, bidiBrackets[lo].open, true
	}
	return 0, false, false
}

// canonBracket folds the two brackets BD16 matches through.
func canonBracket(r rune) rune {
	switch r {
	case 0x3008:
		return 0x2329
	case 0x3009:
		return 0x232a
	}
	return r
}

// maxBracketDepth is what BD16 tracks before it gives up.
const maxBracketDepth = 63

// brackets resolves the bracket pairs of one sequence, N0.
func (b *bidiText) brackets(class []bidiClass, seq runSeq) {
	p := seq.pos
	type open struct {
		want rune
		at   int
	}
	var stack []open
	var pairs [][2]int
	for k, i := range p {
		if class[i] != bidiON {
			continue
		}
		to, isOpen, ok := bracketOf(b.text[i])
		if !ok {
			continue
		}
		if isOpen {
			if len(stack) == maxBracketDepth {
				return
			}
			stack = append(stack, open{canonBracket(to), k})
			continue
		}
		for j := len(stack) - 1; j >= 0; j-- {
			if stack[j].want != canonBracket(b.text[i]) {
				continue
			}
			pairs = append(pairs, [2]int{stack[j].at, k})
			stack = stack[:j]
			break
		}
	}
	slices.SortFunc(pairs, func(a, c [2]int) int { return a[0] - c[0] })

	e := dirOf(b.levels[p[0]])
	o := bidiL
	if e == bidiL {
		o = bidiR
	}
	for _, pr := range pairs {
		found := bidiON
		for k := pr[0] + 1; k < pr[1]; k++ {
			d := strongDir(class[p[k]])
			if d != bidiL && d != bidiR {
				continue
			}
			if d == e {
				found = e
				break
			}
			found = o
		}
		switch found {
		case bidiON:
			continue
		case o:
			// N0 c: what precedes the pair settles it.
			prev := seq.sos
			for k := pr[0] - 1; k >= 0; k-- {
				if d := strongDir(class[p[k]]); d == bidiL || d == bidiR {
					prev = d
					break
				}
			}
			if prev != o {
				found = e
			}
		}
		class[p[pr[0]]] = found
		class[p[pr[1]]] = found
		// A mark that followed a bracket before W1 follows it now.
		for _, at := range pr {
			for k := at + 1; k < len(p) && b.orig[p[k]] == bidiNSM; k++ {
				class[p[k]] = found
			}
		}
	}
}
