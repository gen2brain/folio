package syntax

// Inheritable page attributes, ISO 32000-1 table 30.
var inheritable = []Name{"Resources", "MediaBox", "CropBox", "Rotate"}

// NumPages returns the number of pages.
func (f *File) NumPages() int { return len(f.pages) }

// PageRef returns the reference to page i, counting from zero.
func (f *File) PageRef(i int) Ref {
	if i < 0 || i >= len(f.pages) {
		return Ref{}
	}
	return f.pages[i]
}

// Page returns the dictionary of page i, counting from zero, with the
// inheritable attributes of its ancestors filled in.
func (f *File) Page(i int) Dict {
	if i < 0 || i >= len(f.pages) {
		return nil
	}
	return f.GetDict(f.pages[i])
}

// pageRefs walks the page tree and returns one reference per leaf, in order.
func (f *File) pageRefs() []Ref {
	root := f.GetDict(f.root["Pages"])
	if root == nil {
		return f.scanForPages()
	}
	path := map[Ref]bool{}
	var out []Ref
	f.walkPages(f.root["Pages"], root, path, &out, 0)
	if len(out) == 0 {
		return f.scanForPages()
	}
	return out
}

const maxPageDepth = 64

func (f *File) walkPages(ref Object, node Dict, path map[Ref]bool, out *[]Ref, depth int) {
	if node == nil || depth > maxPageDepth || len(*out) >= maxObjects {
		return
	}
	if r, ok := ref.(Ref); ok {
		if path[r] {
			f.errorf("page tree cycle at %v", r)
			return
		}
		path[r] = true
		defer delete(path, r)
	}

	kids := f.GetArray(node["Kids"])
	if kids == nil {
		if r, ok := ref.(Ref); ok {
			f.inherit(node, r)
			*out = append(*out, r)
		}
		return
	}
	count := int(f.GetInt(node["Count"], -1))
	start := len(*out)

	for _, kid := range kids {
		if count >= 0 && len(*out)-start >= count {
			f.errorf("page tree node has more kids than its /Count %d", count)
			break
		}
		kd := f.GetDict(kid)
		if kd == nil {
			if r, isRef := kid.(Ref); isRef {
				f.errorf("page %d: %v is missing", len(*out)+1, r)
				*out = append(*out, r)
				continue
			}
			f.errorf("page tree kid %s is not a dictionary", format(kid))
			continue
		}
		for _, k := range inheritable {
			if _, ok := kd[k]; !ok {
				if v, ok := node[k]; ok {
					kd[k] = v
				}
			}
		}
		f.walkPages(kid, kd, path, out, depth+1)
	}
}

// inherit fills in attributes a leaf did not get from the walk, which happens
// when the page was reached by repair rather than through the tree.
func (f *File) inherit(page Dict, ref Ref) {
	for _, k := range inheritable {
		if _, ok := page[k]; ok {
			continue
		}
		parent := page["Parent"]
		for depth := 0; depth < maxPageDepth; depth++ {
			pd := f.GetDict(parent)
			if pd == nil {
				break
			}
			if v, ok := pd[k]; ok {
				page[k] = v
				break
			}
			parent = pd["Parent"]
		}
	}
}

// scanForPages finds page objects directly, for a file whose page tree is
// unusable.
func (f *File) scanForPages() []Ref {
	var out []Ref
	for num := range f.xref.ents {
		if len(out) >= maxObjects {
			break
		}
		r := Ref{Num: uint32(num)}
		e, ok := f.xref.entry(r.Num)
		if !ok {
			continue
		}
		r.Gen = uint16(e.gen)
		if e.kind == xrefInStream {
			r.Gen = 0
		}
		d := f.GetDict(r)
		if d == nil || f.GetName(d["Type"]) != "Page" {
			continue
		}
		f.inherit(d, r)
		out = append(out, r)
	}
	if len(out) > 0 {
		f.errorf("page tree unusable; found %d pages by scanning", len(out))
	}
	return out
}
