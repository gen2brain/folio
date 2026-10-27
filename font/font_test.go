package font

import (
	"bytes"
	"cmp"
	"compress/zlib"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

const refDir = "/media/adata/temp/corpus/pdf"

// TestStandardFonts checks the glyphs and metrics of the fourteen substitutes.
func TestStandardFonts(t *testing.T) {
	for name := range stdFontFile {
		f := Standard(name)
		if f == nil {
			t.Fatalf("%s: not loaded", name)
		}
		if f.Kind != KindCFF {
			t.Errorf("%s: kind %d, want CFF", name, f.Kind)
		}
		if f.NumGlyphs() < 100 {
			t.Errorf("%s: %d glyphs", name, f.NumGlyphs())
		}
		if !f.HasGlyphNames() {
			t.Errorf("%s: no glyph names", name)
		}

		glyph := "A"
		if name == "Symbol" {
			glyph = "alpha"
		} else if name == "ZapfDingbats" {
			glyph = "a9"
		}
		gid := f.GIDForName(glyph)
		if gid <= 0 {
			t.Errorf("%s: no glyph %q", name, glyph)
			continue
		}
		p := f.GlyphPath(gid)
		if p == nil || p.IsEmpty() {
			t.Errorf("%s: glyph %q has no outline", name, glyph)
		}
		if w := StandardWidth(name, glyph); w > 0 {
			if adv := f.Advance(gid); adv != float32(w) {
				t.Errorf("%s: %q advance %g, metrics say %g", name, glyph, adv, w)
			}
		}
	}
}

// TestStandardName holds the substitution rules to the names real files use.
func TestStandardName(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		serif, fixed, symbolic bool
		bold, italic           bool
		want                   string
	}{
		{name: "Helvetica", want: "Helvetica"},
		{name: "ABCDEF+Helvetica", want: "Helvetica"},
		{name: "Arial-BoldMT", want: "Helvetica-Bold"},
		{name: "ArialMT", want: "Helvetica"},
		{name: "TimesNewRomanPSMT", want: "Times-Roman"},
		{name: "CourierNewPSMT", want: "Courier"},
		{name: "Unknown", want: "Helvetica"},
		{name: "Unknown", serif: true, want: "Times-Roman"},
		{name: "Unknown", fixed: true, want: "Courier"},
		{name: "Unknown", bold: true, italic: true, want: "Helvetica-BoldOblique"},
		{name: "SomethingBoldItalic", want: "Helvetica-BoldOblique"},
		{name: "MonospaceBold", want: "Courier-Bold"},
		{name: "GeorgiaItalic", want: "Times-Italic"},
	} {
		got := StandardName(tc.name, tc.serif, tc.fixed, tc.symbolic, tc.bold, tc.italic)
		if got != tc.want {
			t.Errorf("StandardName(%q) = %q, want %q", tc.name, got, tc.want)
		}
		if Standard(got) == nil {
			t.Errorf("StandardName(%q) = %q, which is not one of the fourteen", tc.name, got)
		}
	}
}

func TestRuneForName(t *testing.T) {
	for _, tc := range []struct {
		name string
		want rune
	}{
		{"A", 'A'},
		{"space", ' '},
		{"eacute", 'é'},
		{"uni0041", 'A'},
		{"u00C6", 'Æ'},
		{"a.sc", 'a'},
		{"nonexistentglyphname", 0},
		{"", 0},
	} {
		if got := RuneForName(tc.name); got != tc.want {
			t.Errorf("RuneForName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestEncodings(t *testing.T) {
	for _, name := range []string{"StandardEncoding", "WinAnsiEncoding", "MacRomanEncoding", "Symbol", "ZapfDingbats"} {
		e := Encoding(name)
		if e == nil {
			t.Fatalf("%s: missing", name)
		}
		if e[65] == "" && name != "Symbol" && name != "ZapfDingbats" {
			t.Errorf("%s: code 65 is unnamed", name)
		}
	}
	if Encoding("NoSuchEncoding") != nil {
		t.Error("an unknown encoding resolved")
	}
	if got := Encoding("WinAnsiEncoding")[0xA9]; got != "copyright" {
		t.Errorf("WinAnsi 0xA9 = %q", got)
	}
	if got := MacRomanCode('é'); got != 0x8E {
		t.Errorf("MacRomanCode(é) = %d, want 142", got)
	}
}

// TestSystemFonts parses whatever fonts the machine has.
func TestSystemFonts(t *testing.T) {
	var files []string
	for _, dir := range []string{"/usr/share/fonts", "/System/Library/Fonts", "C:\\Windows\\Fonts"} {
		filepath.Walk(dir, func(p string, fi os.FileInfo, err error) error {
			if err != nil || fi.IsDir() || len(files) >= 12 {
				return nil
			}
			switch strings.ToLower(filepath.Ext(p)) {
			case ".ttf", ".otf", ".pfb":
				files = append(files, p)
			}
			return nil
		})
	}
	if len(files) == 0 {
		t.Skip("no system fonts")
	}
	for _, name := range files {
		b, err := os.ReadFile(name)
		if err != nil {
			continue
		}
		f, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(name), err)
			continue
		}
		if f.NumGlyphs() == 0 {
			t.Errorf("%s: no glyphs", filepath.Base(name))
			continue
		}
		outlines := 0
		for gid := 0; gid < f.NumGlyphs() && gid < 200; gid++ {
			if p := f.GlyphPath(gid); p != nil && !p.IsEmpty() {
				outlines++
			}
		}
		if outlines == 0 {
			t.Errorf("%s: %d glyphs, none with an outline", filepath.Base(name), f.NumGlyphs())
		}
	}
}

// TestMalformedFonts feeds the parsers truncated and corrupted programs.
func TestMalformedFonts(t *testing.T) {
	seeds := [][]byte{}
	for name := range stdFontFile {
		if f := Standard(name); f != nil {
			seeds = append(seeds, []byte(stdFontData[stdFontFile[name]]))
			break
		}
	}
	if len(seeds) == 0 {
		t.Skip("no seed font")
	}
	orig := seeds[0]
	for _, frac := range []int{2, 3, 4, 8, 16} {
		b := make([]byte, len(orig)*(frac-1)/frac)
		copy(b, orig)
		mustNotPanic(t, b)
	}
	for i := 0; i < len(orig); i += 101 {
		b := make([]byte, len(orig))
		copy(b, orig)
		b[i] ^= 0xff
		mustNotPanic(t, b)
	}
	for _, b := range [][]byte{nil, {}, {0}, {1, 0, 4, 2}, []byte("%!PS-AdobeFont"), []byte("OTTO")} {
		mustNotPanic(t, b)
	}
	// A bare CFF whose charstring index is empty.
	mustNotPanic(t, []byte{
		1, 0, 4, 2,
		0, 0,
		0, 1, 1, 1, 9,
		28, 0, 25, 15, 28, 0, 23, 17,
		0, 0,
		0, 0,
		0, 0,
		0,
	})
}

func mustNotPanic(t *testing.T, b []byte) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic on %d bytes: %v", len(b), r)
		}
	}()
	f, err := Parse(b)
	if err != nil {
		return
	}
	for gid := 0; gid < f.NumGlyphs() && gid < 50; gid++ {
		f.GlyphPath(gid)
		f.Advance(gid)
		f.GlyphName(gid)
	}
	f.GIDForName("A")
	f.GIDForRune('A')
}

