package font

// skip reports a glyph a lookup passes over: what its flags ignore, and the
// marks outside the class or the set it filters on.
func (b *buffer) skip(l *otLookup, i int) bool {
	if i < 0 || i >= len(b.items) {
		return false
	}
	gid := b.items[i].gid
	switch b.f.gdef.class(gid) {
	case classBase:
		return l.flag&flagIgnoreBase != 0
	case classLigature:
		return l.flag&flagIgnoreLig != 0
	case classMark:
		if l.flag&flagIgnoreMarks != 0 {
			return true
		}
		if m := l.flag & flagMarkAttach; m != 0 && b.f.gdef.attachClass(gid) != m>>8 {
			return true
		}
		if l.flag&flagMarkFilter != 0 && !b.f.gdef.inMarkSet(l.markSet, gid) {
			return true
		}
	}
	return false
}

// next is the glyph after one that the lookup does not pass over, and the end
// of the buffer when there is none.
func (b *buffer) next(l *otLookup, i int) int {
	for k := i + 1; k < len(b.items); k++ {
		if !b.skip(l, k) {
			return k
		}
	}
	return len(b.items)
}

// prev is the glyph before one that the lookup does not pass over, and -1
// when there is none.
func (b *buffer) prev(l *otLookup, i int) int {
	for k := i - 1; k >= 0; k-- {
		if !b.skip(l, k) {
			return k
		}
	}
	return -1
}

// matcher says what a glyph of a context has to be.
type matcher func(gid int) bool

// forward matches a run of glyphs after a place, and returns how many glyphs
// of the buffer it covered.
func (b *buffer) forward(l *otLookup, at int, want []matcher) (int, bool) {
	p := at
	n := 1
	for _, m := range want {
		p = b.next(l, p)
		if p >= len(b.items) || !m(b.items[p].gid) {
			return 0, false
		}
		n = p - at + 1
	}
	return n, true
}

// backward matches a run of glyphs before a place, in the order they are
// written in the table, which is the one nearest the place first.
func (b *buffer) backward(l *otLookup, at int, want []matcher) bool {
	p := at
	for _, m := range want {
		p = b.prev(l, p)
		if p < 0 || !m(b.items[p].gid) {
			return false
		}
	}
	return true
}

// matchContext reads one context subtable and matches it at a place.
func (b *buffer) matchContext(d []byte, l *otLookup, off, at int) (int, []subRecord, bool) {
	if off+2 > len(d) {
		return 0, nil, false
	}
	switch be16(d, off) {
	case 1:
		cov := off + be16(d, off+2)
		i := coverageIndex(d, cov, b.items[at].gid)
		if i < 0 || i >= be16(d, off+4) {
			return 0, nil, false
		}
		set := off + be16(d, off+6+2*i)
		if set+2 > len(d) {
			return 0, nil, false
		}
		for k := range be16(d, set) {
			rule := set + be16(d, set+2+2*k)
			if rule+4 > len(d) {
				continue
			}
			n, nrec := be16(d, rule), be16(d, rule+2)
			if n == 0 || rule+4+2*(n-1) > len(d) {
				continue
			}
			var want []matcher
			for c := 1; c < n; c++ {
				g := be16(d, rule+4+2*(c-1))
				want = append(want, func(x int) bool { return x == g })
			}
			if got, ok := b.forward(l, at, want); ok {
				return got, readRecords(d, rule+4+2*(n-1), nrec), true
			}
		}
	case 2:
		cov := off + be16(d, off+2)
		if coverageIndex(d, cov, b.items[at].gid) < 0 {
			return 0, nil, false
		}
		cd := off + be16(d, off+4)
		i := classOf(d, cd, b.items[at].gid)
		if i >= be16(d, off+6) {
			return 0, nil, false
		}
		set := be16(d, off+8+2*i)
		if set == 0 {
			return 0, nil, false
		}
		set += off
		if set+2 > len(d) {
			return 0, nil, false
		}
		for k := range be16(d, set) {
			rule := set + be16(d, set+2+2*k)
			if rule+4 > len(d) {
				continue
			}
			n, nrec := be16(d, rule), be16(d, rule+2)
			if n == 0 || rule+4+2*(n-1) > len(d) {
				continue
			}
			var want []matcher
			for c := 1; c < n; c++ {
				cl := be16(d, rule+4+2*(c-1))
				want = append(want, func(x int) bool { return classOf(d, cd, x) == cl })
			}
			if got, ok := b.forward(l, at, want); ok {
				return got, readRecords(d, rule+4+2*(n-1), nrec), true
			}
		}
	case 3:
		n, nrec := be16(d, off+2), be16(d, off+4)
		if n == 0 || off+6+2*n > len(d) {
			return 0, nil, false
		}
		if coverageIndex(d, off+be16(d, off+6), b.items[at].gid) < 0 {
			return 0, nil, false
		}
		var want []matcher
		for c := 1; c < n; c++ {
			cv := off + be16(d, off+6+2*c)
			want = append(want, func(x int) bool { return coverageIndex(d, cv, x) >= 0 })
		}
		if got, ok := b.forward(l, at, want); ok {
			return got, readRecords(d, off+6+2*n, nrec), true
		}
	}
	return 0, nil, false
}

