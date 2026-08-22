package svg

import (
	"strconv"
	"strings"

	"github.com/gen2brain/folio/gfx"
)

// paint is what an element fills or strokes with. SVG lets both of them be
// left out, which is not the same as painting nothing: none means nothing is
// drawn, and an unset paint is inherited.
type paint struct {
	none  bool
	color [3]float32
}

var (
	black = paint{color: [3]float32{0, 0, 0}}
	// noPaint is what none means: the shape is not drawn at all.
	noPaint = paint{none: true}
)

// parsePaint reads a paint value. current is what currentColor stands for,
// and ok is false for a word this does not understand, which leaves whatever
// was inherited in place.
func parsePaint(s string, current paint) (paint, bool) {
	s = strings.TrimSpace(s)
	switch strings.ToLower(s) {
	case "":
		return paint{}, false
	case "none":
		return noPaint, true
	case "currentcolor":
		return current, true
	}
	// A paint may name a server and then a color to fall back on, and this
	// draws no server yet, so the fallback is what is left.
	if strings.HasPrefix(s, "url(") {
		if i := strings.IndexByte(s, ')'); i >= 0 {
			rest := strings.TrimSpace(s[i+1:])
			if rest == "" {
				return paint{}, false
			}
			return parsePaint(rest, current)
		}
		return paint{}, false
	}
	c, ok := parseColor(s, current)
	if !ok {
		return paint{}, false
	}
	return c, true
}

// parseColor reads the color forms of CSS Color 4 that SVG 1.1 allows: a
// keyword, three or six hexadecimal digits, and the two rgb functions.
func parseColor(s string, current paint) (paint, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return paint{}, false
	}
	if s[0] == '#' {
		return hexColor(s[1:])
	}
	low := strings.ToLower(s)
	if strings.HasPrefix(low, "rgb(") || strings.HasPrefix(low, "rgba(") {
		i := strings.IndexByte(s, '(')
		j := strings.LastIndexByte(s, ')')
		if j < i {
			return paint{}, false
		}
		return rgbColor(s[i+1 : j])
	}
	if low == "currentcolor" {
		return current, true
	}
	r, g, b, ok := gfx.NamedColor(low)
	if !ok {
		return paint{}, false
	}
	return paint{color: [3]float32{float32(r) / 255, float32(g) / 255, float32(b) / 255}}, true
}

func hexColor(s string) (paint, bool) {
	var v [3]float32
	switch len(s) {
	case 3, 4:
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(s[i:i+1], 16, 8)
			if err != nil {
				return paint{}, false
			}
			v[i] = float32(n*17) / 255
		}
	case 6, 8:
		for i := 0; i < 3; i++ {
			n, err := strconv.ParseUint(s[i*2:i*2+2], 16, 8)
			if err != nil {
				return paint{}, false
			}
			v[i] = float32(n) / 255
		}
	default:
		return paint{}, false
	}
	return paint{color: v}, true
}

func rgbColor(args string) (paint, bool) {
	f := strings.FieldsFunc(args, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '/'
	})
	if len(f) < 3 {
		return paint{}, false
	}
	var v [3]float32
	for i := 0; i < 3; i++ {
		s := strings.TrimSpace(f[i])
		pct := strings.HasSuffix(s, "%")
		n, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
		if err != nil {
			return paint{}, false
		}
		if pct {
			n = n * 255 / 100
		}
		v[i] = float32(min(max(n, 0), 255)) / 255
	}
	return paint{color: v}, true
}

// opacity reads a number or a percentage clamped to the unit interval, which
// is what every opacity property is.
func opacity(s string) (float32, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	pct := strings.HasSuffix(s, "%")
	v, err := strconv.ParseFloat(strings.TrimSuffix(s, "%"), 32)
	if err != nil {
		return 0, false
	}
	if pct {
		v /= 100
	}
	return float32(min(max(v, 0), 1)), true
}