func FuzzParse(fu *testing.F) {
	for _, file := range stdFontFile {
		fu.Add([]byte(stdFontData[file]))
		break
	}
	if b, err := os.ReadFile(filepath.Join("..", "testdata", "mini.woff2")); err == nil {
		fu.Add(b)
	}
	fu.Fuzz(func(t *testing.T, b []byte) {
		defer slowFuzz(t)()
		f, err := Parse(b)
		if err != nil {
			return
		}
		for gid := 0; gid < f.NumGlyphs() && gid < 20; gid++ {
			f.GlyphPath(gid)
		}
		f.Shape([]rune("مها شَدّ שָׁ áb fi"), true)
		f.Shape([]rune("مها fi"), false)
	})
}

func FuzzBrotli(fu *testing.F) {
	if b, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.br")); err == nil {
		fu.Add(b)
	}
	fu.Add([]byte{0x0b})
	fu.Fuzz(func(t *testing.T, b []byte) {
		defer slowFuzz(t)()
		const limit = 1 << 20
		out, err := brotliDecode(b, limit)
		if err == nil && len(out) > limit {
			t.Fatalf("%d bytes past the limit", len(out))
		}
	})
}

// TestStandardConcurrent checks two concurrent renders reaching one standard font.
func TestStandardConcurrent(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f := Standard("Helvetica")
			if f == nil {
				t.Error("no Helvetica")
				return
			}
			for gid := 0; gid < 64; gid++ {
				f.GlyphPath(gid)
			}
		}()
	}
	wg.Wait()
}

// TestSystemIndex checks the index of the fonts the machine has.
func TestSystemIndex(t *testing.T) {
	ix := systemIndex()
	if len(ix.all) == 0 {
		t.Skip("the machine has no fonts")
	}
	for _, e := range ix.all {
		if e.family == "" {
			t.Errorf("%s is indexed under no family", e.path)
		}
		if e.weight < 1 || e.weight > 1000 {
			t.Errorf("%s has weight %d", e.path, e.weight)
		}
	}
	e := ix.all[0]
	f := SystemFont(e.family, e.weight >= 600, e.italic)
	if f == nil {
		t.Fatalf("%q is indexed and not found", e.family)
	}
	if foldName(f.Family) != foldName(e.family) && !hasPrefixFold(foldName(f.Family), foldName(e.family)) {
		t.Errorf("asked for %q and got %q", e.family, f.Family)
	}
	if SystemFont("a family no machine has, surely", false, false) != nil {
		t.Error("a family that is not there was found")
	}
	if SystemFont("", false, false) != nil {
		t.Error("the empty family was found")
	}
}

