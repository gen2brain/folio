package svg

import (
	"bytes"
	"regexp"
)

// maxEntities and maxExpanded bound what the internal subset of a document
// type declaration may expand to, so that a file whose entities refer to each
// other cannot fill memory.
const (
	maxEntities = 64
	maxExpanded = 1 << 22
	maxPasses   = 4
)

var entityDecl = regexp.MustCompile(`<!ENTITY\s+([A-Za-z_][\w.:-]*)\s+("[^"]*"|'[^']*')\s*>`)

// expandEntities replaces the general entities a document declares for itself.
// encoding/xml resolves none of them, and one may stand for markup rather than
// for text, so the substitution happens on the bytes before they are read.
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
			if !bytes.Contains(body, name) {
				continue
			}
			if len(body)+len(values[n]) > maxExpanded {
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
