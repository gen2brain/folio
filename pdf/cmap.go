package pdf

import (
	"cmp"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/gen2brain/pdf/syntax"
)

// codespace is one range of byte codes of a given length.
type codespace struct {
	nbytes    int
	low, high uint32
}

// cidRange maps a run of codes to a run of CIDs.
type cidRange struct {
	low, high uint32
	cid       uint32
}

// CMap maps byte codes to CIDs, ISO 32000-1 9.7.5. The same syntax is used by
// a ToUnicode CMap, where the values are Unicode rather than CIDs.
type CMap struct {
	Name   string
	WMode  int
	spaces []codespace
	ranges []cidRange
	single map[uint32]uint32
	// text holds the destinations of a ToUnicode CMap, which map one code to
	// a string rather than to a number: a ligature is one code and several
	// characters.
	text     map[uint32]string
	usecmap  *CMap
	identity bool
	entries  int
	// sorted reports that ranges is ordered by low and no two overlap, which
	// is what lets a lookup bisect them.
	sorted bool
}

// identityCMap is Identity-H and Identity-V: two byte codes, CID equal to the
// code, which is what most files use.
func identityCMap(wmode int) *CMap {
	return &CMap{
		Name:     "Identity",
		WMode:    wmode,
		identity: true,
		spaces:   []codespace{{nbytes: 2, low: 0, high: 0xffff}},
	}
}

// Next reads the code at the start of s and returns it, the number of bytes it
// used, and the CID it maps to.
func (c *CMap) Next(s []byte) (code uint32, n int, cid uint32) {
	if len(s) == 0 {
		return 0, 0, 0
	}
	best := 0
	var v uint32
	for n := 1; n <= 4 && n <= len(s); n++ {
		v = v<<8 | uint32(s[n-1])
		for _, sp := range c.spaces {
			if sp.nbytes == n && v >= sp.low && v <= sp.high {
				best = n
				goto found
			}
		}
	}
found:
	if best == 0 {
		best = c.defaultLen()
		if best > len(s) {
			best = len(s)
		}
	}
	code = 0
	for i := 0; i < best; i++ {
		code = code<<8 | uint32(s[i])
	}
	return code, best, c.Lookup(code)
}

func (c *CMap) defaultLen() int {
	if len(c.spaces) > 0 {
		return c.spaces[0].nbytes
	}
	if c.identity {
		return 2
	}
	return 1
}

// Lookup returns the CID a code maps to.
func (c *CMap) Lookup(code uint32) uint32 {
	if c.identity {
		return code
	}
	if v, ok := c.single[code]; ok {
		return v
	}
	if c.sorted {
		i, j := 0, len(c.ranges)
		for i < j {
			h := (i + j) / 2
			if c.ranges[h].high < code {
				i = h + 1
			} else {
				j = h
			}
		}
		if i < len(c.ranges) && code >= c.ranges[i].low {
			r := c.ranges[i]
			return r.cid + (code - r.low)
		}
	} else {
		for _, r := range c.ranges {
			if code >= r.low && code <= r.high {
				return r.cid + (code - r.low)
			}
		}
	}
	if c.usecmap != nil {
		return c.usecmap.Lookup(code)
	}
	return 0
}

// parseCMap reads an embedded CMap stream.
func (d *Document) parseCMap(data []byte, depth int) *CMap {
	c := &CMap{single: map[uint32]uint32{}}
	l := syntax.NewLexer(data)
	p := syntax.NewParser(l, nil)
	p.AllowStreams(false)

	var stack []Object
	var section syntax.Keyword
	for {
		obj, ok := p.Object()
		if !ok {
			break
		}
		kw, isKw := obj.(syntax.Keyword)
		if !isKw {
			if len(stack) < 64 {
				stack = append(stack, obj)
			}
			if n := cmapArity(section); n > 0 && len(stack) >= n {
				c.addEntry(section, stack)
				stack = stack[:0]
			}
			continue
		}
		switch kw {
		case "usecmap":
			if len(stack) > 0 && depth < maxNesting {
				if n, ok := stack[len(stack)-1].(Name); ok {
					c.usecmap = d.predefinedCMap(n, depth+1)
				}
			}
		case "def":
			if len(stack) >= 2 {
				if n, ok := stack[len(stack)-2].(Name); ok && n == "WMode" {
					c.WMode = int(d.f.GetInt(stack[len(stack)-1], 0))
				}
			}
		}
		if cmapArity(kw) > 0 {
			section = kw
		} else {
			section = ""
		}
		stack = stack[:0]
	}
	if len(c.spaces) == 0 {
		c.spaces = append(c.spaces, codespace{nbytes: 2, low: 0, high: 0xffff})
	}
	c.sortRanges()
	return c
}