func hasPrefixFold(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// TestFallback checks that a character the base fourteen cannot draw finds a face.
func TestFallback(t *testing.T) {
	if len(systemIndex().all) == 0 {
		t.Skip("the machine has no fonts")
	}
	for _, r := range []rune{'日', 'あ', '한', 'Ж', 'α'} {
		f := Fallback(r, false, false)
		if f == nil {
			t.Logf("%c: the machine has no face for it", r)
			continue
		}
		if f.GIDForRune(r) <= 0 {
			t.Errorf("%c: %q was chosen and has no glyph for it", r, f.Family)
		}
		if f2 := Fallback(r, false, false); f2 != f {
			t.Errorf("%c: the answer is not remembered", r)
		}
	}
	if Fallback(0x10fffd, false, false) != nil {
		t.Log("a private use character found a face, which is allowed")
	}
}

func TestCovers(t *testing.T) {
	check := func(path string) {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := Parse(b)
		if err != nil {
			return
		}
		e := &entry{path: path}
		for r := rune(0); r < 0x3000; r++ {
			if got, want := e.covers(r), f.GIDForRune(r) > 0; got != want {
				t.Fatalf("%s: covers(%U) = %v, the parsed font says %v", path, r, got, want)
			}
		}
		for r := range sameShape {
			if got, want := e.covers(r), f.GIDForRune(r) > 0; got != want {
				t.Fatalf("%s: covers(%U) = %v, the parsed font says %v", path, r, got, want)
			}
		}
	}
	check(filepath.Join("..", "testdata", "mini.ttf"))
	ix := systemIndex()
	for i, e := range ix.all {
		if i >= 12 {
			break
		}
		check(e.path)
	}
	if (&entry{path: filepath.Join(t.TempDir(), "missing.ttf")}).covers('a') {
		t.Error("a missing file covers a character")
	}
	mini, err := os.ReadFile(filepath.Join("..", "testdata", "mini.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	gone := filepath.Join(t.TempDir(), "gone.ttf")
	if err := os.WriteFile(gone, mini, 0o644); err != nil {
		t.Fatal(err)
	}
	e := &entry{path: gone}
	if !e.covers('A') {
		t.Fatal("mini.ttf does not cover A")
	}
	os.Remove(gone)
	if !e.covers('A') {
		t.Error("the character map was read again rather than remembered")
	}

	const r = 0x10fffd
	scanned := func() int {
		n := 0
		for _, e := range ix.all {
			if e.font == nil {
				continue
			}
			named := false
			for _, name := range fallbackFamilies[scriptOf(r)] {
				named = named || hasPrefixFold(foldName(e.family), foldName(name))
			}
			if !named {
				n++
			}
		}
		return n
	}
	before := scanned()
	f := Fallback(r, true, true)
	want := before
	if f != nil {
		want++
	}
	if after := scanned(); after > want {
		t.Errorf("a fallback search kept %d fonts it did not choose", after-want)
	}
}

// TestAddFontDir checks that a caller's own directory is searched.
func TestAddFontDir(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notafont.ttf"), []byte("nonsense"), 0o644); err != nil {
		t.Fatal(err)
	}
	AddFontDir(dir)
	if e := describe(filepath.Join(dir, "notafont.ttf")); e != nil {
		t.Errorf("nonsense described as %+v", e)
	}
	if e := describe(filepath.Join(dir, "missing.ttf")); e != nil {
		t.Errorf("a missing file described as %+v", e)
	}
}

func TestFoldName(t *testing.T) {
	cases := [][2]string{
		{"Noto Sans CJK JP", "notosanscjkjp"},
		{"Times New Roman", "timesnewroman"},
		{"  DejaVu-Sans  ", "dejavusans"},
		{"", ""},
	}
	for _, c := range cases {
		if got := foldName(c[0]); got != c[1] {
			t.Errorf("foldName(%q) = %q, want %q", c[0], got, c[1])
		}
	}
}

func TestScriptOf(t *testing.T) {
	cases := []struct {
		r rune
		s script
	}{
		{'a', scriptLatin}, {'日', scriptHan}, {'あ', scriptKana}, {'한', scriptHangul},
		{'Ж', scriptCyrillic}, {'α', scriptGreek}, {'ع', scriptArabic}, {'א', scriptHebrew},
		{'ก', scriptThai}, {'क', scriptDevanagari},
	}
	for _, c := range cases {
		if got := scriptOf(c.r); got != c.s {
			t.Errorf("scriptOf(%c) = %d, want %d", c.r, got, c.s)
		}
	}
	for k := range scriptOther {
		if len(fallbackFamilies[k]) == 0 {
			t.Errorf("script %d has no families to try", k)
		}
	}
}

// TestCIDSharedPrivate checks an FDArray whose dictionaries share one Private.
func TestCIDSharedPrivate(t *testing.T) {
	index := func(items [][]byte) []byte {
		b := []byte{byte(len(items) >> 8), byte(len(items)), 4}
		off := 1
		for i := 0; i <= len(items); i++ {
			b = binary.BigEndian.AppendUint32(b, uint32(off))
			if i < len(items) {
				off += len(items[i])
			}
		}
		for _, it := range items {
			b = append(b, it...)
		}
		return b
	}
	num := func(v int) []byte { return binary.BigEndian.AppendUint32([]byte{29}, uint32(v)) }

	subrs := make([][]byte, 4096)
	for i := range subrs {
		subrs[i] = []byte{11}
	}
	priv := append(num(6), 19)
	privAt := 16
	data := make([]byte, privAt)
	data = append(data, priv...)
	data = append(data, index(subrs)...)
	fd := append(append(num(len(priv)), num(privAt)...), 18)
	fds := make([][]byte, 4096)
	for i := range fds {
		fds[i] = fd
	}
	fdaAt := len(data)
	data = append(data, index(fds)...)

	c := &cffFont{data: data}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	c.readCIDPrivates(map[int][]float64{key12(36): {float64(fdaAt)}}, false)
	runtime.ReadMemStats(&after)
	if len(c.fdPriv) != 4096 || len(c.fdPriv[4095].subrs) != 4096 {
		t.Fatalf("%d private dicts, the last with %d subroutines", len(c.fdPriv), len(c.fdPriv[len(c.fdPriv)-1].subrs))
	}
	if got := after.TotalAlloc - before.TotalAlloc; got > 64<<20 {
		t.Errorf("4096 dictionaries sharing one Private allocated %d MB", got>>20)
	}
}

// TestCIDRanges reads FDSelect ranges that overlap and maps CIDs back to glyphs.
func TestCIDRanges(t *testing.T) {
	const n = 60000
	d := []byte{3}
	d = binary.BigEndian.AppendUint16(d, n)
	for i := range n {
		first, fd := 0, byte(1)
		if i%2 == 1 {
			first, fd = 0xffff, 2
		}
		d = append(binary.BigEndian.AppendUint16(d, uint16(first)), fd)
	}
	d = binary.BigEndian.AppendUint16(d, 0xffff)
	c := &cffFont{data: append([]byte{0}, d...), charStrings: make([][]byte, 0xffff)}
	c.readCIDPrivates(map[int][]float64{key12(37): {1}}, false)
	for _, g := range []int{0, 0x8000, 0xfffe} {
		if c.fdSelect[g] != 1 {
			t.Errorf("glyph %d is in font dict %d, want the first range's 1", g, c.fdSelect[g])
		}
	}

	c = &cffFont{charset: []uint16{0, 5, 7, 5}, charStrings: make([][]byte, 4)}
	for cid, want := range map[int]int{0: 0, 5: 1, 7: 2, 3: 3, 9: 0} {
		if got := c.gidForCID(cid); got != want {
			t.Errorf("CID %d is glyph %d, want %d", cid, got, want)
		}
	}
}

// TestLayoutShared checks feature and lookup lists that repeat one table.
func TestLayoutShared(t *testing.T) {
	const n = 4096
	put := func(b []byte, vs ...int) []byte {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, uint16(v))
		}
		return b
	}
	data := put(nil, 1)
	data = append(data, "kern"...)
	data = put(data, 8)
	data = put(data, 0, n)
	for i := range n {
		data = put(data, i)
	}
	lang := len(data)
	data = put(data, 0, 0xffff, n)
	for range n {
		data = put(data, 0)
	}
	look := len(data)
	data = put(data, n)
	for range n {
		data = put(data, 2+2*n)
	}
	data = put(data, 1, 0, n)
	for range n {
		data = put(data, 0)
	}
	tbl := &otLayout{data: data, feat: 0, look: look}

	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	got := tbl.featureLookups(lang, "kern")
	first := tbl.lookup(0)
	for i := 1; i < n; i++ {
		if tbl.lookup(i) != first {
			t.Fatalf("lookup %d was read again", i)
		}
	}
	runtime.ReadMemStats(&after)
	if len(got) != n {
		t.Errorf("a feature named %d times gave %d lookups, want %d", n, len(got), n)
	}
	if first == nil || len(first.subs) != n {
		t.Fatalf("the shared lookup is %+v", first)
	}
	if a := after.TotalAlloc - before.TotalAlloc; a > 16<<20 {
		t.Errorf("shared tables allocated %d MB", a>>20)
	}
}

