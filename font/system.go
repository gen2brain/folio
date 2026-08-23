package font

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

// The bounds a scan of the system fonts works inside.
const (
	maxFontFiles = 4096
	maxFontDepth = 6
	maxFontBytes = 1 << 26
	maxScanOpen  = 256
)

// systemDirs are where each system keeps its fonts.
func systemDirs() []string {
	var dirs []string
	home, _ := os.UserHomeDir()
	switch runtime.GOOS {
	case "darwin", "ios":
		dirs = []string{"/System/Library/Fonts", "/Library/Fonts"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, "Library/Fonts"))
		}
	case "windows":
		if w := os.Getenv("WINDIR"); w != "" {
			dirs = append(dirs, filepath.Join(w, "Fonts"))
		}
		if l := os.Getenv("LOCALAPPDATA"); l != "" {
			dirs = append(dirs, filepath.Join(l, `Microsoft\Windows\Fonts`))
		}
	case "android":
		dirs = []string{"/system/fonts", "/system/font", "/data/fonts"}
	default:
		dirs = []string{"/usr/share/fonts", "/usr/local/share/fonts", "/usr/share/X11/fonts"}
		if home != "" {
			dirs = append(dirs, filepath.Join(home, ".local/share/fonts"), filepath.Join(home, ".fonts"))
		}
	}
	if v := os.Getenv("XDG_DATA_HOME"); v != "" {
		dirs = append(dirs, filepath.Join(v, "fonts"))
	}
	return dirs
}

// extraDirs are the directories a caller added.
var (
	extraMu   sync.Mutex
	extraDirs []string
)

// AddFontDir adds a directory to the ones the system look-up searches. It has
// to be called before the first look-up to have any effect.
func AddFontDir(dir string) {
	extraMu.Lock()
	extraDirs = append(extraDirs, dir)
	extraMu.Unlock()
}

// entry is one font file the system has, described by its name table alone.
type entry struct {
	path   string
	family string
	weight int
	italic bool
	// once guards the full parse, which happens only for a file that is chosen.
	once sync.Once
	font *Font
}

func (e *entry) load() *Font {
	e.once.Do(func() {
		fi, err := os.Stat(e.path)
		if err != nil || fi.Size() > maxFontBytes {
			return
		}
		b, err := os.ReadFile(e.path)
		if err != nil {
			return
		}
		if f, err := Parse(b); err == nil {
			e.font = f
		}
	})
	return e.font
}

// index is every font file the machine has, by family and in found order.
type index struct {
	all    []*entry
	byName map[string][]*entry
}

var systemIndex = sync.OnceValue(buildIndex)

func buildIndex() *index {
	ix := &index{byName: map[string][]*entry{}}
	extraMu.Lock()
	dirs := append(append([]string{}, extraDirs...), systemDirs()...)
	extraMu.Unlock()
	seen := map[string]bool{}
	for _, dir := range dirs {
		walkFonts(dir, 0, func(path string) {
			if len(ix.all) >= maxFontFiles || seen[path] {
				return
			}
			seen[path] = true
			e := describe(path)
			if e == nil {
				return
			}
			ix.all = append(ix.all, e)
			key := foldName(e.family)
			if key != "" {
				ix.byName[key] = append(ix.byName[key], e)
			}
		})
	}
	return ix
}

// walkFonts visits the font files under a directory, bounded: a link loop or
// a mount full of files must not run away with the scan.
func walkFonts(dir string, depth int, fn func(string)) {
	if depth > maxFontDepth {
		return
	}
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, d := range ents {
		p := filepath.Join(dir, d.Name())
		if d.IsDir() {
			walkFonts(p, depth+1, fn)
			continue
		}
		switch strings.ToLower(filepath.Ext(d.Name())) {
		case ".ttf", ".otf", ".ttc", ".otc", ".pfb":
			fn(p)
		}
	}
}

