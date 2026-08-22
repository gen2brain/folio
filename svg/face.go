package svg

import (
	"strconv"
	"strings"

	"github.com/gen2brain/folio/font"
)

// faceRule is one @font-face block: the family it declares, what it is a
// face of, and the files it may be read from, best first.
type faceRule struct {
	family string
	weight int
	italic bool
	src    []string
}

// readFaceRule turns the declarations of an @font-face block into a rule, and
// fails when it names no family or nothing to read.
func readFaceRule(decls []decl) (faceRule, bool) {
	f := faceRule{weight: 400}
	for _, d := range decls {
		switch d.name {
		case "font-family":
			f.family = strings.Trim(strings.TrimSpace(d.value), `"'`)
		case "font-style":
			v := strings.ToLower(strings.TrimSpace(d.value))
			f.italic = v == "italic" || v == "oblique"
		case "font-weight":
			f.weight = faceWeight(d.value)
		case "src":
			f.src = faceSrc(d.value)
		}
	}
	if f.family == "" || len(f.src) == 0 {
		return f, false
	}
	return f, true
}

func faceWeight(v string) int {
	v = strings.ToLower(strings.TrimSpace(v))
	switch v {
	case "normal", "":
		return 400
	case "bold":
		return 700
	case "lighter":
		return 300
	case "bolder":
		return 600
	}
	// A range gives two numbers and the face is as heavy as the first.
	if i := strings.IndexByte(v, ' '); i > 0 {
		v = v[:i]
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 1000 {
		return 400
	}
	return n
}

// faceSrc reads the comma separated list a src declares, keeping the local
// files in the order they were written. A local() name is not a file and a
// data URL is not one either: neither is followed.
func faceSrc(v string) []string {
	var out []string
	for _, one := range splitTop(v) {
		one = strings.TrimSpace(one)
		i := strings.Index(strings.ToLower(one), "url(")
		if i < 0 {
			continue
		}
		rest := one[i+4:]
		j := strings.IndexByte(rest, ')')
		if j < 0 {
			continue
		}
		u := strings.Trim(strings.TrimSpace(rest[:j]), `"'`)
		if u == "" || strings.HasPrefix(u, "data:") || strings.Contains(u, "://") {
			continue
		}
		out = append(out, u)
	}
	return out
}

// splitTop splits on the commas that are not inside a url() or a string.
func splitTop(v string) []string {
	var out []string
	depth, quote := 0, byte(0)
	start := 0
	for i := 0; i < len(v); i++ {
		switch c := v[i]; {
		case quote != 0:
			if c == quote {
				quote = 0
			}
		case c == '"' || c == '\'':
			quote = c
		case c == '(':
			depth++
		case c == ')':
			if depth > 0 {
				depth--
			}
		case c == ',' && depth == 0:
			out = append(out, v[start:i])
			start = i + 1
		}
	}
	return append(out, v[start:])
}

// embedded is the face the drawing brings for a family at a weight and a
// slant, and nil when it brings none. A family declared more than once picks
// the face closest to what was asked for, the same way a stylesheet does.
func (d *Document) embedded(family string, bold, italic bool) *font.Font {
	if len(d.faces) == 0 {
		return nil
	}
	want := 400
	if bold {
		want = 700
	}
	best, score := -1, -1
	for i := range d.faces {
		f := &d.faces[i]
		if !strings.EqualFold(f.family, family) {
			continue
		}
		v := 1000 - abs(f.weight-want)
		if f.italic == italic {
			v += 500
		}
		if v > score {
			best, score = i, v
		}
	}
	if best < 0 {
		return nil
	}
	for _, src := range d.faces[best].src {
		if prog := d.embeddedFont(src); prog != nil {
			return prog
		}
	}
	return nil
}

// embeddedFont reads and parses one of the font programs a drawing names,
// once however many elements ask for it.
func (d *Document) embeddedFont(name string) *font.Font {
	d.fontMu.Lock()
	defer d.fontMu.Unlock()
	if prog, ok := d.fonts[name]; ok {
		return prog
	}
	var prog *font.Font
	if d.open != nil {
		if b, err := d.open(name); err == nil {
			prog, _ = font.Parse(b)
		}
	}
	if d.fonts == nil {
		d.fonts = map[string]*font.Font{}
	}
	d.fonts[name] = prog
	return prog
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