// TestCompositeFanOut checks a composite glyph that names itself many times.
func TestCompositeFanOut(t *testing.T) {
	put := func(b []byte, vs ...int) []byte {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, uint16(v))
		}
		return b
	}
	comp := put(nil, 0xffff, 0, 0, 0, 0)
	for range 7 {
		comp = put(comp, 0x0020|0x0001, 0, 0, 0)
	}
	comp = put(comp, 0x0001, 1, 0, 0)
	simple := put(nil, 1, 0, 0, 10, 10, 2, 0)
	simple = append(simple, 1, 1, 1, 1)
	simple = put(simple, 0, 10, 0, 0, 0, 10)
	glyf := append(append([]byte{}, comp...), simple...)
	loca := binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(binary.BigEndian.AppendUint32(nil, 0), uint32(len(comp))), uint32(len(glyf)))
	f := &Font{sfnt: &sfnt{tables: map[string][]byte{"loca": loca, "glyf": glyf}, locaLong: true}}
	budget := maxComponents
	done := make(chan struct{})
	go func() {
		f.glyfPath(0, 0, &budget)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("a glyph of seven copies of itself did not finish")
	}
	if budget > 0 {
		t.Errorf("the budget was not spent: %d left", budget)
	}
}

// TestWOFF unpacks a WOFF container.
func TestWOFF(t *testing.T) {
	src := Standard("Times-Roman")
	if src == nil {
		t.Skip("no substitute to pack")
	}
	for _, name := range []string{"", "wOFF"} {
		if _, err := Parse([]byte(name)); err == nil {
			t.Errorf("%q parsed as a font", name)
		}
	}
	b := append([]byte("wOFF"), make([]byte, 60)...)
	if _, err := Parse(b); err == nil {
		t.Error("an empty WOFF parsed")
	}

	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	zw.Write([]byte{0})
	zw.Close()
	const n = 512
	bomb := make([]byte, 44+20*n)
	copy(bomb, "wOFF")
	binary.BigEndian.PutUint16(bomb[12:], n)
	binary.BigEndian.PutUint32(bomb[16:], uint32(100*(len(bomb)+z.Len())))
	for i := range n {
		e := 44 + 20*i
		binary.BigEndian.PutUint32(bomb[e+4:], uint32(len(bomb)))
		binary.BigEndian.PutUint32(bomb[e+8:], uint32(z.Len()))
		binary.BigEndian.PutUint32(bomb[e+12:], 1<<27)
	}
	bomb = append(bomb, z.Bytes()...)
	if _, err := Parse(bomb); err == nil {
		t.Error("512 tables claiming 128 MB each parsed")
	}
}

