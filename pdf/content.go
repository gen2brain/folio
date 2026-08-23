package pdf

import (
	"github.com/gen2brain/folio/syntax"
)

// DefaultContentCacheOps is how many operands of parsed content streams a
// document keeps when nothing says otherwise.
const DefaultContentCacheOps = 1 << 18

type contentOp struct {
	kw   syntax.Keyword
	args []syntax.Operand
}

type content struct {
	ops  []contentOp
	args []syntax.Operand
	errs []error
}

// uncacheable marks a stream that is read from its bytes every time.
var uncacheable = &content{}

func (ip *interp) runStream(st *syntax.Stream, data []byte) {
	if c := ip.doc.content(st, data); c != nil {
		ip.replay(c)
		return
	}
	ip.run(data)
}

func (ip *interp) replay(c *content) {
	for i := range c.ops {
		if *ip.ops++; *ip.ops > maxOperations {
			ip.errorf("page exceeds %d operations", maxOperations)
			return
		}
		ip.op(c.ops[i].kw, c.ops[i].args)
	}
	for _, e := range c.errs {
		ip.doc.errorf("content stream: %v", e)
	}
}

// content returns the parsed form of a content stream, nil the first time it
// is asked for and whenever the cache is full or the stream cannot be kept.
func (d *Document) content(st *syntax.Stream, data []byte) *content {
	if st == nil || d.ContentCacheOps < 0 {
		return nil
	}
	d.mu.Lock()
	c, seen := d.contents[st]
	if !seen {
		if d.contents == nil {
			d.contents = map[*syntax.Stream]*content{}
		}
		d.contents[st] = nil
		d.mu.Unlock()
		return nil
	}
	full := d.contentOps >= d.contentBudget()
	d.mu.Unlock()
	switch {
	case c == uncacheable:
		return nil
	case c != nil:
		return c
	case full:
		return nil
	}

	c = d.parseContent(data)
	d.mu.Lock()
	if kept := d.contents[st]; kept != nil {
		c = kept
	} else if d.contentOps < d.contentBudget() {
		d.contents[st] = c
		d.contentOps += len(c.args) + len(c.ops)
	}
	d.mu.Unlock()
	if c == uncacheable {
		return nil
	}
	return c
}

func (d *Document) contentBudget() int {
	if d.ContentCacheOps == 0 {
		return DefaultContentCacheOps
	}
	return d.ContentCacheOps
}

// parseContent reads a content stream into the operators it runs. One
// carrying an inline image is not kept.
func (d *Document) parseContent(data []byte) *content {
	l := syntax.NewLexer(data)
	p := syntax.NewParser(l, d.f)
	p.AllowStreams(false)

	c := &content{}
	start := 0
	for {
		op, ok := p.Operand()
		if !ok {
			break
		}
		kw, isKw := op.Obj.(syntax.Keyword)
		if !isKw {
			if len(c.args)-start < maxOperands {
				c.args = append(c.args, op)
			}
			continue
		}
		if kw == "BI" {
			return uncacheable
		}
		c.ops = append(c.ops, contentOp{kw: kw, args: c.args[start:len(c.args)]})
		start = len(c.args)
	}
	at := 0
	for i := range c.ops {
		k := len(c.ops[i].args)
		c.ops[i].args = c.args[at : at+k : at+k]
		at += k
	}
	c.errs = l.Errors()
	return c
}