// cmapArity is how many operands one entry of a CMap section takes, and zero
// for a keyword that opens no section.
func cmapArity(kw syntax.Keyword) int {
	switch kw {
	case "begincodespacerange", "begincidchar", "beginbfchar":
		return 2
	case "begincidrange", "beginbfrange":
		return 3
	}
	return 0
}

// maxCMapEntries bounds what one CMap may record, over all its sections,
// counting a range that expands to one destination per code as its length.
const maxCMapEntries = 1 << 20

// addEntry records one entry of a CMap section from its operands.
func (c *CMap) addEntry(section syntax.Keyword, ops []Object) {
	if c.entries >= maxCMapEntries {
		return
	}
	c.entries++
	switch section {
	case "begincodespacerange":
		lo, ok1 := ops[0].(String)
		hi, ok2 := ops[1].(String)
		if ok1 && ok2 && len(lo) > 0 && len(lo) <= 4 {
			c.spaces = append(c.spaces, codespace{
				nbytes: len(lo), low: beCode(lo), high: beCode(hi),
			})
		}
	case "begincidrange", "beginbfrange":
		lo, ok1 := ops[0].(String)
		hi, ok2 := ops[1].(String)
		if !ok1 || !ok2 {
			return
		}
		switch v := ops[2].(type) {
		case Integer:
			c.ranges = append(c.ranges, cidRange{beCode(lo), beCode(hi), uint32(v)})
		case String:
			if len(v) > 2 {
				if beCode(hi) > beCode(lo) {
					c.entries += int(beCode(hi) - beCode(lo))
				}
				c.addTextRange(beCode(lo), beCode(hi), v)
			} else {
				c.ranges = append(c.ranges, cidRange{beCode(lo), beCode(hi), beCode(v)})
			}
		case Array:
			base := beCode(lo)
			c.entries += len(v)
			for j, e := range v {
				if s, ok := e.(String); ok {
					c.setText(base+uint32(j), s)
				}
			}
		}
	case "begincidchar", "beginbfchar":
		code, ok := ops[0].(String)
		if !ok {
			return
		}
		switch v := ops[1].(type) {
		case Integer:
			c.single[beCode(code)] = uint32(v)
		case String:
			c.setText(beCode(code), v)
		}
	}
}

// sortRanges orders the ranges so that a lookup may bisect them, which it can
// only do when none of them overlap: where two do, the first written wins and
// only a scan in that order finds it.
func (c *CMap) sortRanges() {
	r := slices.Clone(c.ranges)
	slices.SortFunc(r, func(a, b cidRange) int { return cmp.Compare(a.low, b.low) })
	for i := range r {
		if r[i].high < r[i].low || (i > 0 && r[i].low <= r[i-1].high) {
			return
		}
	}
	c.ranges, c.sorted = r, true
}

// setText records a ToUnicode destination, which is UTF-16BE.
func (c *CMap) setText(code uint32, s String) {
	if len(s) <= 2 {
		c.single[code] = beCode(s)
		return
	}
	if c.text == nil {
		c.text = map[uint32]string{}
	}
	c.text[code] = utf16BE(s)
}

// addTextRange records a range whose destination string increments in its
// last unit.
func (c *CMap) addTextRange(lo, hi uint32, s String) {
	if hi < lo || hi-lo > 1<<16 {
		return
	}
	base := utf16BE(s)
	r := []rune(base)
	if len(r) == 0 {
		return
	}
	for code := lo; code <= hi; code++ {
		r[len(r)-1] = []rune(base)[len(r)-1] + rune(code-lo)
		c.setTextString(code, string(r))
	}
}

func (c *CMap) setTextString(code uint32, s string) {
	if c.text == nil {
		c.text = map[uint32]string{}
	}
	c.text[code] = s
}