// matchChain reads one chaining context subtable and matches it at a place:
// what has to come before, what the run itself is, and what has to follow.
func (b *buffer) matchChain(d []byte, l *otLookup, off, at int) (int, []subRecord, bool) {
	if off+2 > len(d) {
		return 0, nil, false
	}
	switch be16(d, off) {
	case 1, 2:
		format := be16(d, off)
		cov := off + be16(d, off+2)
		if coverageIndex(d, cov, b.items[at].gid) < 0 {
			return 0, nil, false
		}
		var back, input, ahead, setOff int
		if format == 1 {
			i := coverageIndex(d, cov, b.items[at].gid)
			if i >= be16(d, off+4) {
				return 0, nil, false
			}
			setOff = off + be16(d, off+6+2*i)
		} else {
			back, input, ahead = off+be16(d, off+4), off+be16(d, off+6), off+be16(d, off+8)
			i := classOf(d, input, b.items[at].gid)
			if i >= be16(d, off+10) {
				return 0, nil, false
			}
			v := be16(d, off+12+2*i)
			if v == 0 {
				return 0, nil, false
			}
			setOff = off + v
		}
		if setOff+2 > len(d) {
			return 0, nil, false
		}
		for k := range be16(d, setOff) {
			rule := setOff + be16(d, setOff+2+2*k)
			n, recs, ok := b.chainRule(d, l, rule, at, format, back, input, ahead)
			if ok {
				return n, recs, true
			}
		}
	case 3:
		p := off + 2
		nb := be16(d, p)
		if p+2+2*nb > len(d) {
			return 0, nil, false
		}
		var backs []matcher
		for i := range nb {
			cv := off + be16(d, p+2+2*i)
			backs = append(backs, func(x int) bool { return coverageIndex(d, cv, x) >= 0 })
		}
		p += 2 + 2*nb
		ni := be16(d, p)
		if ni == 0 || p+2+2*ni > len(d) {
			return 0, nil, false
		}
		if coverageIndex(d, off+be16(d, p+2), b.items[at].gid) < 0 {
			return 0, nil, false
		}
		var ins []matcher
		for i := 1; i < ni; i++ {
			cv := off + be16(d, p+2+2*i)
			ins = append(ins, func(x int) bool { return coverageIndex(d, cv, x) >= 0 })
		}
		p += 2 + 2*ni
		na := be16(d, p)
		if p+2+2*na > len(d) {
			return 0, nil, false
		}
		var aheads []matcher
		for i := range na {
			cv := off + be16(d, p+2+2*i)
			aheads = append(aheads, func(x int) bool { return coverageIndex(d, cv, x) >= 0 })
		}
		p += 2 + 2*na
		if p+2 > len(d) {
			return 0, nil, false
		}
		if !b.backward(l, at, backs) {
			return 0, nil, false
		}
		n, ok := b.forward(l, at, ins)
		if !ok {
			return 0, nil, false
		}
		if !b.aheadOK(l, at+n-1, aheads) {
			return 0, nil, false
		}
		return n, readRecords(d, p+2, be16(d, p)), true
	}
	return 0, nil, false
}

// chainRule matches one rule of a chaining context of format 1 or 2.
func (b *buffer) chainRule(d []byte, l *otLookup, rule, at, format, back, input, ahead int) (int, []subRecord, bool) {
	if rule+2 > len(d) {
		return 0, nil, false
	}
	match := func(v, cd int) matcher {
		if format == 1 {
			return func(x int) bool { return x == v }
		}
		return func(x int) bool { return classOf(d, cd, x) == v }
	}
	p := rule
	nb := be16(d, p)
	if p+2+2*nb > len(d) {
		return 0, nil, false
	}
	var backs []matcher
	for i := range nb {
		backs = append(backs, match(be16(d, p+2+2*i), back))
	}
	p += 2 + 2*nb
	ni := be16(d, p)
	if ni == 0 || p+2+2*(ni-1) > len(d) {
		return 0, nil, false
	}
	var ins []matcher
	for i := 1; i < ni; i++ {
		ins = append(ins, match(be16(d, p+2+2*(i-1)), input))
	}
	p += 2 + 2*(ni-1)
	na := be16(d, p)
	if p+2+2*na > len(d) {
		return 0, nil, false
	}
	var aheads []matcher
	for i := range na {
		aheads = append(aheads, match(be16(d, p+2+2*i), ahead))
	}
	p += 2 + 2*na
	if p+2 > len(d) {
		return 0, nil, false
	}
	if !b.backward(l, at, backs) {
		return 0, nil, false
	}
	n, ok := b.forward(l, at, ins)
	if !ok {
		return 0, nil, false
	}
	if !b.aheadOK(l, at+n-1, aheads) {
		return 0, nil, false
	}
	return n, readRecords(d, p+2, be16(d, p)), true
}

// aheadOK matches what has to follow the run.
func (b *buffer) aheadOK(l *otLookup, last int, want []matcher) bool {
	p := last
	for _, m := range want {
		p = b.next(l, p)
		if p >= len(b.items) || !m(b.items[p].gid) {
			return false
		}
	}
	return true
}
