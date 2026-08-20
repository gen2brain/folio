package raster

import "sync"

// GlyphKey identifies a rendered glyph mask: the font it came from, the glyph
// in it, the transform with the translation taken out, and the subpixel phase
// of the origin in quarter pixels.
type GlyphKey struct {
	Font       any
	GID        int32
	A, B, C, D float32
	SubX, SubY uint8
}

// SubPixels is how many phases of the glyph origin are kept apart. Snapping a
// glyph to whole pixels is visible as uneven spacing.
const SubPixels = 4

// GlyphCache holds rendered glyph masks, bounded by their total size, and
// drops the least recently used when it is full.
type GlyphCache struct {
	// mu guards everything below it. A cache is shared by every goroutine
	// rendering the same document, and both halves of a lookup move the
	// entry to the head of the list, so there is no read-only path to
	// separate out.
	mu         sync.Mutex
	max, used  int
	m          map[GlyphKey]*glyphEntry
	head, tail *glyphEntry
}

type glyphEntry struct {
	key        GlyphKey
	mask       *Pixmap
	size       int
	prev, next *glyphEntry
}

// NewGlyphCache returns a cache holding at most max bytes of masks.
func NewGlyphCache(max int) *GlyphCache {
	if max <= 0 {
		max = 1 << 22
	}
	return &GlyphCache{max: max, m: map[GlyphKey]*glyphEntry{}}
}

// Get returns a cached mask, nil when there is none.
func (c *GlyphCache) Get(k GlyphKey) *Pixmap {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.m[k]
	if e == nil {
		return nil
	}
	c.unlink(e)
	c.link(e)
	return e.mask
}

// Put adds a mask, which the cache then owns. A nil mask records that the
// glyph draws nothing, which is worth remembering too.
func (c *GlyphCache) Put(k GlyphKey, mask *Pixmap) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.m[k]; e != nil {
		c.unlink(e)
		c.used -= e.size
		delete(c.m, k)
	}
	size := 64
	if mask != nil {
		size += len(mask.Samples)
	}
	if size > c.max {
		return
	}
	e := &glyphEntry{key: k, mask: mask, size: size}
	c.m[k] = e
	c.link(e)
	c.used += size
	for c.used > c.max && c.tail != nil {
		old := c.tail
		c.unlink(old)
		c.used -= old.size
		delete(c.m, old.key)
	}
}

// Clear empties the cache.
func (c *GlyphCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m = map[GlyphKey]*glyphEntry{}
	c.head, c.tail = nil, nil
	c.used = 0
}

func (c *GlyphCache) link(e *glyphEntry) {
	e.prev, e.next = nil, c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *GlyphCache) unlink(e *glyphEntry) {
	if e.prev != nil {
		e.prev.next = e.next
	} else {
		c.head = e.next
	}
	if e.next != nil {
		e.next.prev = e.prev
	} else {
		c.tail = e.prev
	}
	e.prev, e.next = nil, nil
}
