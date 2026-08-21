package html

import "unicode/utf8"

// lbClass is a line break class of UAX #14, as rule LB1 leaves it: AI, SG and
// XX are resolved to AL, SA to CM or AL, and CJ to NS by the generator.
type lbClass uint8

// The classes, in the order tools/gentables writes the table in.
const (
	lbXX lbClass = iota
	lbAL
	lbAK
	lbAP
	lbAS
	lbB2
	lbBA
	lbBB
	lbBK
	lbCB
	lbCL
	lbCM
	lbCP
	lbCR
	lbEB
	lbEM
	lbEX
	lbGL
	lbH2
	lbH3
	lbHH
	lbHL
	lbHY
	lbID
	lbIN
	lbIS
	lbJL
	lbJT
	lbJV
	lbLF
	lbNL
	lbNS
	lbNU
	lbOP
	lbPO
	lbPR
	lbQU
	lbRI
	lbSP
	lbSY
	lbVF
	lbVI
	lbWJ
	lbZW
	lbZWJ
)

// The properties a table entry carries above the class.
const (
	lbClassMask = 0x3f
	lbIsPi      = 1 << 6
	lbIsPf      = 1 << 7
	lbIsEast    = 1 << 8
	lbIsPictUn  = 1 << 9
	// lbIsDotted marks U+25CC DOTTED CIRCLE, which rule LB28a names on its
	// own.
	lbIsDotted = 1 << 10
)

