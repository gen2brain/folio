package svg

import (
	"bytes"
	"regexp"
)

// maxEntities and maxExpanded bound what the internal DTD subset may expand to.
const (
	maxEntities = 64
	maxExpanded = 1 << 22
	maxPasses   = 4
)

var entityDecl = regexp.MustCompile(`<!ENTITY\s+([A-Za-z_][\w.:-]*)\s+("[^"]*"|'[^']*')\s*>`)

// expandEntities replaces the general entities a document declares for itself.
func expandEntities(b []byte) []byte {
	i := bytes.Index(b, []byte("<!DOCTYPE"))
	if i < 0 {
		return b
	}
	j := bytes.IndexByte(b[i:], '[')
	if j < 0 {
		return b
	}
	k := bytes.Index(b[i+j:], []byte("]"))
	if k < 0 {
		return b
	}
	subset := b[i+j : i+j+k]
	found := entityDecl.FindAllSubmatch(subset, maxEntities)
	if len(found) == 0 {
		return b
	}
	names := make([][]byte, 0, len(found))
	values := make([][]byte, 0, len(found))
	for _, m := range found {
		names = append(names, append([]byte("&"), append(m[1], ';')...))
		values = append(values, m[2][1:len(m[2])-1])
	}
	body := b
	for range maxPasses {
		hit := false
		for n, name := range names {
			count := bytes.Count(body, name)
			if count == 0 {
				continue
			}
			if len(body)+count*(len(values[n])-len(name)) > maxExpanded {
				return b
			}
			body = bytes.ReplaceAll(body, name, values[n])
			hit = true
		}
		if !hit {
			break
		}
	}
	return body
}