// TestWOFFCorpus reads the web fonts of the corpus.
func TestWOFFCorpus(t *testing.T) {
	dir := envOr("PDF_REF_DIR", refDir)
	all, _ := filepath.Glob(filepath.Join(dir, "corpus/books/epub3/30/*/EPUB/*.woff"))
	var paths []string
	for _, p := range all {
		if !strings.Contains(p, ".obf.") {
			paths = append(paths, p)
		}
	}
	if len(paths) == 0 {
		t.Skip("no web fonts in the corpus")
	}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := Parse(b)
		if err != nil {
			t.Errorf("%s: %v", p, err)
			continue
		}
		if f.Family == "" || f.NumGlyphs() == 0 || f.GIDForRune('A') <= 0 {
			t.Errorf("%s: family %q, %d glyphs, A is %d", p, f.Family, f.NumGlyphs(), f.GIDForRune('A'))
		}
		if f.GlyphPath(f.GIDForRune('A')) == nil {
			t.Errorf("%s: the A has no outline", p)
		}
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// TestBrotli decodes what the brotli command line compresses.
func TestBrotli(t *testing.T) {
	if _, err := exec.LookPath("brotli"); err != nil {
		t.Skip("no brotli command")
	}
	var random [1 << 14]byte
	x := uint32(12345)
	for i := range random {
		x = x*1664525 + 1013904223
		random[i] = byte(x >> 24)
	}
	prose := strings.Repeat("The quick brown fox jumps over the lazy dog. ", 40) +
		"<html><head><title>Time of the day</title></head><body> and \"other\" things.</body></html>"
	cases := map[string][]byte{
		"empty":  nil,
		"one":    []byte("x"),
		"prose":  []byte(prose),
		"font":   []byte(stdFontData["FoxitSans"]),
		"random": random[:],
	}
	for name, want := range cases {
		for _, q := range []string{"0", "5", "9", "11"} {
			for _, w := range []string{"10", "16", "24"} {
				cmd := exec.Command("brotli", "-c", "-q", q, "-w", w)
				cmd.Stdin = bytes.NewReader(want)
				var out bytes.Buffer
				cmd.Stdout = &out
				if err := cmd.Run(); err != nil {
					t.Fatalf("brotli -q %s -w %s: %v", q, w, err)
				}
				got, err := brotliDecode(out.Bytes(), len(want)+1)
				if err != nil {
					t.Errorf("%s q%s w%s: %v", name, q, w, err)
					continue
				}
				if !bytes.Equal(got, want) {
					t.Errorf("%s q%s w%s: %d bytes, want %d", name, q, w, len(got), len(want))
				}
			}
		}
	}
}

// TestBrotliFile decodes the brotli stream checked in.
func TestBrotliFile(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.pdf"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join("..", "testdata", "minimal.br"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := brotliDecode(src, len(want))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("%d bytes, want %d", len(got), len(want))
	}
	if _, err := brotliDecode(src, len(want)-1); err == nil {
		t.Error("a stream decoded past its limit")
	}
	for i := range src {
		b := append([]byte(nil), src[:i]...)
		if _, err := brotliDecode(b, len(want)); err == nil {
			t.Errorf("%d bytes of the stream decoded whole", i)
		}
	}
}

// TestWOFF2 unpacks the WOFF2 font checked in against the reference decoder.
func TestWOFF2(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "testdata", "mini.woff2"))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("..", "testdata", "mini.ttf"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := woff2SFNT(src)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		i := 0
		for i < len(got) && i < len(want) && got[i] == want[i] {
			i++
		}
		t.Fatalf("%d bytes of sfnt, want %d, differing at %d", len(got), len(want), i)
	}

	f, err := Parse(src)
	if err != nil {
		t.Fatal(err)
	}
	if f.Family != "Mini" || f.NumGlyphs() != 4 || f.UnitsPerEm != 1000 {
		t.Errorf("family %q, %d glyphs, %d per em", f.Family, f.NumGlyphs(), f.UnitsPerEm)
	}
	for i, c := range "ABC" {
		g := f.GIDForRune(c)
		if g != i+1 {
			t.Errorf("%c is glyph %d, want %d", c, g, i+1)
		}
		if f.GlyphPath(g) == nil {
			t.Errorf("%c has no outline", c)
		}
	}
	for g, want := range []float32{500, 600, 700, 900} {
		if got := f.Advance(g); got != want {
			t.Errorf("glyph %d is %v wide, want %v", g, got, want)
		}
	}
	if _, err := Parse(append([]byte("wOF2"), make([]byte, 60)...)); err == nil {
		t.Error("an empty WOFF2 parsed")
	}
	for i := range src {
		if _, err := Parse(src[:i]); err == nil {
			t.Errorf("%d bytes of the web font parsed whole", i)
		}
	}
}