// utf16BE decodes the big endian UTF-16 a ToUnicode destination is written in.
func utf16BE(s String) string {
	var units []uint16
	for i := 0; i+1 < len(s); i += 2 {
		units = append(units, uint16(s[i])<<8|uint16(s[i+1]))
	}
	if len(s)%2 == 1 {
		units = append(units, uint16(s[len(s)-1]))
	}
	return string(utf16Decode(units))
}

// utf16Decode turns UTF-16 units into runes, pairing surrogates.
func utf16Decode(units []uint16) []rune {
	out := make([]rune, 0, len(units))
	for i := 0; i < len(units); i++ {
		u := units[i]
		if u >= 0xd800 && u < 0xdc00 && i+1 < len(units) {
			if v := units[i+1]; v >= 0xdc00 && v < 0xe000 {
				out = append(out, rune(u-0xd800)<<10|rune(v-0xdc00)+0x10000)
				i++
				continue
			}
		}
		out = append(out, rune(u))
	}
	return out
}

// Text returns the string a ToUnicode CMap maps a code to.
func (c *CMap) Text(code uint32) string {
	if c == nil {
		return ""
	}
	if s, ok := c.text[code]; ok {
		return s
	}
	if v := c.Lookup(code); v != 0 {
		return string(rune(v))
	}
	if c.usecmap != nil {
		return c.usecmap.Text(code)
	}
	return ""
}

func beCode(s String) uint32 {
	var v uint32
	for i := 0; i < len(s) && i < 4; i++ {
		v = v<<8 | uint32(s[i])
	}
	return v
}

// predefinedCMap resolves one of the CMaps named in ISO 32000-1 table 118.
// The byte encodings are built in; the Unicode ones are not, and a two byte
// code is the better guess for those.
func (d *Document) predefinedCMap(name Name, depth int) *CMap {
	switch name {
	case "Identity-H":
		return identityCMap(0)
	case "Identity-V":
		return identityCMap(1)
	}
	if c, ok := builtinCMap(string(name)); ok {
		return c
	}
	wmode := 0
	if len(name) > 2 && name[len(name)-2:] == "-V" {
		wmode = 1
	}
	d.errorf("predefined CMap /%s is not built in; assuming two byte codes", name)
	c := identityCMap(wmode)
	c.Name = string(name)
	return c
}

var (
	builtinMu    sync.Mutex
	builtinCache = map[string]*CMap{}
)

// builtinCMap parses one of the generated tables. The parsed form is kept for
// the life of the process, because the predefined CMaps are the same for every
// file and several may be open at once.
func builtinCMap(name string) (*CMap, bool) {
	builtinMu.Lock()
	defer builtinMu.Unlock()
	if c, ok := builtinCache[name]; ok {
		return c, c != nil
	}
	src, ok := builtinCMaps[name]
	if !ok {
		builtinCache[name] = nil
		return nil, false
	}
	c := &CMap{Name: name, single: map[uint32]uint32{}}
	for _, line := range strings.Split(src, "\n") {
		f := strings.Fields(line)
		if len(f) == 0 {
			continue
		}
		switch f[0] {
		case "w1":
			c.WMode = 1
		case "s":
			if len(f) == 4 {
				n, _ := strconv.Atoi(f[1])
				c.spaces = append(c.spaces, codespace{
					nbytes: n, low: hexCode(f[2]), high: hexCode(f[3]),
				})
			}
		case "r":
			if len(f) == 4 {
				cid, _ := strconv.ParseUint(f[3], 10, 32)
				c.ranges = append(c.ranges, cidRange{hexCode(f[1]), hexCode(f[2]), uint32(cid)})
			}
		case "c":
			if len(f) == 3 {
				cid, _ := strconv.ParseUint(f[2], 10, 32)
				c.single[hexCode(f[1])] = uint32(cid)
			}
		}
	}
	if len(c.spaces) == 0 {
		c.spaces = append(c.spaces, codespace{nbytes: 2, low: 0, high: 0xffff})
	}
	c.sortRanges()
	builtinCache[name] = c
	return c, true
}

func hexCode(s string) uint32 {
	v, _ := strconv.ParseUint(s, 16, 32)
	return uint32(v)
}
