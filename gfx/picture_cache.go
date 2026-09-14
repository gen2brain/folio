package gfx

import (
	"bytes"
	"image"
	"sync"

	"github.com/gen2brain/folio/raster"
)

// DefaultPictureCacheBytes is how many decoded samples a PictureCache keeps
// when its Bytes is zero.
const DefaultPictureCacheBytes = 1 << 26

// PictureCache holds the decoded pixels of the pictures opened through it,
// least recently drawn first to go. It is safe for concurrent use.
type PictureCache struct {
	// Bytes bounds the decoded samples kept. Zero is DefaultPictureCacheBytes,
	// negative is no cache at all.
	Bytes int

	mu         sync.Mutex
	entries    map[pictureKey]*pictureEntry
	head, tail *pictureEntry
	used       int
}

type pictureKey struct {
	pic   *Picture
	model raster.Model
}

type pictureEntry struct {
	key        pictureKey
	px         *raster.Pixmap
	size       int
	prev, next *pictureEntry
}

// Open reads how big a picture is and keeps b, which must not change
// afterwards, decoding the pixels only when a device draws the picture.
func (c *PictureCache) Open(b []byte) (*Picture, error) {
	w, h, err := pictureSize(b)
	if err != nil {
		return nil, err
	}
	return &Picture{raw: b, cache: c, W: w, H: h}, nil
}

// Purge drops every decoded picture the cache holds.
func (c *PictureCache) Purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries, c.head, c.tail, c.used = nil, nil, nil, 0
	c.mu.Unlock()
}

func pictureSize(b []byte) (int, int, error) {
	if dec := pictureDecoder(b); dec != nil {
		img, err := dec(b)
		if err != nil {
			return 0, 0, err
		}
		r := img.Bounds()
		if err := boundPicture(r.Dx(), r.Dy()); err != nil {
			return 0, 0, err
		}
		return r.Dx(), r.Dy(), nil
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return 0, 0, err
	}
	if err := boundPicture(cfg.Width, cfg.Height); err != nil {
		return 0, 0, err
	}
	return cfg.Width, cfg.Height, nil
}

func (c *PictureCache) pixels(p *Picture, cs *ColorSpace) (*raster.Pixmap, error) {
	m := raster.ModelRGB
	if cs != nil {
		m = cs.Model()
	}
	if px := c.get(pictureKey{p, m}); px != nil {
		return px, nil
	}
	base := c.get(pictureKey{p, raster.ModelRGB})
	if base == nil {
		px, err := decodePixmap(p.raw)
		if err != nil {
			return nil, err
		}
		base = c.put(pictureKey{p, raster.ModelRGB}, px)
	}
	if m == raster.ModelRGB {
		return base, nil
	}
	return c.put(pictureKey{p, m}, convertPixmap(base, m)), nil
}

func (c *PictureCache) get(k pictureKey) *raster.Pixmap {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e := c.entries[k]
	if e == nil {
		return nil
	}
	c.unlink(e)
	c.link(e)
	return e.px
}

func (c *PictureCache) put(k pictureKey, px *raster.Pixmap) *raster.Pixmap {
	if c == nil {
		return px
	}
	limit := c.Bytes
	if limit == 0 {
		limit = DefaultPictureCacheBytes
	}
	size := len(px.Samples) + 64
	if limit < 0 || size > limit {
		return px
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if e := c.entries[k]; e != nil {
		c.unlink(e)
		c.link(e)
		return e.px
	}
	if c.entries == nil {
		c.entries = map[pictureKey]*pictureEntry{}
	}
	e := &pictureEntry{key: k, px: px, size: size}
	c.entries[k] = e
	c.link(e)
	c.used += size
	for c.used > limit && c.tail != nil && c.tail != e {
		old := c.tail
		c.unlink(old)
		c.used -= old.size
		delete(c.entries, old.key)
	}
	return px
}

func (c *PictureCache) link(e *pictureEntry) {
	e.prev, e.next = nil, c.head
	if c.head != nil {
		c.head.prev = e
	}
	c.head = e
	if c.tail == nil {
		c.tail = e
	}
}

func (c *PictureCache) unlink(e *pictureEntry) {
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