// describe reads the name table of a font file and nothing else: the table
// directory says where it is, so four short reads answer what a file holds.
func describe(path string) *entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var head [12]byte
	if _, err := f.ReadAt(head[:], 0); err != nil {
		return nil
	}
	off := int64(0)
	if string(head[:4]) == "ttcf" {
		var first [4]byte
		if _, err := f.ReadAt(first[:], 12); err != nil {
			return nil
		}
		off = int64(be32(first[:], 0))
		if _, err := f.ReadAt(head[:], off); err != nil {
			return nil
		}
	}
	n := be16(head[:], 4)
	if n <= 0 || n > 512 {
		return nil
	}
	dir := make([]byte, 16*n)
	if _, err := f.ReadAt(dir, off+12); err != nil && err != io.EOF {
		return nil
	}
	s := &sfnt{tables: map[string][]byte{}}
	for i := range n {
		e := 16 * i
		if e+16 > len(dir) {
			break
		}
		name := tag(dir, e)
		switch name {
		case "name", "OS/2", "head":
		default:
			continue
		}
		start, length := int64(be32(dir, e+8)), be32(dir, e+12)
		if length <= 0 || length > 1<<20 {
			continue
		}
		b := make([]byte, length)
		if _, err := f.ReadAt(b, start); err != nil && err != io.EOF {
			continue
		}
		s.tables[name] = b
	}
	if len(s.tables) == 0 {
		return nil
	}
	var probe Font
	probe.readNames(s)
	if probe.Family == "" {
		return nil
	}
	return &entry{path: path, family: probe.Family, weight: probe.Weight, italic: probe.Italic}
}

// foldName is what two family names are compared as: no case, no spaces and
// nothing that is not a letter or a digit.
func foldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// SystemFont returns a face the machine has for a family, and nil when it has
// none. A family with several weights and slants gives the nearest.
func SystemFont(family string, bold, italic bool) *Font {
	key := foldName(family)
	if key == "" {
		return nil
	}
	ix := systemIndex()
	best, score := (*entry)(nil), -1
	for _, e := range ix.byName[key] {
		if s := fit(e, bold, italic); s > score {
			best, score = e, s
		}
	}
	if best == nil {
		// A family the name table spells differently: Noto Sans CJK is
		// indexed under one language and asked for under none.
		for k, list := range ix.byName {
			if !strings.HasPrefix(k, key) {
				continue
			}
			for _, e := range list {
				if s := fit(e, bold, italic); s > score {
					best, score = e, s
				}
			}
		}
	}
	if best == nil {
		return nil
	}
	return best.load()
}

// fit scores how well a file answers a request for a weight and a slant.
func fit(e *entry, bold, italic bool) int {
	want := 400
	if bold {
		want = 700
	}
	s := 1000 - abs(e.weight-want)
	if e.italic == italic {
		s += 500
	}
	return s
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// Fallback returns a face the machine has that can draw r, nil when it has
// none. It is what a document in a script the base fourteen cannot draw needs.
func Fallback(r rune, bold, italic bool) *Font {
	k := scriptOf(r)
	script := fallbackKey{k, bold, italic, 0}
	if v, ok := fallbackCache.Load(script); ok {
		if f, _ := v.(*Font); f != nil && f.GIDForRune(r) > 0 {
			return f
		}
	}
	// A character the face for its script does not have is remembered on its
	// own: without that, every one of them opens every font again.
	key := fallbackKey{k, bold, italic, r}
	if v, ok := fallbackCache.Load(key); ok {
		f, _ := v.(*Font)
		return f
	}
	f := findFallback(k, r, bold, italic)
	fallbackCache.Store(key, f)
	if f != nil {
		fallbackCache.LoadOrStore(script, f)
	}
	return f
}

type fallbackKey struct {
	script script
	bold   bool
	italic bool
	rune   rune
}

var fallbackCache sync.Map

func findFallback(k script, r rune, bold, italic bool) *Font {
	for _, name := range fallbackFamilies[k] {
		if f := SystemFont(name, bold, italic); f != nil && f.GIDForRune(r) > 0 {
			return f
		}
	}
	ix := systemIndex()
	opened := 0
	for _, e := range ix.all {
		if opened++; opened > maxScanOpen {
			break
		}
		if f := e.load(); f != nil && f.GIDForRune(r) > 0 {
			return f
		}
	}
	return nil
}
