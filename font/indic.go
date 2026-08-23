package font

import "math/bits"

// The categories the syllable machine runs on, in the tables' order.
const (
	inX = iota
	inC
	inV
	inN
	inH
	inZWNJ
	inZWJ
	inM
	inSM
	inA
	inPlaceholder
	inDottedCircle
	inRS
	inMPst
	inRepha
	inRa
	inCM
	inSymbol
	inCS
)

// The places a character of a syllable ends up in, which is the order the
// reordering sorts them into.
const (
	posStart = iota
	posRaToReph
	posPreM
	posPreC
	posBaseC
	posAfterMain
	posAboveC
	posBeforeSub
	posBelowC
	posAfterSub
	posBeforePost
	posPostC
	posAfterPost
	posSMVD
	posEnd
)

// indicOf is what a character of an Indic script is and where it sits.
func indicOf(r rune) (uint8, uint8) {
	lo, hi := 0, len(indicTable)-1
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		if r > indicTable[mid].hi {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	v := indicTable[lo].val
	return uint8(v >> 8), uint8(v)
}

func isConsonant(cat uint8) bool {
	switch cat {
	case inC, inRa, inCM, inV, inPlaceholder, inDottedCircle:
		return true
	}
	return false
}

func isJoiner(cat uint8) bool { return cat == inZWJ || cat == inZWNJ }

// The kinds of syllable the machine finds.
const (
	sylConsonant = iota
	sylVowel
	sylStandalone
	sylSymbol
	sylBroken
	sylOther
)

// sylMatch walks the categories of a run and reports where a production may
// end. The positions are a bitmask from base, so matching allocates nothing.
type sylMatch func(cat []uint8, base int, in uint64) uint64

// sylReach bounds how far ahead a syllable may run, which is the width of the
// mask the matchers carry.
const sylReach = 63

func sylOne(want ...uint8) sylMatch {
	return func(cat []uint8, base int, in uint64) uint64 {
		out := uint64(0)
		for m := in; m != 0; m &= m - 1 {
			k := bits.TrailingZeros64(m)
			if base+k >= len(cat) || k >= sylReach {
				continue
			}
			for _, w := range want {
				if cat[base+k] == w {
					out |= 1 << uint(k+1)
					break
				}
			}
		}
		return out
	}
}

func sylSeq(ms ...sylMatch) sylMatch {
	return func(cat []uint8, base int, in uint64) uint64 {
		for _, m := range ms {
			if in = m(cat, base, in); in == 0 {
				return 0
			}
		}
		return in
	}
}

func sylAlt(ms ...sylMatch) sylMatch {
	return func(cat []uint8, base int, in uint64) uint64 {
		out := uint64(0)
		for _, m := range ms {
			out |= m(cat, base, in)
		}
		return out
	}
}

func sylOpt(m sylMatch) sylMatch {
	return func(cat []uint8, base int, in uint64) uint64 {
		return in | m(cat, base, in)
	}
}

func sylStar(m sylMatch) sylMatch {
	return func(cat []uint8, base int, in uint64) uint64 {
		out := in
		for {
			next := out | m(cat, base, out)
			if next == out {
				return out
			}
			out = next
		}
	}
}

// The grammar of a syllable, which is the one the reference shaper uses.
var (
	sylC     = sylOne(inC, inRa)
	sylZ     = sylOne(inZWJ, inZWNJ)
	sylN     = sylSeq(sylOpt(sylSeq(sylOpt(sylOne(inZWNJ)), sylOne(inRS))), sylOpt(sylSeq(sylOne(inN), sylOpt(sylOne(inN)))))
	sylCN    = sylSeq(sylC, sylOpt(sylOne(inZWJ)), sylOpt(sylN))
	sylReph  = sylAlt(sylSeq(sylOne(inRa), sylOne(inH)), sylOne(inRepha))
	sylMatra = sylSeq(sylStar(sylZ), sylAlt(sylOne(inM), sylSeq(sylOpt(sylOne(inSM)), sylOne(inMPst))), sylOpt(sylOne(inN)), sylOpt(sylOne(inH)))
	sylTail  = sylSeq(sylOpt(sylSeq(sylOpt(sylZ), sylOne(inSM), sylOpt(sylOne(inSM)), sylOpt(sylOne(inZWNJ)))), sylStar(sylOne(inA)))
	sylHal   = sylSeq(sylOpt(sylZ), sylOne(inH), sylOpt(sylSeq(sylOne(inZWJ), sylOpt(sylOne(inN)))))
	sylFinal = sylAlt(sylHal, sylSeq(sylOne(inH), sylOne(inZWNJ)))
	sylMed   = sylOpt(sylOne(inCM))
	sylHalM  = sylAlt(sylFinal, sylStar(sylMatra))
	sylCtail = sylSeq(sylStar(sylSeq(sylHal, sylCN)), sylMed, sylHalM, sylTail)

	sylKinds = []struct {
		kind  int
		match sylMatch
	}{
		{sylConsonant, sylSeq(sylOpt(sylAlt(sylOne(inRepha), sylOne(inCS))), sylCN, sylCtail)},
		{sylVowel, sylSeq(sylOpt(sylReph), sylOne(inV), sylOpt(sylN), sylAlt(sylOne(inZWJ), sylCtail))},
		{sylStandalone, sylSeq(sylAlt(
			sylSeq(sylOpt(sylAlt(sylOne(inRepha), sylOne(inCS))), sylOne(inPlaceholder)),
			sylSeq(sylOpt(sylReph), sylOne(inDottedCircle))), sylOpt(sylN), sylCtail)},
		{sylSymbol, sylSeq(sylOne(inSymbol), sylOpt(sylOne(inN)), sylTail)},
		{sylBroken, sylSeq(sylOpt(sylReph), sylOpt(sylN), sylCtail)},
	}
)

// syllables cuts a run into the syllables the reordering works on, taking the
// longest match at each place and the first kind that gives it.
func syllables(cat []uint8) [][3]int {
	var out [][3]int
	for at := 0; at < len(cat); {
		best, kind := 0, sylOther
		for _, k := range sylKinds {
			if e := k.match(cat, at, 1); e != 0 {
				if n := 63 - bits.LeadingZeros64(e); n > best {
					best, kind = n, k.kind
				}
			}
		}
		end := at + best
		if best == 0 {
			end, kind = at+1, sylOther
		}
		out = append(out, [3]int{at, end, kind})
		at = end
	}
	return out
}

// The features of an Indic script, in the order they run. The first group is
// applied to the glyphs one place asks for, the rest to the whole syllable.
var indicFormFeatures = []string{"nukt", "akhn", "rphf", "rkrf", "pref", "blwf", "abvf", "half", "pstf", "vatu", "cjct"}

var indicFinalFeatures = []string{"init", "pres", "abvs", "blws", "psts", "haln"}

// indicMask is the bit a form feature is applied through, which is set on the
// glyphs whose place in the syllable calls for it.
func indicMask(name string) uint32 {
	for i, f := range indicFormFeatures {
		if f == name {
			return 1 << uint(i+8)
		}
	}
	return 1
}

// indicShape reorders each syllable of a run and marks which of the form
// features may touch each glyph.
func (b *buffer) indicShape(runes []rune) {
	cats := make([]uint8, len(runes))
	poss := make([]uint8, len(runes))
	for i, r := range runes {
		cats[i], poss[i] = indicOf(r)
	}
	for n, s := range syllables(cats) {
		b.indicSyllable(cats, poss, s[0], s[1], s[2], n+1)
	}
}

// indicSyllable finds the base consonant of one syllable, gives everything
// else a place relative to it and sorts the glyphs into that order.
func (b *buffer) indicSyllable(cats, poss []uint8, start, end, kind, num int) {
	if end-start < 1 {
		return
	}
	for i := start; i < end; i++ {
		b.items[i].syl = num
	}
	if kind == sylOther || kind == sylSymbol {
		return
	}
	limit := start
	reph := false
	// A syllable that opens with Ra and a halant, and has more after it,
	// puts that Ra above the rest as a reph.
	if end-start >= 3 && cats[start] == inRa && cats[start+1] == inH && !isJoiner(cats[start+2]) {
		limit = start + 2
		for limit < end && isJoiner(cats[limit]) {
			limit++
		}
		reph = true
	}

	// The base is the last consonant not written below or after the previous.
	base := end
	seenBelow := false
	for i := end - 1; i >= limit; i-- {
		if isConsonant(cats[i]) {
			if poss[i] != posBelowC && (poss[i] != posPostC || seenBelow) {
				base = i
				break
			}
			if poss[i] == posBelowC {
				seenBelow = true
			}
			base = i
			continue
		}
		if i > start && cats[i] == inZWJ && cats[i-1] == inH {
			break
		}
	}
	if base == end {
		base = limit
		if base >= end {
			return
		}
	}

	// Everything before the base is written before it, and the base stands.
	place := make([]uint8, end-start)
	for i := start; i < end; i++ {
		place[i-start] = poss[i]
	}
	for i := start; i < base; i++ {
		place[i-start] = min(posPreC, poss[i])
	}
	place[base-start] = posBaseC
	// A halant after the base belongs with what it follows.
	for i := base + 1; i < end; i++ {
		if cats[i] == inH || cats[i] == inN || isJoiner(cats[i]) {
			place[i-start] = place[i-1-start]
		}
	}
	if reph {
		place[0] = posRaToReph
	}
	for i := start; i < end; i++ {
		b.items[i].pos = place[i-start]
	}

	// The consonants before the base take their half form, and the syllable
	// asks the font for the rest by where each glyph sits.
	for i := start; i < end; i++ {
		m := uint32(1)
		switch {
		case i == start && reph:
			m |= indicMask("rphf")
		case i < base:
			m |= indicMask("half")
		case i > base:
			switch place[i-start] {
			case posBelowC:
				m |= indicMask("blwf")
			case posAboveC:
				m |= indicMask("abvf")
			case posPostC, posAfterPost:
				m |= indicMask("pstf")
			}
			m |= indicMask("pref")
		}
		m |= indicMask("nukt") | indicMask("akhn") | indicMask("rkrf") |
			indicMask("vatu") | indicMask("cjct")
		b.items[i].mask = m
	}

	// The glyphs are drawn in the order their places give, and two in the
	// same place keep the order they were written in.
	b.sortByPos(start, end)
}

// sortByPos puts a syllable in the order its places give, merging the
// clusters of whatever each glyph moves over.
func (b *buffer) sortByPos(start, end int) {
	for i := start + 1; i < end; i++ {
		j := i
		for j > start && b.items[j-1].pos > b.items[i].pos {
			j--
		}
		if j == i {
			continue
		}
		b.mergeClusters(j, i+1)
		moved := b.items[i]
		copy(b.items[j+1:i+1], b.items[j:i])
		b.items[j] = moved
	}
}

// mergeClusters gives a run of glyphs the earliest cluster they had, and any
// glyph either side already sharing one with the run.
func (b *buffer) mergeClusters(from, to int) {
	if to-from < 2 || from < 0 || to > len(b.items) {
		return
	}
	c := b.items[from].cluster
	for i := from; i < to; i++ {
		c = min(c, b.items[i].cluster)
	}
	for from > 0 && b.items[from-1].cluster == b.items[from].cluster {
		from--
	}
	for to < len(b.items) && b.items[to-1].cluster == b.items[to].cluster {
		to++
	}
	for i := from; i < to; i++ {
		b.items[i].cluster = c
	}
}

// indicFinal moves the reph of each syllable to where the font has made room
// for it, which is only known once the form features have run.
func (b *buffer) indicFinal() {
	for start := 0; start < len(b.items); {
		syl := b.items[start].syl
		end := start
		for end < len(b.items) && b.items[end].syl == syl {
			end++
		}
		if syl != 0 {
			b.indicReph(start, end)
		}
		start = end
	}
}

// indicReph is where the reph of one syllable ends up: after its first
// halant, and at the end of the syllable when it has none.
func (b *buffer) indicReph(start, end int) {
	// A reph that the font did not make one glyph of is left where it is:
	// the font is drawing it in place.
	if end-start < 2 || b.items[start].pos != posRaToReph || !b.items[start].ligated {
		return
	}
	base := start
	for i := start; i < end; i++ {
		if b.items[i].pos == posBaseC {
			base = i
			break
		}
	}
	at := start + 1
	for at < base && !b.isHalant(at) {
		at++
	}
	if at >= base || !b.isHalant(at) {
		// Nothing to hang it off, so it goes last, before the marks that
		// belong to the whole syllable.
		at = end - 1
		for at > start && b.items[at].pos == posSMVD {
			at--
		}
	} else if at+1 < base && b.isJoiner(at+1) {
		at++
	}
	if at <= start {
		return
	}
	b.mergeClusters(start, at+1)
	reph := b.items[start]
	copy(b.items[start:at], b.items[start+1:at+1])
	b.items[at] = reph
}

// isHalant reports a glyph written as one, which the reph hangs off.
func (b *buffer) isHalant(i int) bool {
	return b.items[i].pos == posEnd && !b.items[i].ligated
}

func (b *buffer) isJoiner(i int) bool { return b.items[i].pos == posEnd }