// TestJoining covers the form a cursive letter takes from its neighbors.
func TestJoining(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		want joinType
	}{
		{'م', joinDual},           // meem joins both ways
		{'ا', joinRight},          // alef joins only backwards
		{0x0640, joinCausing},     // tatweel
		{0x064E, joinTransparent}, // fatha, a mark
		{'A', joinNone},
	} {
		if got := joinTypeOf(tc.r); got != tc.want {
			t.Errorf("U+%04X is %d, want %d", tc.r, got, tc.want)
		}
	}

	// Meem, heh, alef: initial, medial, final.
	text := []rune("مها")
	b := &buffer{items: make([]item, len(text))}
	for i := range b.items {
		b.items[i].mask = 1
	}
	b.joining(text)
	want := []int{formInit, formMedi, formFina}
	for i, w := range want {
		if b.items[i].mask&(1<<uint(w)) == 0 {
			t.Errorf("%q took the wrong form, mask %b", text[i], b.items[i].mask)
		}
	}
	one := []rune("م")
	b = &buffer{items: []item{{mask: 1}}}
	b.joining(one)
	if b.items[0].mask&(1<<formIsol) == 0 {
		t.Errorf("a letter on its own took mask %b", b.items[0].mask)
	}
}

// TestMultipleSubBound checks a substitution that multiplies the run.
func TestMultipleSubBound(t *testing.T) {
	put := func(b []byte, vs ...int) []byte {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, uint16(v))
		}
		return b
	}
	d := put(nil, 1, 8, 1, 14)
	d = put(d, 1, 1, 5)
	d = put(d, 1000)
	for range 1000 {
		d = put(d, 5)
	}
	b := &buffer{items: []item{{gid: 5}}, maxLen: max(1*maxLenFactor, maxLenMin)}
	for range 100 {
		b.multipleSub(d, 0, 0)
	}
	if len(b.items) > b.maxLen {
		t.Errorf("the run grew to %d glyphs, over %d", len(b.items), b.maxLen)
	}
	if len(b.items) < 1000 {
		t.Errorf("the substitution did not apply: %d glyphs", len(b.items))
	}
}

// TestContextBudget runs a context lookup that applies itself sixty-four times.
func TestContextBudget(t *testing.T) {
	put := func(b []byte, vs ...int) []byte {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, uint16(v))
		}
		return b
	}
	const k = 64
	d := put(nil, 1, 0, 10, 12, 14, 0, 0, 1, 4, gsubContext, 0, 1, 8, 3, 1, k, 8+4*k)
	for range k {
		d = put(d, 0, 0)
	}
	d = put(d, 1, 1, 5)
	f := &Font{gsub: readLayout(d)}
	b := &buffer{f: f, items: []item{{gid: 5, mask: 1}, {gid: 5, mask: 1}}, maxLen: maxLenMin, ops: maxOpsMin}
	b.applySub(f.gsub.lookup(0), 1, 0)
	if b.ops >= 0 {
		t.Errorf("%d nested lookups left, want the budget spent", b.ops)
	}
}

// TestKernTables reads the one horizontal subtable among sixty thousand vertical ones.
func TestKernTables(t *testing.T) {
	put := func(b []byte, vs ...int) []byte {
		for _, v := range vs {
			b = binary.BigEndian.AppendUint16(b, uint16(v))
		}
		return b
	}
	const n = 60000
	d := put(nil, 0, n)
	for range n - 1 {
		d = put(d, 0, 6, 0x0002)
	}
	d = put(d, 0, 20, 0x0001, 1, 6, 0, 0, 1, 2, 0xffce)
	f := &Font{kern: kernTables(d)}
	for range 20000 {
		if v := f.kernPairs(1, 2); v != -50 {
			t.Fatalf("the pair kerns by %d, want -50", v)
		}
	}
	if v := f.kernPairs(2, 1); v != 0 {
		t.Errorf("an absent pair kerns by %d", v)
	}
}

// TestMarkOrder covers the order the marks of one cluster are put in.
func TestMarkOrder(t *testing.T) {
	// A shin dot is written after a qamats and drawn before it.
	runes := []rune{0x05E9, 0x05B8, 0x05C1}
	clusters := []int{0, 0, 0}
	sortMarks(runes, clusters)
	if want := []rune{0x05E9, 0x05C1, 0x05B8}; string(runes) != string(want) {
		t.Errorf("sorted to %04X, want %04X", runes, want)
	}
	// A shadda comes before the vowel it is written after.
	runes = []rune{0x0628, 0x064E, 0x0651}
	clusters = []int{0, 0, 0}
	sortMarks(runes, clusters)
	if want := []rune{0x0628, 0x0651, 0x064E}; string(runes) != string(want) {
		t.Errorf("sorted to %04X, want %04X", runes, want)
	}
	if clusters[0] != 0 || clusters[2] != 0 {
		t.Errorf("clusters came out %v", clusters)
	}
}