// lbLookup returns the packed class and properties of a code point.
func lbLookup(r rune) uint16 {
	lo, hi := 0, len(lbTable)-1
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if r > lbTable[m].hi {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lbTable[lo].val
}

// The three things a break position can be.
const (
	lbProhibited = iota
	lbAllowed
	lbMandatory
)

// lineBreak is one position a line may break at.
type lineBreak struct {
	// pos is the byte offset in the text.
	pos int
	// mandatory is a break the text asks for rather than allows.
	mandatory bool
}

// lbText is the text of one run with everything the rules ask about it.
type lbText struct {
	// c is the class of each character rule LB9 did not absorb, at is where
	// it starts in the text, and val its properties.
	c   []lbClass
	at  []int
	val []uint16
	// zwj marks a position the original text has a zero width joiner before.
	zwj []bool
	m   int
}

func (t *lbText) pi(k int) bool   { return k >= 0 && k < t.m && t.val[k]&lbIsPi != 0 }
func (t *lbText) pf(k int) bool   { return k >= 0 && k < t.m && t.val[k]&lbIsPf != 0 }
func (t *lbText) east(k int) bool { return k >= 0 && k < t.m && t.val[k]&lbIsEast != 0 }
func (t *lbText) pict(k int) bool { return k >= 0 && k < t.m && t.val[k]&lbIsPictUn != 0 }
func (t *lbText) dot(k int) bool  { return k >= 0 && k < t.m && t.val[k]&lbIsDotted != 0 }

// lineBreaks returns every position a line may break at in s, by the rules of
// UAX #14. The end of the text is always the last of them.
func lineBreaks(s string) []lineBreak {
	t := resolve(s)
	if t.m == 0 {
		if len(s) == 0 {
			return nil
		}
		return []lineBreak{{pos: len(s), mandatory: true}}
	}
	out := make([]lineBreak, 0, 8+len(s)/8)
	for k := 1; k < t.m; k++ {
		switch t.action(k) {
		case lbAllowed:
			out = append(out, lineBreak{pos: t.at[k]})
		case lbMandatory:
			out = append(out, lineBreak{pos: t.at[k], mandatory: true})
		}
	}
	return append(out, lineBreak{pos: len(s), mandatory: true})
}

// resolve decodes the text and applies rules LB9 and LB10: a combining mark
// after a base takes the base's class and is dropped from the sequence, and
// one that follows nothing takes the class of a capital A.
func resolve(s string) *lbText {
	n := utf8.RuneCountInString(s)
	t := &lbText{
		c:   make([]lbClass, 0, n),
		at:  make([]int, 0, n),
		val: make([]uint16, 0, n),
		zwj: make([]bool, 0, n),
	}
	var prev lbClass = lbXX
	first := true
	wasZWJ := false
	for i, r := range s {
		v := lbLookup(r)
		k := lbClass(v & lbClassMask)
		joiner := k == lbZWJ
		if (k == lbCM || joiner) && !first {
			switch prev {
			case lbBK, lbCR, lbLF, lbNL, lbSP, lbZW:
			default:
				wasZWJ = joiner
				continue
			}
		}
		if k == lbCM || joiner {
			k, v = lbAL, 0
		}
		t.c = append(t.c, k)
		t.at = append(t.at, i)
		t.val = append(t.val, v)
		t.zwj = append(t.zwj, wasZWJ)
		prev, first, wasZWJ = k, false, joiner
	}
	t.m = len(t.c)
	return t
}

// skipSP walks back over the spaces rules LB8, LB14, LB15a, LB16 and LB17
// look through.
func (t *lbText) skipSP(k int) int {
	for k >= 0 && t.c[k] == lbSP {
		k--
	}
	return k
}

// numBack reports whether walking back over the infix separators of a number
// lands on a digit, which is what the several forms of rule LB25 share.
func (t *lbText) numBack(k int) bool {
	for k >= 0 && (t.c[k] == lbSY || t.c[k] == lbIS) {
		k--
	}
	return k >= 0 && t.c[k] == lbNU
}

func (t *lbText) akLike(k int) bool {
	if k < 0 || k >= t.m {
		return false
	}
	return t.c[k] == lbAK || t.c[k] == lbAS || t.dot(k)
}

func isJamo(k lbClass) bool {
	switch k {
	case lbJL, lbJV, lbJT, lbH2, lbH3:
		return true
	}
	return false
}

func isAlpha(k lbClass) bool { return k == lbAL || k == lbHL }

// action decides the break before the character at k, applying the rules of
// UAX #14 §6 in the order they are numbered.
func (t *lbText) action(k int) int {
	a, b := t.c[k-1], t.c[k]

	if a == lbBK {
		return lbMandatory
	}
	if a == lbCR && b == lbLF {
		return lbProhibited
	}
	if a == lbCR || a == lbLF || a == lbNL {
		return lbMandatory
	}
	switch b {
	case lbBK, lbCR, lbLF, lbNL, lbSP, lbZW:
		return lbProhibited
	}
	if j := t.skipSP(k - 1); j >= 0 && t.c[j] == lbZW {
		return lbAllowed
	}
	if t.zwj[k] {
		return lbProhibited
	}
	if a == lbWJ || b == lbWJ {
		return lbProhibited
	}
	if a == lbGL {
		return lbProhibited
	}
	if b == lbGL {
		switch a {
		case lbSP, lbBA, lbHY, lbHH:
		default:
			return lbProhibited
		}
	}
	switch b {
	case lbCL, lbCP, lbEX, lbSY:
		return lbProhibited
	}
	if j := t.skipSP(k - 1); j >= 0 && t.c[j] == lbOP {
		return lbProhibited
	}
	if j := t.skipSP(k - 1); j >= 0 && t.c[j] == lbQU && t.pi(j) {
		if j == 0 {
			return lbProhibited
		}
		switch t.c[j-1] {
		case lbBK, lbCR, lbLF, lbNL, lbOP, lbQU, lbGL, lbSP, lbZW:
			return lbProhibited
		}
	}
	if b == lbQU && t.pf(k) {
		if k+1 >= t.m {
			return lbProhibited
		}
		switch t.c[k+1] {
		case lbSP, lbGL, lbWJ, lbCL, lbQU, lbCP, lbEX, lbIS, lbSY, lbBK, lbCR, lbLF, lbNL, lbZW:
			return lbProhibited
		}
	}
	if a == lbSP && b == lbIS && k+1 < t.m && t.c[k+1] == lbNU {
		return lbAllowed
	}
	if b == lbIS {
		return lbProhibited
	}
	if b == lbNS {
		if j := t.skipSP(k - 1); j >= 0 && (t.c[j] == lbCL || t.c[j] == lbCP) {
			return lbProhibited
		}
	}
	if b == lbB2 {
		if j := t.skipSP(k - 1); j >= 0 && t.c[j] == lbB2 {
			return lbProhibited
		}
	}
	if a == lbSP {
		return lbAllowed
	}
	if b == lbQU && !t.pi(k) {
		return lbProhibited
	}
	if a == lbQU && !t.pf(k-1) {
		return lbProhibited
	}
	if b == lbQU && (!t.east(k-1) || k+1 >= t.m || !t.east(k+1)) {
		return lbProhibited
	}
	if a == lbQU && (!t.east(k) || k-2 < 0 || !t.east(k-2)) {
		return lbProhibited
	}
	if a == lbCB || b == lbCB {
		return lbAllowed
	}
	if (a == lbHY || a == lbHH) && isAlpha(b) {
		if k < 2 {
			return lbProhibited
		}
		switch t.c[k-2] {
		case lbBK, lbCR, lbLF, lbNL, lbSP, lbZW, lbCB, lbGL:
			return lbProhibited
		}
	}
	switch b {
	case lbBA, lbHH, lbHY, lbNS:
		return lbProhibited
	}
	if a == lbBB {
		return lbProhibited
	}
	if k >= 2 && t.c[k-2] == lbHL && (a == lbHY || a == lbHH) && b != lbHL {
		return lbProhibited
	}
	if a == lbSY && b == lbHL {
		return lbProhibited
	}
	if b == lbIN {
		return lbProhibited
	}
	if isAlpha(a) && b == lbNU || a == lbNU && isAlpha(b) {
		return lbProhibited
	}
	if a == lbPR && (b == lbID || b == lbEB || b == lbEM) {
		return lbProhibited
	}
	if (a == lbID || a == lbEB || a == lbEM) && b == lbPO {
		return lbProhibited
	}
	if (a == lbPR || a == lbPO) && isAlpha(b) || isAlpha(a) && (b == lbPR || b == lbPO) {
		return lbProhibited
	}
	if t.number(k, a, b) {
		return lbProhibited
	}
	if a == lbJL && (b == lbJL || b == lbJV || b == lbH2 || b == lbH3) {
		return lbProhibited
	}
	if (a == lbJV || a == lbH2) && (b == lbJV || b == lbJT) {
		return lbProhibited
	}
	if (a == lbJT || a == lbH3) && b == lbJT {
		return lbProhibited
	}
	if isJamo(a) && b == lbPO || a == lbPR && isJamo(b) {
		return lbProhibited
	}
	if isAlpha(a) && isAlpha(b) {
		return lbProhibited
	}
	if t.brahmic(k, a, b) {
		return lbProhibited
	}
	if a == lbIS && isAlpha(b) {
		return lbProhibited
	}
	if (isAlpha(a) || a == lbNU) && b == lbOP && !t.east(k) {
		return lbProhibited
	}
	if a == lbCP && !t.east(k-1) && (isAlpha(b) || b == lbNU) {
		return lbProhibited
	}
	if a == lbRI && b == lbRI {
		n := 0
		for j := k - 1; j >= 0 && t.c[j] == lbRI; j-- {
			n++
		}
		if n%2 == 1 {
			return lbProhibited
		}
	}
	if b == lbEM && (a == lbEB || t.pict(k-1)) {
		return lbProhibited
	}
	return lbAllowed
}

// number is rule LB25, which holds a number and the prefixes and postfixes
// around it together.
func (t *lbText) number(k int, a, b lbClass) bool {
	switch {
	case (a == lbCL || a == lbCP) && (b == lbPO || b == lbPR) && t.numBack(k-2):
		return true
	case (b == lbPO || b == lbPR || b == lbNU) && t.numBack(k-1):
		return true
	case (a == lbPO || a == lbPR) && b == lbOP:
		if k+1 < t.m && t.c[k+1] == lbNU {
			return true
		}
		return k+2 < t.m && t.c[k+1] == lbIS && t.c[k+2] == lbNU
	case (a == lbPO || a == lbPR || a == lbHY || a == lbIS) && b == lbNU:
		return true
	}
	return false
}

// brahmic is rule LB28a, which holds an orthographic syllable together.
func (t *lbText) brahmic(k int, a, b lbClass) bool {
	akb := b == lbAK || b == lbAS || t.dot(k)
	switch {
	case a == lbAP && akb:
		return true
	case t.akLike(k-1) && (b == lbVF || b == lbVI):
		return true
	case a == lbVI && t.akLike(k-2) && (b == lbAK || t.dot(k)):
		return true
	case t.akLike(k-1) && akb && k+1 < t.m && t.c[k+1] == lbVF:
		return true
	}
	return false
}