// TestDecompose covers the decomposition of a precomposed character a font lacks.
func TestDecompose(t *testing.T) {
	a, b, ok := decomposeRune('á')
	if !ok || a != 'a' || b != 0x0301 {
		t.Errorf("decomposed to %04X %04X %v", a, b, ok)
	}
	if _, _, ok := decomposeRune('a'); ok {
		t.Error("a plain letter decomposed")
	}
}

// TestShapeArabic checks that an Arabic run shaped with a machine face joins.
func TestShapeArabic(t *testing.T) {
	f := Fallback('م', false, false)
	if f == nil || !f.Shaped() {
		t.Skip("no Arabic face with layout tables")
	}
	nominal := f.GIDForRune('م')
	gs := f.Shape([]rune("مها"), true)
	if len(gs) != 3 {
		t.Fatalf("%d glyphs, want 3", len(gs))
	}
	if gs[2].Cluster != 0 || gs[0].Cluster != 2 {
		t.Errorf("clusters %d %d %d, want the run reversed", gs[0].Cluster, gs[1].Cluster, gs[2].Cluster)
	}
	if gs[2].GID == nominal {
		t.Errorf("the meem is still glyph %d, want the joined form", nominal)
	}
	for _, g := range gs {
		if g.GID <= 0 {
			t.Errorf("glyph %d is not there", g.GID)
		}
	}
}

// TestSyllables covers how an Indic run is cut into syllables.
func TestSyllables(t *testing.T) {
	cats := func(s string) []uint8 {
		out := make([]uint8, len([]rune(s)))
		for i, r := range []rune(s) {
			out[i], _ = indicOf(r)
		}
		return out
	}
	for _, tc := range []struct {
		text string
		want [][2]int
	}{
		{"क", [][2]int{{0, 1}}},
		{"क्क", [][2]int{{0, 3}}},
		{"कि", [][2]int{{0, 2}}},
		{"नमस्ते", [][2]int{{0, 1}, {1, 2}, {2, 6}}},
		{"कर्म", [][2]int{{0, 1}, {1, 4}}},
	} {
		var got [][2]int
		for _, s := range syllables(cats(tc.text)) {
			got = append(got, [2]int{s[0], s[1]})
		}
		if len(got) != len(tc.want) {
			t.Errorf("%q cut into %v, want %v", tc.text, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("%q cut into %v, want %v", tc.text, got, tc.want)
				break
			}
		}
	}
}

// TestIndicCategories covers the Indic category and position table.
func TestIndicCategories(t *testing.T) {
	for _, tc := range []struct {
		r   rune
		cat uint8
		pos uint8
	}{
		{'क', inC, posBaseC},     // a consonant
		{'र', inRa, posBaseC},    // the one that becomes a reph
		{0x094D, inH, posEnd},    // the virama
		{0x093F, inM, posPreM},   // the matra written before its consonant
		{0x0947, inM, posAboveC}, // and one written above it
		{0x093C, inN, posEnd},    // the nukta
		{0x0902, inSM, posSMVD},  // anusvara
	} {
		cat, pos := indicOf(tc.r)
		if cat != tc.cat || pos != tc.pos {
			t.Errorf("U+%04X is (%d,%d), want (%d,%d)", tc.r, cat, pos, tc.cat, tc.pos)
		}
	}
}

// TestShapeDevanagari checks that a pre-base matra is drawn before its consonant.
func TestShapeDevanagari(t *testing.T) {
	f := Fallback('क', false, false)
	if f == nil || !f.Shaped() {
		t.Skip("no Devanagari face with layout tables")
	}
	gs := f.Shape([]rune("कि"), false)
	if len(gs) != 2 {
		t.Fatalf("%d glyphs, want 2", len(gs))
	}
	alone := f.Shape([]rune("क"), false)
	if len(alone) != 1 {
		t.Fatalf("%d glyphs for the consonant on its own", len(alone))
	}
	if gs[1].GID != alone[0].GID {
		t.Errorf("the consonant is glyph %d and came out %d, so the matra did not move",
			alone[0].GID, gs[1].GID)
	}
	if gs[0].Cluster != 0 || gs[1].Cluster != 0 {
		t.Errorf("clusters %d and %d, want both nought", gs[0].Cluster, gs[1].Cluster)
	}
}

// TestBidiCharacterConformance runs the Unicode bidirectional character test.
func TestBidiCharacterConformance(t *testing.T) {
	name := filepath.Join(cmp.Or(os.Getenv("PDF_REF_DIR"), refDir), "specs/BidiCharacterTest.txt")
	b, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	total, bad := 0, 0
	for n, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		f := strings.Split(line, ";")
		if len(f) != 5 {
			t.Fatalf("line %d: %d fields", n+1, len(f))
		}
		var text []rune
		for _, s := range strings.Fields(f[0]) {
			v, err := strconv.ParseInt(s, 16, 32)
			if err != nil {
				t.Fatalf("line %d: %v", n+1, err)
			}
			text = append(text, rune(v))
		}
		base := -1
		switch f[1] {
		case "0":
			base = 0
		case "1":
			base = 1
		}
		total++

		res := bidiResolve(text, base)
		if got, want := int(res.para), atoiOr(f[2], -1); got != want {
			if bad++; bad <= 10 {
				t.Errorf("line %d: paragraph level %d, want %d", n+1, got, want)
			}
			continue
		}
		levels := res.line(0, len(text))
		wantLevels := strings.Fields(f[3])
		if len(wantLevels) != len(levels) {
			t.Fatalf("line %d: %d levels, want %d", n+1, len(levels), len(wantLevels))
		}
		fail := false
		for i, w := range wantLevels {
			// An x is a removed character.
			if w == "x" {
				if !res.gone[i] {
					fail = true
				}
				continue
			}
			if res.gone[i] || int(levels[i]) != atoiOr(w, -1) {
				fail = true
			}
		}
		if fail {
			if bad++; bad <= 10 {
				t.Errorf("line %d: levels %v, want %v (%q)", n+1, levels, wantLevels, f[0])
			}
			continue
		}
		var order []int
		for _, i := range bidiOrder(levels) {
			if !res.gone[i] {
				order = append(order, i)
			}
		}
		var want []int
		for _, s := range strings.Fields(f[4]) {
			want = append(want, atoiOr(s, -1))
		}
		if !slices.Equal(order, want) {
			if bad++; bad <= 10 {
				t.Errorf("line %d: order %v, want %v (%q)", n+1, order, want, f[0])
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d bidi cases wrong", bad, total)
	}
	t.Logf("%d bidi character cases", total)
}

func atoiOr(s string, or int) int {
	v, err := strconv.Atoi(s)
	if err != nil {
		return or
	}
	return v
}

// bidiSample is a character of each bidi class, none of them a bracket.
var bidiSample = map[string]rune{
	"L": 'A', "R": 0x05d0, "AL": 0x0627, "EN": '0', "ES": '+', "ET": '#',
	"AN": 0x0660, "CS": ',', "NSM": 0x0300, "BN": 0x00ad, "B": 0x2029,
	"S": '\t', "WS": ' ', "ON": '!', "LRE": 0x202a, "RLE": 0x202b,
	"PDF": 0x202c, "LRO": 0x202d, "RLO": 0x202e, "LRI": 0x2066,
	"RLI": 0x2067, "FSI": 0x2068, "PDI": 0x2069,
}

// TestBidiIsolates resolves a paragraph of a hundred thousand isolates.
func TestBidiIsolates(t *testing.T) {
	text := []rune(strings.Repeat("\u2068a\u05d0", 50000) + strings.Repeat("\u2069", 50000))
	b := bidiResolve(text, -1)
	if len(b.levels) != len(text) {
		t.Fatalf("%d levels for %d characters", len(b.levels), len(text))
	}
}

// TestBidiConformance runs the Unicode bidirectional class test.
func TestBidiConformance(t *testing.T) {
	name := filepath.Join(cmp.Or(os.Getenv("PDF_REF_DIR"), refDir), "specs/BidiTest.txt")
	b, err := os.ReadFile(name)
	if err != nil {
		t.Skipf("no %s", name)
	}
	for name, r := range bidiSample {
		if got := bidiClassOf(r); bidiClasses[got] != name {
			t.Fatalf("U+%04X is %s, want %s", r, bidiClasses[got], name)
		}
	}

	var wantLevels []string
	var wantOrder []int
	total, bad := 0, 0
	for n, line := range strings.Split(string(b), "\n") {
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		if v, ok := strings.CutPrefix(line, "@Levels:"); ok {
			wantLevels = strings.Fields(v)
			continue
		}
		if v, ok := strings.CutPrefix(line, "@Reorder:"); ok {
			wantOrder = nil
			for _, s := range strings.Fields(v) {
				wantOrder = append(wantOrder, atoiOr(s, -1))
			}
			continue
		}
		classes, bits, ok := strings.Cut(line, ";")
		if !ok {
			t.Fatalf("line %d: %q", n+1, line)
		}
		var text []rune
		for _, c := range strings.Fields(classes) {
			r, ok := bidiSample[c]
			if !ok {
				t.Fatalf("line %d: no sample for class %q", n+1, c)
			}
			text = append(text, r)
		}
		set := atoiOr(strings.TrimSpace(bits), 0)
		for _, tc := range []struct {
			bit  int
			base int
		}{{1, -1}, {2, 0}, {4, 1}} {
			if set&tc.bit == 0 {
				continue
			}
			total++
			res := bidiResolve(text, tc.base)
			levels := res.line(0, len(text))
			fail := len(wantLevels) != len(levels)
			for i := 0; !fail && i < len(levels); i++ {
				if wantLevels[i] == "x" {
					fail = !res.gone[i]
					continue
				}
				fail = res.gone[i] || int(levels[i]) != atoiOr(wantLevels[i], -1)
			}
			var order []int
			for _, i := range bidiOrder(levels) {
				if !res.gone[i] {
					order = append(order, i)
				}
			}
			if !fail && !slices.Equal(order, wantOrder) {
				fail = true
			}
			if fail {
				if bad++; bad <= 10 {
					t.Errorf("line %d base %d: levels %v order %v, want %v and %v (%s)",
						n+1, tc.base, levels, order, wantLevels, wantOrder, classes)
				}
			}
		}
	}
	if bad > 0 {
		t.Fatalf("%d of %d bidi cases wrong", bad, total)
	}
	t.Logf("%d bidi class cases", total)
}

// slowFuzz fails a fuzz input that runs past five seconds.
func slowFuzz(t *testing.T) func() {
	start := time.Now()
	return func() {
		if d := time.Since(start); d > 5*time.Second {
			t.Fatalf("took %v", d)
		}
	}
}
