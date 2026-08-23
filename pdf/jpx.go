package pdf

import (
	"fmt"

	"github.com/gen2brain/folio/syntax"
)

// JPEG 2000 decoding, ITU-T T.800, ported from pdf.js with OpenJPEG read for
// the parts it leaves out. The codestream carries its own geometry and
// precision, which ISO 32000-1 7.4.9 says wins over the image dictionary.

type jpxComponent struct {
	precision      int
	signed         bool
	xr, yr         int
	x0, x1, y0, y1 int
	w, h           int
}

type jpxSIZ struct {
	xsiz, ysiz, xosiz, yosiz     int
	xtsiz, ytsiz, xtosiz, ytosiz int
	csiz                         int
}

type jpxPrecinctSize struct{ ppx, ppy int }

// jpxCOD is a coding style, from COD or COC.
type jpxCOD struct {
	customPrecincts bool
	sop, eph        bool
	progression     int
	layers          int
	mct             int
	levels          int
	xcb, ycb        int
	bypass          bool
	resetContexts   bool
	terminateAll    bool
	verticalStripe  bool
	predictable     bool
	segmentSymbol   bool
	reversible      bool
	precincts       []jpxPrecinctSize
}

type jpxSpqcd struct{ epsilon, mu int }

// jpxQuant is a quantization style, from QCD or QCC.
type jpxQuant struct {
	noQuantization  bool
	scalarExpounded bool
	guardBits       int
	spqcds          []jpxSpqcd
}

type jpxCodeblock struct {
	cbx, cby               int
	tbx0, tby0, tbx1, tby1 int
	x0, y0, x1, y1         int
	precinctNumber         int
	precinct               *jpxPrecinct
	subband                string
	lblock                 int
	seen                   bool
	zeroBitPlanes          int
	passes                 int
	chunks                 []jpxChunk
}

// jpxChunk is one codeword segment of a codeblock. Without the bypass or
// terminate all styles a codeblock is one segment however many packets it
// arrives in, so consecutive chunks that do not terminate are read as one.
type jpxChunk struct {
	data       []byte
	passes     int
	terminated bool
	raw        bool
}

type jpxPrecinct struct {
	cbxMin, cbyMin, cbxMax, cbyMax int
	inclusion                      *jpxTagTree
	zeroBitPlanes                  *jpxTagTree
}

type jpxSubband struct {
	kind                   string
	tbx0, tby0, tbx1, tby1 int
	resolution             *jpxResolution
	codeblocks             []*jpxCodeblock
	precincts              map[int]*jpxPrecinct
}

type jpxPrecinctParams struct {
	width, height           int
	wide, high, count       int
	widthInSub, heightInSub int
}

type jpxResolution struct {
	trx0, try0, trx1, try1 int
	level                  int
	subbands               []*jpxSubband
	prec                   jpxPrecinctParams
	// byPrecinct lists each precinct's codeblocks in the order B.10.8
	// gives them, which is what a packet carries.
	byPrecinct [][]*jpxCodeblock
}

type jpxTileComp struct {
	tcx0, tcy0, tcx1, tcy1 int
	w, h                   int
	cod                    *jpxCOD
	quant                  *jpxQuant
	resolutions            []*jpxResolution
	subbands               []*jpxSubband
}

type jpxTile struct {
	tx0, ty0, tx1, ty1 int
	w, h               int
	components         []*jpxTileComp
	cod                *jpxCOD
	packets            jpxPackets
}

// jpxTilePart is one SOT header and the coding styles in force inside it.
type jpxTilePart struct {
	index     int
	dataEnd   int
	partIndex int
	cod       *jpxCOD
	coc       map[int]*jpxCOD
	qcd       *jpxQuant
	qcc       map[int]*jpxQuant
}

type jpxContext struct {
	siz        jpxSIZ
	components []*jpxComponent
	tiles      []*jpxTile
	mainHeader bool
	cod        *jpxCOD
	coc        map[int]*jpxCOD
	qcd        *jpxQuant
	qcc        map[int]*jpxQuant
	current    *jpxTilePart
	// indexed keeps the samples at the precision the codestream carries,
	// which is what an image whose color space is Indexed reads them at.
	indexed bool
}

// maxJPXBlocks bounds the code blocks one tile may be divided into, so that a
// header a few bytes long cannot ask for a structure the image cannot hold.
const maxJPXBlocks = 1 << 20

// jpxImage is one decoded codestream: interleaved samples, one byte a
// component, plus what the codestream says about itself.
type jpxImage struct {
	width, height int
	comps         int
	pix           []byte
}

func jpxCeilDiv(a, b int) int {
	if b <= 0 {
		return 0
	}
	if a >= 0 {
		return (a + b - 1) / b
	}
	return -((-a) / b)
}

// jpxCeilHalf is ceil(x/s - 1/2), which places the high pass subbands.
func jpxCeilHalf(x, s int) int {
	return jpxCeilDiv(2*x-s, 2*s)
}

func jpxLog2(x int) int {
	n := 0
	for 1<<uint(n) < x {
		n++
	}
	return n
}

func jpxByte(data []byte, i int) int {
	if i < 0 || i >= len(data) {
		return 0
	}
	return int(data[i])
}

func jpxU16(data []byte, i int) int {
	if i < 0 || i+2 > len(data) {
		return 0
	}
	return int(data[i])<<8 | int(data[i+1])
}

func jpxU32(data []byte, i int) int {
	if i < 0 || i+4 > len(data) {
		return 0
	}
	return int(data[i])<<24 | int(data[i+1])<<16 | int(data[i+2])<<8 | int(data[i+3])
}

// Section B.10.2 Tag trees. A node holds the value decoded so far and
// whether that value is final; decoding stops at a threshold and resumes in a
// later layer where it left off, which is what lets an empty packet carry no
// bits at all.
type jpxTagNode struct {
	value int
	known bool
}

type jpxTagLevel struct {
	w, h  int
	nodes []jpxTagNode
}

type jpxTagTree struct {
	levels []jpxTagLevel
	path   [32]int
}

func newJPXTagTree(w, h int) *jpxTagTree {
	t := &jpxTagTree{}
	for n := jpxLog2(max(w, h)) + 1; n > 0 && len(t.levels) < 32; n-- {
		t.levels = append(t.levels, jpxTagLevel{w: w, h: h, nodes: make([]jpxTagNode, w*h)})
		w, h = jpxCeilDiv(w, 2), jpxCeilDiv(h, 2)
	}
	return t
}

// decode reads the value at (i, j), stopping once it is known or has reached
// threshold. It reports whether the value is final.
func (t *jpxTagTree) decode(r *jpxPacketReader, i, j, threshold int) (int, bool) {
	n := len(t.levels)
	for l := 0; l < n; l++ {
		t.path[l] = i + j*t.levels[l].w
		i, j = i>>1, j>>1
	}
	lo := 0
	for l := n - 1; l >= 0; l-- {
		index := t.path[l]
		if index < 0 || index >= len(t.levels[l].nodes) {
			return lo, false
		}
		node := &t.levels[l].nodes[index]
		if node.value < lo {
			node.value = lo
		}
		for !node.known && node.value < threshold {
			if r.readBits(1) != 0 {
				node.known = true
			} else {
				node.value++
			}
			if r.overflow {
				return node.value, false
			}
		}
		lo = node.value
		if !node.known {
			return lo, false
		}
	}
	return lo, true
}

// jpxPacket names the codeblocks one packet carries, Section B.10.8.
type jpxPacket struct {
	layer      int
	codeblocks []*jpxCodeblock
}

type jpxPackets interface {
	next() (*jpxPacket, bool)
}

func jpxCreatePacket(res *jpxResolution, precinct, layer int) *jpxPacket {
	p := &jpxPacket{layer: layer}
	if precinct >= 0 && precinct < len(res.byPrecinct) {
		p.codeblocks = res.byPrecinct[precinct]
	}
	return p
}

func (res *jpxResolution) index() {
	res.byPrecinct = make([][]*jpxCodeblock, res.prec.count)
	for _, sb := range res.subbands {
		for _, cb := range sb.codeblocks {
			if n := cb.precinctNumber; n >= 0 && n < len(res.byPrecinct) {
				res.byPrecinct[n] = append(res.byPrecinct[n], cb)
			}
		}
	}
}

func jpxMaxLevels(tile *jpxTile) int {
	n := 0
	for _, c := range tile.components {
		n = max(n, c.cod.levels)
	}
	return n
}

// B.12.1.1 Layer-resolution-component-position.
type jpxLRCP struct {
	tile          *jpxTile
	layers, comps int
	maxLevels     int
	l, r, i, k    int
}

func (it *jpxLRCP) next() (*jpxPacket, bool) {
	for ; it.l < it.layers; it.l++ {
		for ; it.r <= it.maxLevels; it.r++ {
			for ; it.i < it.comps; it.i++ {
				c := it.tile.components[it.i]
				if it.r > c.cod.levels {
					continue
				}
				res := c.resolutions[it.r]
				if it.k < res.prec.count {
					p := jpxCreatePacket(res, it.k, it.l)
					it.k++
					return p, true
				}
				it.k = 0
			}
			it.i = 0
		}
		it.r = 0
	}
	return nil, false
}

// B.12.1.2 Resolution-layer-component-position.
type jpxRLCP struct {
	tile          *jpxTile
	layers, comps int
	maxLevels     int
	r, l, i, k    int
}

func (it *jpxRLCP) next() (*jpxPacket, bool) {
	for ; it.r <= it.maxLevels; it.r++ {
		for ; it.l < it.layers; it.l++ {
			for ; it.i < it.comps; it.i++ {
				c := it.tile.components[it.i]
				if it.r > c.cod.levels {
					continue
				}
				res := c.resolutions[it.r]
				if it.k < res.prec.count {
					p := jpxCreatePacket(res, it.k, it.l)
					it.k++
					return p, true
				}
				it.k = 0
			}
			it.i = 0
		}
		it.l = 0
	}
	return nil, false
}

// B.12.1.3 Resolution-position-component-layer.
type jpxRPCL struct {
	tile          *jpxTile
	layers, comps int
	maxLevels     int
	maxPrecincts  []int
	r, p, c, l    int
}

func (it *jpxRPCL) next() (*jpxPacket, bool) {
	for ; it.r <= it.maxLevels; it.r++ {
		for ; it.p < it.maxPrecincts[it.r]; it.p++ {
			for ; it.c < it.comps; it.c++ {
				comp := it.tile.components[it.c]
				if it.r > comp.cod.levels {
					continue
				}
				res := comp.resolutions[it.r]
				if it.p >= res.prec.count {
					continue
				}
				if it.l < it.layers {
					pk := jpxCreatePacket(res, it.p, it.l)
					it.l++
					return pk, true
				}
				it.l = 0
			}
			it.c = 0
		}
		it.p = 0
	}
	return nil, false
}

type jpxScaleSize struct{ w, h int }

type jpxCompScale struct {
	resolutions      []jpxScaleSize
	minW, minH       int
	maxWide, maxHigh int
}

type jpxScale struct {
	components       []jpxCompScale
	minW, minH       int
	maxWide, maxHigh int
}

func jpxPrecinctScale(tile *jpxTile) jpxScale {
	s := jpxScale{minW: 1 << 30, minH: 1 << 30}
	for _, comp := range tile.components {
		cs := jpxCompScale{minW: 1 << 30, minH: 1 << 30}
		cs.resolutions = make([]jpxScaleSize, comp.cod.levels+1)
		scale := 1
		for r := comp.cod.levels; r >= 0; r-- {
			res := comp.resolutions[r]
			w := scale * res.prec.width
			h := scale * res.prec.height
			cs.minW = min(cs.minW, w)
			cs.minH = min(cs.minH, h)
			cs.maxWide = max(cs.maxWide, res.prec.wide)
			cs.maxHigh = max(cs.maxHigh, res.prec.high)
			cs.resolutions[r] = jpxScaleSize{w, h}
			scale <<= 1
		}
		s.minW = min(s.minW, cs.minW)
		s.minH = min(s.minH, cs.minH)
		s.maxWide = max(s.maxWide, cs.maxWide)
		s.maxHigh = max(s.maxHigh, cs.maxHigh)
		s.components = append(s.components, cs)
	}
	return s
}

func jpxPrecinctIndex(px, py int, size jpxScaleSize, minW, minH int, res *jpxResolution) (int, bool) {
	x := px * minW
	y := py * minH
	if size.w == 0 || size.h == 0 || x%size.w != 0 || y%size.h != 0 {
		return 0, false
	}
	return x/size.w + (y/size.h)*res.prec.wide, true
}

// B.12.1.4 Position-component-resolution-layer.
type jpxPCRL struct {
	tile            *jpxTile
	layers, comps   int
	scale           jpxScale
	l, r, c, px, py int
}

func (it *jpxPCRL) next() (*jpxPacket, bool) {
	for ; it.py < it.scale.maxHigh; it.py++ {
		for ; it.px < it.scale.maxWide; it.px++ {
			for ; it.c < it.comps; it.c++ {
				comp := it.tile.components[it.c]
				for ; it.r <= comp.cod.levels; it.r++ {
					res := comp.resolutions[it.r]
					k, ok := jpxPrecinctIndex(it.px, it.py, it.scale.components[it.c].resolutions[it.r],
						it.scale.minW, it.scale.minH, res)
					if !ok {
						continue
					}
					if it.l < it.layers {
						pk := jpxCreatePacket(res, k, it.l)
						it.l++
						return pk, true
					}
					it.l = 0
				}
				it.r = 0
			}
			it.c = 0
		}
		it.px = 0
	}
	return nil, false
}

// B.12.1.5 Component-position-resolution-layer.
type jpxCPRL struct {
	tile            *jpxTile
	layers, comps   int
	scale           jpxScale
	l, r, c, px, py int
}

func (it *jpxCPRL) next() (*jpxPacket, bool) {
	for ; it.c < it.comps; it.c++ {
		comp := it.tile.components[it.c]
		cs := it.scale.components[it.c]
		for ; it.py < cs.maxHigh; it.py++ {
			for ; it.px < cs.maxWide; it.px++ {
				for ; it.r <= comp.cod.levels; it.r++ {
					res := comp.resolutions[it.r]
					k, ok := jpxPrecinctIndex(it.px, it.py, cs.resolutions[it.r], cs.minW, cs.minH, res)
					if !ok {
						continue
					}
					if it.l < it.layers {
						pk := jpxCreatePacket(res, k, it.l)
						it.l++
						return pk, true
					}
					it.l = 0
				}
				it.r = 0
			}
			it.px = 0
		}
		it.py = 0
	}
	return nil, false
}

func jpxComponentDims(c *jpxComponent, siz *jpxSIZ) {
	c.x0 = jpxCeilDiv(siz.xosiz, c.xr)
	c.x1 = jpxCeilDiv(siz.xsiz, c.xr)
	c.y0 = jpxCeilDiv(siz.yosiz, c.yr)
	c.y1 = jpxCeilDiv(siz.ysiz, c.yr)
	c.w = c.x1 - c.x0
	c.h = c.y1 - c.y0
}

// B.3 Division into tiles and tile-components.
func jpxTileGrids(ctx *jpxContext) error {
	siz := &ctx.siz
	if siz.xtsiz <= 0 || siz.ytsiz <= 0 {
		return fmt.Errorf("%w: JPX tile is %dx%d", ErrInvalid, siz.xtsiz, siz.ytsiz)
	}
	nx := jpxCeilDiv(siz.xsiz-siz.xtosiz, siz.xtsiz)
	ny := jpxCeilDiv(siz.ysiz-siz.ytosiz, siz.ytsiz)
	if nx <= 0 || ny <= 0 || nx > 1<<20 || ny > 1<<20 || nx*ny > 1<<20 {
		return fmt.Errorf("%w: JPX has %dx%d tiles", ErrInvalid, nx, ny)
	}
	for q := 0; q < ny; q++ {
		for p := 0; p < nx; p++ {
			t := &jpxTile{
				tx0: max(siz.xtosiz+p*siz.xtsiz, siz.xosiz),
				ty0: max(siz.ytosiz+q*siz.ytsiz, siz.yosiz),
				tx1: min(siz.xtosiz+(p+1)*siz.xtsiz, siz.xsiz),
				ty1: min(siz.ytosiz+(q+1)*siz.ytsiz, siz.ysiz),
			}
			t.w, t.h = t.tx1-t.tx0, t.ty1-t.ty0
			for range ctx.components {
				t.components = append(t.components, &jpxTileComp{})
			}
			ctx.tiles = append(ctx.tiles, t)
		}
	}
	for i, c := range ctx.components {
		for _, t := range ctx.tiles {
			tc := t.components[i]
			tc.tcx0 = jpxCeilDiv(t.tx0, c.xr)
			tc.tcy0 = jpxCeilDiv(t.ty0, c.yr)
			tc.tcx1 = jpxCeilDiv(t.tx1, c.xr)
			tc.tcy1 = jpxCeilDiv(t.ty1, c.yr)
			tc.w, tc.h = tc.tcx1-tc.tcx0, tc.tcy1-tc.tcy0
		}
	}
	return nil
}

// B.7 Division of a subband into codeblocks, with the precinct sizes B.6 says
// the resolution is divided by.
func jpxBlockDims(cod *jpxCOD, r int) (ppx, ppy, xcb, ycb int) {
	ppx, ppy = 15, 15
	if cod.customPrecincts {
		if r < len(cod.precincts) {
			ppx, ppy = cod.precincts[r].ppx, cod.precincts[r].ppy
		} else if len(cod.precincts) > 0 {
			last := cod.precincts[len(cod.precincts)-1]
			ppx, ppy = last.ppx, last.ppy
		}
	}
	if r > 0 {
		xcb, ycb = min(cod.xcb, ppx-1), min(cod.ycb, ppy-1)
	} else {
		xcb, ycb = min(cod.xcb, ppx), min(cod.ycb, ppy)
	}
	return
}

func jpxBuildPrecincts(res *jpxResolution, ppx, ppy int) {
	pw, ph := 1<<uint(ppx), 1<<uint(ppy)
	shift := 1
	if res.level == 0 {
		shift = 0
	}
	p := jpxPrecinctParams{
		width:       pw,
		height:      ph,
		widthInSub:  1 << uint(ppx-shift),
		heightInSub: 1 << uint(ppy-shift),
	}
	if res.trx1 > res.trx0 {
		p.wide = jpxCeilDiv(res.trx1, pw) - res.trx0/pw
	}
	if res.try1 > res.try0 {
		p.high = jpxCeilDiv(res.try1, ph) - res.try0/ph
	}
	p.count = p.wide * p.high
	res.prec = p
}

func jpxBuildCodeblocks(sb *jpxSubband, xcb, ycb int) {
	cw, ch := 1<<uint(xcb), 1<<uint(ycb)
	cbx0, cby0 := sb.tbx0>>uint(xcb), sb.tby0>>uint(ycb)
	cbx1 := (sb.tbx1 + cw - 1) >> uint(xcb)
	cby1 := (sb.tby1 + ch - 1) >> uint(ycb)
	prec := &sb.resolution.prec
	sb.precincts = map[int]*jpxPrecinct{}
	for j := cby0; j < cby1; j++ {
		for i := cbx0; i < cbx1; i++ {
			cb := &jpxCodeblock{
				cbx: i, cby: j,
				tbx0: cw * i, tby0: ch * j,
				tbx1: cw * (i + 1), tby1: ch * (j + 1),
				subband: sb.kind,
				lblock:  3,
			}
			cb.x0 = max(sb.tbx0, cb.tbx0)
			cb.y0 = max(sb.tby0, cb.tby0)
			cb.x1 = min(sb.tbx1, cb.tbx1)
			cb.y1 = min(sb.tby1, cb.tby1)
			if cb.x1 <= cb.x0 || cb.y1 <= cb.y0 {
				continue
			}
			pi := (cb.x0 - sb.tbx0) / prec.widthInSub
			pj := (cb.y0 - sb.tby0) / prec.heightInSub
			cb.precinctNumber = pi + pj*prec.wide
			sb.codeblocks = append(sb.codeblocks, cb)

			p := sb.precincts[cb.precinctNumber]
			if p == nil {
				p = &jpxPrecinct{cbxMin: i, cbyMin: j, cbxMax: i, cbyMax: j}
				sb.precincts[cb.precinctNumber] = p
			} else {
				p.cbxMin = min(p.cbxMin, i)
				p.cbxMax = max(p.cbxMax, i)
				p.cbyMin = min(p.cbyMin, j)
				p.cbyMax = max(p.cbyMax, j)
			}
			cb.precinct = p
		}
	}
}

// B.5 Resolution levels and subbands.
func jpxBuildPackets(ctx *jpxContext, tile *jpxTile) error {
	blocks := 0
	for _, comp := range tile.components {
		levels := comp.cod.levels
		if levels < 0 || levels > 32 {
			return fmt.Errorf("%w: JPX has %d decomposition levels", ErrInvalid, levels)
		}
		comp.resolutions = nil
		comp.subbands = nil
		for r := 0; r <= levels; r++ {
			ppx, ppy, xcb, ycb := jpxBlockDims(comp.cod, r)
			scale := 1 << uint(levels-r)
			res := &jpxResolution{
				trx0:  jpxCeilDiv(comp.tcx0, scale),
				try0:  jpxCeilDiv(comp.tcy0, scale),
				trx1:  jpxCeilDiv(comp.tcx1, scale),
				try1:  jpxCeilDiv(comp.tcy1, scale),
				level: r,
			}
			jpxBuildPrecincts(res, ppx, ppy)
			comp.resolutions = append(comp.resolutions, res)

			if r == 0 {
				sb := &jpxSubband{
					kind: "LL", resolution: res,
					tbx0: jpxCeilDiv(comp.tcx0, scale), tby0: jpxCeilDiv(comp.tcy0, scale),
					tbx1: jpxCeilDiv(comp.tcx1, scale), tby1: jpxCeilDiv(comp.tcy1, scale),
				}
				jpxBuildCodeblocks(sb, xcb, ycb)
				res.subbands = []*jpxSubband{sb}
				comp.subbands = append(comp.subbands, sb)
				blocks += len(sb.codeblocks)
				res.index()
				continue
			}
			b := 1 << uint(levels-r+1)
			for _, kind := range []string{"HL", "LH", "HH"} {
				sb := &jpxSubband{kind: kind, resolution: res}
				switch kind {
				case "HL":
					sb.tbx0, sb.tbx1 = jpxCeilHalf(comp.tcx0, b), jpxCeilHalf(comp.tcx1, b)
					sb.tby0, sb.tby1 = jpxCeilDiv(comp.tcy0, b), jpxCeilDiv(comp.tcy1, b)
				case "LH":
					sb.tbx0, sb.tbx1 = jpxCeilDiv(comp.tcx0, b), jpxCeilDiv(comp.tcx1, b)
					sb.tby0, sb.tby1 = jpxCeilHalf(comp.tcy0, b), jpxCeilHalf(comp.tcy1, b)
				case "HH":
					sb.tbx0, sb.tbx1 = jpxCeilHalf(comp.tcx0, b), jpxCeilHalf(comp.tcx1, b)
					sb.tby0, sb.tby1 = jpxCeilHalf(comp.tcy0, b), jpxCeilHalf(comp.tcy1, b)
				}
				jpxBuildCodeblocks(sb, xcb, ycb)
				res.subbands = append(res.subbands, sb)
				comp.subbands = append(comp.subbands, sb)
				blocks += len(sb.codeblocks)
			}
			res.index()
			if blocks > maxJPXBlocks {
				return fmt.Errorf("%w: JPX tile has more than %d code blocks", ErrInvalid, maxJPXBlocks)
			}
		}
	}

	layers := tile.cod.layers
	comps := len(tile.components)
	switch tile.cod.progression {
	case 0:
		tile.packets = &jpxLRCP{tile: tile, layers: layers, comps: comps, maxLevels: jpxMaxLevels(tile)}
	case 1:
		tile.packets = &jpxRLCP{tile: tile, layers: layers, comps: comps, maxLevels: jpxMaxLevels(tile)}
	case 2:
		maxLevels := jpxMaxLevels(tile)
		maxPrec := make([]int, maxLevels+1)
		for r := 0; r <= maxLevels; r++ {
			for _, c := range tile.components {
				if r < len(c.resolutions) {
					maxPrec[r] = max(maxPrec[r], c.resolutions[r].prec.count)
				}
			}
		}
		tile.packets = &jpxRPCL{tile: tile, layers: layers, comps: comps, maxLevels: maxLevels, maxPrecincts: maxPrec}
	case 3:
		tile.packets = &jpxPCRL{tile: tile, layers: layers, comps: comps, scale: jpxPrecinctScale(tile)}
	case 4:
		tile.packets = &jpxCPRL{tile: tile, layers: layers, comps: comps, scale: jpxPrecinctScale(tile)}
	default:
		return fmt.Errorf("%w: JPX progression order %d", ErrUnsupported, tile.cod.progression)
	}
	return nil
}

// jpxPassType is the pass a codeblock's nth coding pass runs, D.2: the first
// bit plane starts at the cleanup pass and the three then repeat.
func jpxPassType(n int) int { return (n + 2) % 3 }

const (
	jpxSignificance = iota
	jpxRefinement
	jpxCleanup
)

// jpxRawPass reports whether the bypass style codes this pass as raw bits
// rather than arithmetically, D.6.
func jpxRawPass(n int, bypass bool) bool {
	return bypass && n >= 10 && jpxPassType(n) != jpxCleanup
}

// jpxTerminates reports whether a codeword segment ends after pass n.
func jpxTerminates(n int, bypass, termall bool) bool {
	if termall {
		return true
	}
	if !bypass {
		return false
	}
	return n == 9 || n >= 10 && jpxPassType(n) != jpxSignificance
}

// jpxSegments splits n passes starting at pass start into codeword segments,
// which B.10.7.2 gives a length each.
func jpxSegments(start, n int, bypass, termall bool) []int {
	if !bypass && !termall {
		return []int{n}
	}
	var out []int
	c := 0
	for i := 0; i < n; i++ {
		c++
		if jpxTerminates(start+i, bypass, termall) {
			out = append(out, c)
			c = 0
		}
	}
	if c > 0 {
		out = append(out, c)
	}
	return out
}

// jpxPacketReader reads a tile-part's packet headers, which are bit stuffed:
// a zero bit follows every 0xff byte, B.10.1.
type jpxPacketReader struct {
	data     []byte
	pos      int
	buf      uint32
	bits     int
	stuffed  bool
	overflow bool
}

func (r *jpxPacketReader) readBits(n int) uint32 {
	for r.bits < n {
		if r.pos >= len(r.data) {
			r.overflow = true
			r.buf <<= 8
			r.bits += 8
			continue
		}
		b := r.data[r.pos]
		r.pos++
		if r.stuffed {
			r.buf = r.buf<<7 | uint32(b)
			r.bits += 7
			r.stuffed = false
		} else {
			r.buf = r.buf<<8 | uint32(b)
			r.bits += 8
		}
		if b == 0xff {
			r.stuffed = true
		}
	}
	r.bits -= n
	if n >= 32 {
		return r.buf
	}
	return (r.buf >> uint(r.bits)) & (1<<uint(n) - 1)
}

func (r *jpxPacketReader) align() {
	r.bits = 0
	if r.stuffed {
		r.pos++
		r.stuffed = false
	}
}

func (r *jpxPacketReader) skipMarker(value byte) bool {
	if r.pos > 0 && r.pos < len(r.data) && r.data[r.pos-1] == 0xff && r.data[r.pos] == value {
		r.pos++
		return true
	}
	if r.pos+1 < len(r.data) && r.data[r.pos] == 0xff && r.data[r.pos+1] == value {
		r.pos += 2
		return true
	}
	return false
}

// B.10.6 Number of coding passes.
func (r *jpxPacketReader) codingpasses() int {
	if r.readBits(1) == 0 {
		return 1
	}
	if r.readBits(1) == 0 {
		return 2
	}
	if v := r.readBits(2); v < 3 {
		return int(v) + 3
	}
	if v := r.readBits(5); v < 31 {
		return int(v) + 6
	}
	return int(r.readBits(7)) + 37
}

type jpxQueued struct {
	cb         *jpxCodeblock
	passes     int
	length     int
	terminated bool
	raw        bool
}

// B.10 Packet header decoding.
func jpxParsePackets(ctx *jpxContext, tile *jpxTile, data []byte) error {
	r := &jpxPacketReader{data: data}
	sop, eph := tile.cod.sop, tile.cod.eph
	bypass, termall := tile.cod.bypass, tile.cod.terminateAll
	var queue []jpxQueued
	for r.pos < len(data) {
		r.align()
		if sop && r.skipMarker(0x91) {
			r.pos += 4
		}
		packet, ok := tile.packets.next()
		if !ok {
			break
		}
		// An empty packet still carries its end of packet header
		// marker, B.10.1, so the tail of the loop has to run for it.
		empty := r.readBits(1) == 0
		queue = queue[:0]
		for _, cb := range packet.codeblocks {
			if empty {
				break
			}
			precinct := cb.precinct
			col := cb.cbx - precinct.cbxMin
			row := cb.cby - precinct.cbyMin
			included, first := false, false
			if cb.seen {
				included = r.readBits(1) != 0
			} else {
				if precinct.inclusion == nil {
					w := precinct.cbxMax - precinct.cbxMin + 1
					h := precinct.cbyMax - precinct.cbyMin + 1
					precinct.inclusion = newJPXTagTree(w, h)
					precinct.zeroBitPlanes = newJPXTagTree(w, h)
				}
				_, known := precinct.inclusion.decode(r, col, row, packet.layer+1)
				if known {
					cb.seen = true
					included, first = true, true
				}
			}
			if !included {
				continue
			}
			if first {
				cb.zeroBitPlanes, _ = precinct.zeroBitPlanes.decode(r, col, row, 1<<30)
			}
			passes := r.codingpasses()
			for r.readBits(1) != 0 {
				cb.lblock++
				if cb.lblock > 32 {
					return fmt.Errorf("%w: JPX code block length runs away", ErrInvalid)
				}
			}
			start := cb.passes
			for _, np := range jpxSegments(start, passes, bypass, termall) {
				n := jpxLog2(np)
				if np < 1<<uint(n) {
					n--
				}
				queue = append(queue, jpxQueued{
					cb:         cb,
					passes:     np,
					length:     int(r.readBits(n + cb.lblock)),
					terminated: jpxTerminates(start+np-1, bypass, termall),
					raw:        jpxRawPass(start, bypass),
				})
				start += np
			}
			cb.passes += passes
			if r.overflow {
				return nil
			}
		}
		r.align()
		if eph {
			r.skipMarker(0x92)
		}
		for _, q := range queue {
			if r.pos+q.length > len(data) {
				return nil
			}
			q.cb.chunks = append(q.cb.chunks, jpxChunk{
				data:       data[r.pos : r.pos+q.length],
				passes:     q.passes,
				terminated: q.terminated,
				raw:        q.raw,
			})
			r.pos += q.length
		}
	}
	return nil
}

// Section D, coefficient bit modeling.
const (
	jpxUniformContext   = 17
	jpxRunLengthContext = 18
)

// Table D-1, indexed by the packed neighbor sums 0dddvvhh.
var jpxLLAndLHLabels = [...]uint8{
	0, 5, 8, 0, 3, 7, 8, 0, 4, 7, 8, 0, 0, 0, 0, 0, 1, 6, 8, 0, 3, 7, 8, 0, 4,
	7, 8, 0, 0, 0, 0, 0, 2, 6, 8, 0, 3, 7, 8, 0, 4, 7, 8, 0, 0, 0, 0, 0, 2, 6,
	8, 0, 3, 7, 8, 0, 4, 7, 8, 0, 0, 0, 0, 0, 2, 6, 8, 0, 3, 7, 8, 0, 4, 7, 8,
}

var jpxHLLabels = [...]uint8{
	0, 3, 4, 0, 5, 7, 7, 0, 8, 8, 8, 0, 0, 0, 0, 0, 1, 3, 4, 0, 6, 7, 7, 0, 8,
	8, 8, 0, 0, 0, 0, 0, 2, 3, 4, 0, 6, 7, 7, 0, 8, 8, 8, 0, 0, 0, 0, 0, 2, 3,
	4, 0, 6, 7, 7, 0, 8, 8, 8, 0, 0, 0, 0, 0, 2, 3, 4, 0, 6, 7, 7, 0, 8, 8, 8,
}

var jpxHHLabels = [...]uint8{
	0, 1, 2, 0, 1, 2, 2, 0, 2, 2, 2, 0, 0, 0, 0, 0, 3, 4, 5, 0, 4, 5, 5, 0, 5,
	5, 5, 0, 0, 0, 0, 0, 6, 7, 7, 0, 7, 7, 7, 0, 7, 7, 7, 0, 0, 0, 0, 0, 8, 8,
	8, 0, 8, 8, 8, 0, 8, 8, 8, 0, 0, 0, 0, 0, 8, 8, 8, 0, 8, 8, 8, 0, 8, 8, 8,
}

const (
	jpxProcessed   = 1
	jpxFirstMagBit = 2
)

type jpxBitModel struct {
	w, h        int
	labels      []uint8
	neighbors   []uint8
	sign        []uint8
	magnitude   []uint32
	flags       []uint8
	bitsDecoded []uint8
	contexts    [19]uint8
	decoder     *syntax.MQ
	raw         *jpxPacketReader
	vsc         bool
}

// bit reads one decision, from the raw bit stream when the bypass style has
// put this pass outside the arithmetic coder.
func (m *jpxBitModel) bit(label uint32) int {
	if m.raw != nil {
		return int(m.raw.readBits(1))
	}
	return m.decoder.ReadBit(m.contexts[:], label)
}

func newJPXBitModel(w, h int, subband string, zeroBitPlanes int) *jpxBitModel {
	m := &jpxBitModel{w: w, h: h}
	switch subband {
	case "HH":
		m.labels = jpxHHLabels[:]
	case "HL":
		m.labels = jpxHLLabels[:]
	default:
		m.labels = jpxLLAndLHLabels[:]
	}
	n := w * h
	m.neighbors = make([]uint8, n)
	m.sign = make([]uint8, n)
	m.magnitude = make([]uint32, n)
	m.flags = make([]uint8, n)
	m.bitsDecoded = make([]uint8, n)
	if zeroBitPlanes != 0 {
		for i := range m.bitsDecoded {
			m.bitsDecoded[i] = uint8(zeroBitPlanes)
		}
	}
	m.reset()
	return m
}

func (m *jpxBitModel) reset() {
	m.contexts = [19]uint8{}
	m.contexts[0] = 4 << 1
	m.contexts[jpxUniformContext] = 46 << 1
	m.contexts[jpxRunLengthContext] = 3 << 1
}

func (m *jpxBitModel) setNeighbors(row, column, index int) {
	left := column > 0
	right := column+1 < m.w
	if row > 0 && !(m.vsc && row&3 == 0) {
		i := index - m.w
		if left {
			m.neighbors[i-1] += 0x10
		}
		if right {
			m.neighbors[i+1] += 0x10
		}
		m.neighbors[i] += 0x04
	}
	if row+1 < m.h {
		i := index + m.w
		if left {
			m.neighbors[i-1] += 0x10
		}
		if right {
			m.neighbors[i+1] += 0x10
		}
		m.neighbors[i] += 0x04
	}
	if left {
		m.neighbors[index-1]++
	}
	if right {
		m.neighbors[index+1]++
	}
	m.neighbors[index] |= 0x80
}

func (m *jpxBitModel) significancePass() {
	for i0 := 0; i0 < m.h; i0 += 4 {
		for j := 0; j < m.w; j++ {
			index := i0*m.w + j
			for i1 := 0; i1 < 4; i1, index = i1+1, index+m.w {
				i := i0 + i1
				if i >= m.h {
					break
				}
				m.flags[index] &^= jpxProcessed
				if m.magnitude[index] != 0 || m.neighbors[index] == 0 {
					continue
				}
				label := m.labels[m.neighbors[index]]
				if m.bit(uint32(label)) != 0 {
					m.sign[index] = m.signBit(i, j, index)
					m.magnitude[index] = 1
					m.setNeighbors(i, j, index)
					m.flags[index] |= jpxFirstMagBit
				}
				m.bitsDecoded[index]++
				m.flags[index] |= jpxProcessed
			}
		}
	}
}

// signBit is the sign of a newly significant coefficient, raw under the
// bypass style and from the sign contexts of D.3.2 otherwise.
func (m *jpxBitModel) signBit(row, column, index int) uint8 {
	if m.raw != nil {
		return uint8(m.raw.readBits(1))
	}
	return m.decodeSign(row, column, index)
}

func (m *jpxBitModel) decodeSign(row, column, index int) uint8 {
	var contribution, sign0, sign1 int
	significant := column > 0 && m.magnitude[index-1] != 0
	switch {
	case column+1 < m.w && m.magnitude[index+1] != 0:
		sign1 = int(m.sign[index+1])
		if significant {
			sign0 = int(m.sign[index-1])
			contribution = 1 - sign1 - sign0
		} else {
			contribution = 1 - sign1 - sign1
		}
	case significant:
		sign0 = int(m.sign[index-1])
		contribution = 1 - sign0 - sign0
	default:
		contribution = 0
	}
	horizontal := 3 * contribution

	significant = row > 0 && m.magnitude[index-m.w] != 0
	switch {
	case row+1 < m.h && !(m.vsc && row&3 == 3) && m.magnitude[index+m.w] != 0:
		sign1 = int(m.sign[index+m.w])
		if significant {
			sign0 = int(m.sign[index-m.w])
			contribution = 1 - sign1 - sign0 + horizontal
		} else {
			contribution = 1 - sign1 - sign1 + horizontal
		}
	case significant:
		sign0 = int(m.sign[index-m.w])
		contribution = 1 - sign0 - sign0 + horizontal
	default:
		contribution = horizontal
	}

	if contribution >= 0 {
		return uint8(m.decoder.ReadBit(m.contexts[:], uint32(9+contribution)))
	}
	return uint8(m.decoder.ReadBit(m.contexts[:], uint32(9-contribution)) ^ 1)
}

func (m *jpxBitModel) refinementPass() {
	length := m.w * m.h
	for index0 := 0; index0 < length; index0 += m.w * 4 {
		next := min(length, index0+m.w*4)
		for j := 0; j < m.w; j++ {
			for index := index0 + j; index < next; index += m.w {
				if m.magnitude[index] == 0 || m.flags[index]&jpxProcessed != 0 {
					continue
				}
				label := 16
				if m.flags[index]&jpxFirstMagBit != 0 {
					m.flags[index] ^= jpxFirstMagBit
					if m.neighbors[index]&127 == 0 {
						label = 15
					} else {
						label = 14
					}
				}
				bit := m.bit(uint32(label))
				if m.magnitude[index] < 1<<31 {
					m.magnitude[index] = m.magnitude[index]<<1 | uint32(bit)
				}
				m.bitsDecoded[index]++
				m.flags[index] |= jpxProcessed
			}
		}
	}
}

func (m *jpxBitModel) cleanupPass() {
	w := m.w
	for i0 := 0; i0 < m.h; i0 += 4 {
		iNext := min(i0+4, m.h)
		base := i0 * w
		checkAll := i0+3 < m.h
		for j := 0; j < w; j++ {
			index0 := base + j
			allEmpty := checkAll &&
				m.flags[index0] == 0 && m.flags[index0+w] == 0 &&
				m.flags[index0+2*w] == 0 && m.flags[index0+3*w] == 0 &&
				m.neighbors[index0] == 0 && m.neighbors[index0+w] == 0 &&
				m.neighbors[index0+2*w] == 0 && m.neighbors[index0+3*w] == 0
			i1, index, i := 0, index0, i0
			if allEmpty {
				if m.decoder.ReadBit(m.contexts[:], jpxRunLengthContext) == 0 {
					m.bitsDecoded[index0]++
					m.bitsDecoded[index0+w]++
					m.bitsDecoded[index0+2*w]++
					m.bitsDecoded[index0+3*w]++
					continue
				}
				i1 = m.decoder.ReadBit(m.contexts[:], jpxUniformContext)<<1 |
					m.decoder.ReadBit(m.contexts[:], jpxUniformContext)
				if i1 != 0 {
					i = i0 + i1
					index += i1 * w
				}
				m.sign[index] = m.decodeSign(i, j, index)
				m.magnitude[index] = 1
				m.setNeighbors(i, j, index)
				m.flags[index] |= jpxFirstMagBit

				index = index0
				for i2 := i0; i2 <= i; i2, index = i2+1, index+w {
					m.bitsDecoded[index]++
				}
				i1++
			}
			for i = i0 + i1; i < iNext; i, index = i+1, index+w {
				if m.magnitude[index] != 0 || m.flags[index]&jpxProcessed != 0 {
					continue
				}
				label := m.labels[m.neighbors[index]]
				if m.decoder.ReadBit(m.contexts[:], uint32(label)) == 1 {
					m.sign[index] = m.decodeSign(i, j, index)
					m.magnitude[index] = 1
					m.setNeighbors(i, j, index)
					m.flags[index] |= jpxFirstMagBit
				}
				m.bitsDecoded[index]++
			}
		}
	}
}

func (m *jpxBitModel) segmentationSymbol() bool {
	v := m.decoder.ReadBit(m.contexts[:], jpxUniformContext)<<3 |
		m.decoder.ReadBit(m.contexts[:], jpxUniformContext)<<2 |
		m.decoder.ReadBit(m.contexts[:], jpxUniformContext)<<1 |
		m.decoder.ReadBit(m.contexts[:], jpxUniformContext)
	return v == 0xa
}

// Section F, the inverse discrete wavelet transform.
type jpxBand struct {
	w, h  int
	items []float32
}

// jpxExtend is F.3.7, the symmetric extension the filters need on both sides.
func jpxExtend(buf []float32, offset, size int) {
	i1, j1 := offset-1, offset+1
	i2, j2 := offset+size-2, offset+size
	for n := 0; n < 3; n++ {
		buf[i1] = buf[j1]
		buf[j2] = buf[i2]
		i1--
		j1++
		i2--
		j2++
	}
	buf[i1] = buf[j1]
	buf[j2] = buf[i2]
}

// jpxExtendRows is jpxExtend with a row of the band where it has a sample.
func jpxExtendRows(rows [][]float32, offset, size int) {
	i1, j1 := offset-1, offset+1
	i2, j2 := offset+size-2, offset+size
	for k := 0; k < 4; k++ {
		copy(rows[i1], rows[j1])
		copy(rows[j2], rows[i2])
		i1--
		j1++
		i2--
		j2++
	}
}

// jpxReversibleFilterRows is jpxReversibleFilter down every column of a band
// at once, which is what keeps the vertical pass reading whole rows.
func jpxReversibleFilterRows(rows [][]float32, offset, length int) {
	half := length >> 1
	j := offset
	for m := half + 1; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] -= float32((int32(lo[i]) + int32(hi[i]) + 2) >> 2)
		}
	}
	j = offset + 1
	for m := half; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] += float32((int32(lo[i]) + int32(hi[i])) >> 1)
		}
	}
}

// jpxIrreversibleFilterRows is jpxIrreversibleFilter down every column at once.
func jpxIrreversibleFilterRows(rows [][]float32, offset, length int) {
	half := length >> 1

	j := offset - 3
	for m := half + 4; m > 0; m, j = m-1, j+2 {
		mid := rows[j]
		for i := range mid {
			mid[i] = float32(jpxKI * mid[i])
		}
	}
	j = offset - 2
	for m := half + 3; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] = float32(jpxKK*mid[i]) - (float32(jpxDelta*lo[i]) + float32(jpxDelta*hi[i]))
		}
	}
	j = offset - 1
	for m := half + 2; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] -= float32(jpxGamma*lo[i]) + float32(jpxGamma*hi[i])
		}
	}
	j = offset
	for m := half + 1; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] -= float32(jpxBeta*lo[i]) + float32(jpxBeta*hi[i])
		}
	}
	j = offset + 1
	for m := half; m > 0; m, j = m-1, j+2 {
		lo, mid, hi := rows[j-1], rows[j], rows[j+1]
		for i := range mid {
			mid[i] -= float32(jpxAlpha*lo[i]) + float32(jpxAlpha*hi[i])
		}
	}
}

// jpxReversibleFilter is the 5-3 filter of F.3.8.2, which works on integers.
func jpxReversibleFilter(x []float32, offset, length int) {
	half := length >> 1
	j := offset
	for n := half + 1; n > 0; n, j = n-1, j+2 {
		x[j] -= float32((int32(x[j-1]) + int32(x[j+1]) + 2) >> 2)
	}
	j = offset + 1
	for n := half; n > 0; n, j = n-1, j+2 {
		x[j] += float32((int32(x[j-1]) + int32(x[j+1])) >> 1)
	}
}

// The lifting constants of the 9-7 filter, F.3.8.1.
const (
	jpxAlpha = -1.586134342059924
	jpxBeta  = -0.052980118572961
	jpxGamma = 0.882911075530934
	jpxDelta = 0.443506852043971
	jpxKK    = 1.230174104914001
	jpxKI    = 1 / jpxKK
)

// jpxIrreversibleFilter is the 9-7 filter of F.3.8.1.
func jpxIrreversibleFilter(x []float32, offset, length int) {
	const (
		alpha = jpxAlpha
		beta  = jpxBeta
		gamma = jpxGamma
		delta = jpxDelta
		kk    = jpxKK
		ki    = jpxKI
	)
	half := length >> 1

	j := offset - 3
	for n := half + 4; n > 0; n, j = n-1, j+2 {
		x[j] = float32(ki * x[j])
	}
	j = offset - 2
	for n := half + 3; n > 0; n, j = n-1, j+2 {
		x[j] = float32(kk*x[j]) - (float32(delta*x[j-1]) + float32(delta*x[j+1]))
	}
	j = offset - 1
	for n := half + 2; n > 0; n, j = n-1, j+2 {
		x[j] -= float32(gamma*x[j-1]) + float32(gamma*x[j+1])
	}
	j = offset
	for n := half + 1; n > 0; n, j = n-1, j+2 {
		x[j] -= float32(beta*x[j-1]) + float32(beta*x[j+1])
	}
	j = offset + 1
	for n := half; n > 0; n, j = n-1, j+2 {
		x[j] -= float32(alpha*x[j-1]) + float32(alpha*x[j+1])
	}
}

const jpxPad = 4

// jpxIterate is F.3.3 to F.3.5: interleave the lower level into the three high
// pass bands, then filter every row and every column.
func jpxIterate(ll, band jpxBand, u0, v0 int, reversible bool) jpxBand {
	filter := jpxIrreversibleFilter
	if reversible {
		filter = jpxReversibleFilter
	}
	w, h, items := band.w, band.h, band.items
	for i, k := 0, 0; i < ll.h; i++ {
		l := i * 2 * w
		for j := 0; j < ll.w; j, k, l = j+1, k+1, l+2 {
			if l < len(items) {
				items[l] = ll.items[k]
			}
		}
	}

	if w == 1 {
		if u0&1 != 0 {
			for k := 0; k < len(items); k += w {
				items[k] *= 0.5
			}
		}
	} else {
		buf := make([]float32, w+2*jpxPad)
		for v, k := 0, 0; v < h; v, k = v+1, k+w {
			copy(buf[jpxPad:], items[k:k+w])
			jpxExtend(buf, jpxPad, w)
			filter(buf, jpxPad, w)
			copy(items[k:k+w], buf[jpxPad:jpxPad+w])
		}
	}

	if h == 1 {
		if v0&1 != 0 {
			for u := 0; u < w; u++ {
				items[u] *= 0.5
			}
		}
	} else {
		filterRows := jpxIrreversibleFilterRows
		if reversible {
			filterRows = jpxReversibleFilterRows
		}
		pad := make([]float32, 2*jpxPad*w)
		rows := make([][]float32, h+2*jpxPad)
		for v := 0; v < h; v++ {
			rows[jpxPad+v] = items[v*w : v*w+w]
		}
		for k := 0; k < jpxPad; k++ {
			rows[k] = pad[k*w : k*w+w]
			rows[jpxPad+h+k] = pad[(jpxPad+k)*w : (jpxPad+k)*w+w]
		}
		jpxExtendRows(rows, jpxPad, h)
		filterRows(rows, jpxPad, h)
	}
	return jpxBand{w, h, items}
}

var jpxSubbandGain = map[string]int{"LL": 0, "LH": 1, "HL": 1, "HH": 2}

// jpxCopyCoefficients decodes every codeblock of a subband and writes the
// dequantized coefficients into the resolution level, interleaved where F.3.3
// wants them so the transform does not have to move them again.
func jpxCopyCoefficients(coeff []float32, levelW int, sb *jpxSubband, delta float64, mb int, reversible bool, cod *jpxCOD) {
	x0, y0 := sb.tbx0, sb.tby0
	width := sb.tbx1 - sb.tbx0
	if width <= 0 {
		return
	}
	right, bottom := 0, 0
	if sb.kind[0] == 'H' {
		right = 1
	}
	if sb.kind[1] == 'H' {
		bottom = levelW
	}
	interleave := sb.kind != "LL"

	for _, cb := range sb.codeblocks {
		bw, bh := cb.x1-cb.x0, cb.y1-cb.y0
		if bw <= 0 || bh <= 0 || len(cb.chunks) == 0 {
			continue
		}
		m := newJPXBitModel(bw, bh, cb.subband, cb.zeroBitPlanes)
		m.vsc = cod.verticalStripe
		pass := 0
		for i := 0; i < len(cb.chunks); {
			// Chunks that do not terminate continue the same codeword
			// segment, which is the whole codeblock unless the bypass
			// or terminate all style breaks it up.
			raw := cb.chunks[i].raw
			last, passes, total := i, 0, 0
			for {
				passes += cb.chunks[last].passes
				total += len(cb.chunks[last].data)
				if cb.chunks[last].terminated || last+1 >= len(cb.chunks) {
					break
				}
				last++
			}
			seg := cb.chunks[i].data
			if last > i {
				seg = make([]byte, 0, total)
				for k := i; k <= last; k++ {
					seg = append(seg, cb.chunks[k].data...)
				}
			}
			i = last + 1
			if raw {
				m.raw = &jpxPacketReader{data: seg}
				m.decoder = nil
			} else {
				m.raw = nil
				m.decoder = syntax.NewMQ(seg, 0, len(seg))
			}
			for j := 0; j < passes; j++ {
				switch jpxPassType(pass) {
				case jpxSignificance:
					m.significancePass()
				case jpxRefinement:
					m.refinementPass()
				case jpxCleanup:
					m.cleanupPass()
					if cod.segmentSymbol {
						m.segmentationSymbol()
					}
				}
				if cod.resetContexts {
					m.reset()
				}
				pass++
			}
		}

		offset := cb.x0 - x0 + (cb.y0-y0)*width
		correction := 0.0
		if !reversible {
			correction = 0.5
		}
		position := 0
		for j := 0; j < bh; j++ {
			row := offset / width
			levelOffset := 2*row*(levelW-width) + right + bottom
			for k := 0; k < bw; k++ {
				if n := float64(m.magnitude[position]); n != 0 {
					n = (n + correction) * delta
					if m.sign[position] != 0 {
						n = -n
					}
					if nb := int(m.bitsDecoded[position]); nb < mb {
						n *= float64(uint64(1) << uint(mb-nb))
					}
					pos := offset
					if interleave {
						pos = levelOffset + offset*2
					}
					if pos >= 0 && pos < len(coeff) {
						coeff[pos] = float32(n)
					}
				}
				offset++
				position++
			}
			offset += width - bw
		}
	}
}

func jpxTransformTile(ctx *jpxContext, tile *jpxTile, c int) jpxBand {
	comp := tile.components[c]
	if comp.cod == nil || comp.quant == nil || c >= len(ctx.components) {
		return jpxBand{}
	}
	cod, quant := comp.cod, comp.quant
	precision := ctx.components[c].precision
	reversible := cod.reversible

	var bands []jpxBand
	b := 0
	for i := 0; i <= cod.levels && i < len(comp.resolutions); i++ {
		res := comp.resolutions[i]
		w, h := res.trx1-res.trx0, res.try1-res.try0
		if w < 0 || h < 0 || w > maxImagePixels || h > maxImagePixels ||
			int64(w)*int64(h) > maxImagePixels {
			return jpxBand{}
		}
		coeff := make([]float32, w*h)
		for _, sb := range res.subbands {
			var mu, epsilon int
			if !quant.scalarExpounded {
				if len(quant.spqcds) > 0 {
					mu = quant.spqcds[0].mu
					epsilon = quant.spqcds[0].epsilon
				}
				if i > 0 {
					epsilon += 1 - i
				}
			} else {
				if b < len(quant.spqcds) {
					mu = quant.spqcds[b].mu
					epsilon = quant.spqcds[b].epsilon
				}
				b++
			}
			gain := jpxSubbandGain[sb.kind]
			delta := 1.0
			if !reversible {
				delta = jpxPow2(precision+gain-epsilon) * (1 + float64(mu)/2048)
			}
			mb := quant.guardBits + epsilon - 1
			jpxCopyCoefficients(coeff, w, sb, delta, mb, reversible, cod)
		}
		bands = append(bands, jpxBand{w, h, coeff})
	}
	if len(bands) == 0 {
		return jpxBand{}
	}
	ll := bands[0]
	for i := 1; i < len(bands); i++ {
		ll = jpxIterate(ll, bands[i], comp.tcx0, comp.tcy0, reversible)
	}
	return ll
}

func jpxPow2(n int) float64 {
	v := 1.0
	for ; n > 0; n-- {
		v *= 2
	}
	for ; n < 0; n++ {
		v /= 2
	}
	return v
}

// A.6.1 and A.6.2, the coding style of COD or COC. COC carries only the
// component part, so it starts from a copy of the style already in force.
func jpxReadCodingStyle(data []byte, j, end int, base *jpxCOD, component bool) (*jpxCOD, error) {
	cod := &jpxCOD{}
	if base != nil {
		*cod = *base
		cod.precincts = nil
	}
	head := 6
	if !component {
		head = 10
	}
	if j < 0 || j+head > end || end > len(data) {
		return nil, fmt.Errorf("%w: JPX coding style is truncated", ErrInvalid)
	}
	if !component {
		scod := data[j]
		j++
		cod.customPrecincts = scod&1 != 0
		cod.sop = scod&2 != 0
		cod.eph = scod&4 != 0
		cod.progression = int(data[j])
		cod.layers = jpxU16(data, j+1)
		cod.mct = int(data[j+3])
		j += 4
	} else {
		cod.customPrecincts = data[j]&1 != 0
		j++
	}
	cod.levels = int(data[j])
	cod.xcb = int(data[j+1]&0xf) + 2
	cod.ycb = int(data[j+2]&0xf) + 2
	style := data[j+3]
	cod.bypass = style&1 != 0
	cod.resetContexts = style&2 != 0
	cod.terminateAll = style&4 != 0
	cod.verticalStripe = style&8 != 0
	cod.predictable = style&16 != 0
	cod.segmentSymbol = style&32 != 0
	cod.reversible = data[j+4] != 0
	j += 5
	if cod.customPrecincts {
		for ; j < end; j++ {
			cod.precincts = append(cod.precincts, jpxPrecinctSize{int(data[j] & 0xf), int(data[j] >> 4)})
		}
	}
	if cod.layers <= 0 {
		cod.layers = 1
	}
	return cod, nil
}

// A.6.4 and A.6.5, the quantization style of QCD or QCC.
func jpxReadQuant(data []byte, j, end int) (*jpxQuant, error) {
	if j < 0 || j >= end || end > len(data) {
		return nil, fmt.Errorf("%w: JPX quantization is truncated", ErrInvalid)
	}
	q := &jpxQuant{}
	sqcd := data[j]
	j++
	size := 0
	switch sqcd & 0x1f {
	case 0:
		size, q.scalarExpounded = 8, true
	case 1:
		size, q.scalarExpounded = 16, false
	case 2:
		size, q.scalarExpounded = 16, true
	default:
		return nil, fmt.Errorf("%w: JPX quantization style %d", ErrInvalid, sqcd&0x1f)
	}
	q.noQuantization = size == 8
	q.guardBits = int(sqcd >> 5)
	for j < end {
		if size == 8 {
			q.spqcds = append(q.spqcds, jpxSpqcd{epsilon: int(data[j] >> 3)})
			j++
		} else {
			if j+2 > end {
				break
			}
			q.spqcds = append(q.spqcds, jpxSpqcd{
				epsilon: int(data[j] >> 3),
				mu:      int(data[j]&7)<<8 | int(data[j+1]),
			})
			j += 2
		}
	}
	return q, nil
}

// jpxParseCodestream reads the markers of A.4 and decodes every tile it finds.
// A file that stops early keeps what it decoded, as every other reader does.
func jpxParseCodestream(data []byte, start, end int) (*jpxContext, error) {
	ctx := &jpxContext{coc: map[int]*jpxCOD{}, qcc: map[int]*jpxQuant{}}
	pos := start
	for pos+1 < end {
		code := jpxU16(data, pos)
		pos += 2
		length := 0
		switch code {
		case 0xff4f: // SOC
			ctx.mainHeader = true
		case 0xffd9: // EOC
		case 0xff51: // SIZ
			length = jpxU16(data, pos)
			if pos+38 > end {
				return nil, fmt.Errorf("%w: JPX size marker is truncated", ErrInvalid)
			}
			siz := jpxSIZ{
				xsiz: jpxU32(data, pos+4), ysiz: jpxU32(data, pos+8),
				xosiz: jpxU32(data, pos+12), yosiz: jpxU32(data, pos+16),
				xtsiz: jpxU32(data, pos+20), ytsiz: jpxU32(data, pos+24),
				xtosiz: jpxU32(data, pos+28), ytosiz: jpxU32(data, pos+32),
				csiz: jpxU16(data, pos+36),
			}
			if siz.csiz <= 0 || siz.csiz > 256 {
				return nil, fmt.Errorf("%w: JPX has %d components", ErrInvalid, siz.csiz)
			}
			if siz.xsiz <= siz.xosiz || siz.ysiz <= siz.yosiz ||
				siz.xsiz-siz.xosiz > maxImagePixels || siz.ysiz-siz.yosiz > maxImagePixels ||
				int64(siz.xsiz-siz.xosiz)*int64(siz.ysiz-siz.yosiz) > maxImagePixels {
				return nil, fmt.Errorf("%w: JPX is %dx%d", ErrInvalid, siz.xsiz-siz.xosiz, siz.ysiz-siz.yosiz)
			}
			j := pos + 38
			for i := 0; i < siz.csiz; i++ {
				if j+3 > end {
					return nil, fmt.Errorf("%w: JPX size marker is truncated", ErrInvalid)
				}
				c := &jpxComponent{
					precision: jpxByte(data, j)&0x7f + 1,
					signed:    jpxByte(data, j)&0x80 != 0,
					xr:        jpxByte(data, j+1),
					yr:        jpxByte(data, j+2),
				}
				if c.xr <= 0 || c.yr <= 0 {
					return nil, fmt.Errorf("%w: JPX component subsamples by %dx%d", ErrInvalid, c.xr, c.yr)
				}
				j += 3
				jpxComponentDims(c, &siz)
				ctx.components = append(ctx.components, c)
			}
			ctx.siz = siz
			if err := jpxTileGrids(ctx); err != nil {
				return nil, err
			}
		case 0xff52: // COD
			length = jpxU16(data, pos)
			cod, err := jpxReadCodingStyle(data, pos+2, min(pos+length, end), nil, false)
			if err != nil {
				return nil, err
			}
			if ctx.mainHeader {
				ctx.cod = cod
			} else if ctx.current != nil {
				ctx.current.cod = cod
				ctx.current.coc = map[int]*jpxCOD{}
			}
		case 0xff53: // COC
			length = jpxU16(data, pos)
			j := pos + 2
			var c int
			if ctx.siz.csiz < 257 {
				c = jpxByte(data, j)
				j++
			} else {
				c = jpxU16(data, j)
				j += 2
			}
			base := ctx.cod
			if !ctx.mainHeader && ctx.current != nil {
				base = ctx.current.cod
			}
			coc, err := jpxReadCodingStyle(data, j, min(pos+length, end), base, true)
			if err != nil {
				return nil, err
			}
			if ctx.mainHeader {
				ctx.coc[c] = coc
			} else if ctx.current != nil {
				ctx.current.coc[c] = coc
			}
		case 0xff5c: // QCD
			length = jpxU16(data, pos)
			qcd, err := jpxReadQuant(data, pos+2, min(pos+length, end))
			if err != nil {
				return nil, err
			}
			if ctx.mainHeader {
				ctx.qcd = qcd
			} else if ctx.current != nil {
				ctx.current.qcd = qcd
				ctx.current.qcc = map[int]*jpxQuant{}
			}
		case 0xff5d: // QCC
			length = jpxU16(data, pos)
			j := pos + 2
			var c int
			if ctx.siz.csiz < 257 {
				c = jpxByte(data, j)
				j++
			} else {
				c = jpxU16(data, j)
				j += 2
			}
			qcc, err := jpxReadQuant(data, j, min(pos+length, end))
			if err != nil {
				return nil, err
			}
			if ctx.mainHeader {
				ctx.qcc[c] = qcc
			} else if ctx.current != nil {
				ctx.current.qcc[c] = qcc
			}
		case 0xff90: // SOT
			length = jpxU16(data, pos)
			part := &jpxTilePart{
				index:     jpxU16(data, pos+2),
				partIndex: jpxByte(data, pos+8),
			}
			tileLength := jpxU32(data, pos+4)
			part.dataEnd = tileLength + pos - 2
			if tileLength == 0 || part.dataEnd > end {
				part.dataEnd = end
			}
			ctx.mainHeader = false
			if part.partIndex == 0 {
				part.cod = ctx.cod
				part.qcd = ctx.qcd
				part.coc = map[int]*jpxCOD{}
				part.qcc = map[int]*jpxQuant{}
				for k, v := range ctx.coc {
					part.coc[k] = v
				}
				for k, v := range ctx.qcc {
					part.qcc[k] = v
				}
			} else if ctx.current != nil {
				part.cod, part.coc = ctx.current.cod, ctx.current.coc
				part.qcd, part.qcc = ctx.current.qcd, ctx.current.qcc
			}
			ctx.current = part
		case 0xff93: // SOD
			part := ctx.current
			if part == nil {
				return ctx, fmt.Errorf("%w: JPX has data before a tile", ErrInvalid)
			}
			if part.index < 0 || part.index >= len(ctx.tiles) {
				return ctx, fmt.Errorf("%w: JPX names tile %d", ErrInvalid, part.index)
			}
			tile := ctx.tiles[part.index]
			if part.partIndex == 0 {
				if err := jpxInitTile(ctx, tile, part); err != nil {
					return ctx, err
				}
				if err := jpxBuildPackets(ctx, tile); err != nil {
					return ctx, err
				}
			}
			if tile.packets == nil {
				return ctx, fmt.Errorf("%w: JPX tile %d has no header", ErrInvalid, part.index)
			}
			if err := jpxParsePackets(ctx, tile, data[pos:max(pos, part.dataEnd)]); err != nil {
				return ctx, err
			}
			length = max(0, part.dataEnd-pos)
		default:
			if code < 0xff00 {
				return ctx, fmt.Errorf("%w: JPX codestream is out of step at %d", ErrInvalid, pos-2)
			}
			length = jpxU16(data, pos)
			if length < 2 {
				return ctx, fmt.Errorf("%w: JPX marker %04x has length %d", ErrInvalid, code, length)
			}
		}
		pos += length
	}
	if ctx.components == nil {
		return nil, fmt.Errorf("%w: JPX has no size marker", ErrInvalid)
	}
	return ctx, nil
}

func jpxInitTile(ctx *jpxContext, tile *jpxTile, part *jpxTilePart) error {
	if part.cod == nil || part.qcd == nil {
		return fmt.Errorf("%w: JPX tile has no coding style", ErrInvalid)
	}
	for c, comp := range tile.components {
		comp.quant = part.qcd
		if q := part.qcc[c]; q != nil {
			comp.quant = q
		}
		comp.cod = part.cod
		if s := part.coc[c]; s != nil {
			comp.cod = s
		}
	}
	tile.cod = part.cod
	return nil
}

// jpxAssemble runs the inverse wavelet on every tile component, undoes the
// multiple component transform of G.2 and writes interleaved eight bit
// samples, upsampling any component the codestream subsampled.
func jpxAssemble(ctx *jpxContext) (*jpxImage, error) {
	siz := &ctx.siz
	w, h, n := siz.xsiz-siz.xosiz, siz.ysiz-siz.yosiz, siz.csiz
	// The codestream carries its own geometry, so it is what has to be held
	// to the sample count the image layer allows: the wavelet needs a float
	// for every one of them at every level.
	if w <= 0 || h <= 0 || w > maxImagePixels || h > maxImagePixels ||
		int64(w)*int64(h)*int64(n) > maxImagePixels {
		return nil, fmt.Errorf("%w: JPX is %dx%d with %d components", ErrInvalid, w, h, n)
	}
	img := &jpxImage{width: w, height: h, comps: n, pix: make([]byte, w*h*n)}

	bands := make([]jpxBand, n)
	for _, tile := range ctx.tiles {
		if tile.cod == nil {
			continue
		}
		for c := range tile.components {
			bands[c] = jpxTransformTile(ctx, tile, c)
		}
		if tile.cod != nil && tile.cod.mct != 0 && n >= 3 &&
			bands[0].w == bands[1].w && bands[1].w == bands[2].w &&
			bands[0].h == bands[1].h && bands[1].h == bands[2].h {
			jpxInverseMCT(bands, tile.cod.reversible)
		}
		for c := 0; c < n; c++ {
			jpxWriteComponent(img, ctx, tile, c, bands[c])
		}
	}
	return img, nil
}

// G.2 Inverse multiple component transform.
func jpxInverseMCT(bands []jpxBand, reversible bool) {
	b0, b1, b2 := bands[0].items, bands[1].items, bands[2].items
	n := min(len(b0), min(len(b1), len(b2)))
	if reversible {
		for i := 0; i < n; i++ {
			y0, y1, y2 := b0[i], b1[i], b2[i]
			g := y0 - float32((int32(y2)+int32(y1))>>2)
			b0[i] = g + y2
			b1[i] = g
			b2[i] = g + y1
		}
		return
	}
	for i := 0; i < n; i++ {
		y0, y1, y2 := b0[i], b1[i], b2[i]
		b0[i] = y0 + float32(1.402*y2)
		b1[i] = y0 - float32(0.34413*y1) - float32(0.71414*y2)
		b2[i] = y0 + float32(1.772*y1)
	}
}

func jpxWriteComponent(img *jpxImage, ctx *jpxContext, tile *jpxTile, c int, band jpxBand) {
	if band.items == nil || band.w <= 0 {
		return
	}
	comp := ctx.components[c]
	tc := tile.components[c]
	siz := &ctx.siz
	precision := comp.precision
	half := 0
	if !comp.signed {
		half = 1 << uint(precision-1)
	}
	shift := precision - 8
	if ctx.indexed {
		shift = 0
	}

	for y := tile.ty0; y < tile.ty1; y++ {
		oy := y - siz.yosiz
		if oy < 0 || oy >= img.height {
			continue
		}
		sy := y/comp.yr - tc.tcy0
		if sy < 0 || sy >= band.h {
			continue
		}
		row := band.items[sy*band.w:]
		out := img.pix[oy*img.width*img.comps+c:]
		if comp.xr == 1 {
			x0 := max(tile.tx0, siz.xosiz, tc.tcx0)
			x1 := min(tile.tx1, siz.xosiz+img.width, tc.tcx0+band.w)
			if x0 < x1 {
				jpxWriteRow(out[(x0-siz.xosiz)*img.comps:], img.comps,
					row[x0-tc.tcx0:x1-tc.tcx0], half, shift)
			}
			continue
		}
		for x := tile.tx0; x < tile.tx1; x++ {
			ox := x - siz.xosiz
			if ox < 0 || ox >= img.width {
				continue
			}
			sx := x/comp.xr - tc.tcx0
			if sx < 0 || sx >= band.w {
				continue
			}
			out[ox*img.comps] = jpxSample(row[sx], half, shift)
		}
	}
}

// jpxWriteRow is the body of jpxWriteComponent for a component the image is
// not subsampled in, where the samples run one to one.
func jpxWriteRow(out []uint8, stride int, row []float32, half, shift int) {
	switch {
	case shift == 0:
		for i, v := range row {
			out[i*stride] = jpxClamp(int(float64(v) + float64(half) + 0.5))
		}
	case shift > 0:
		for i, v := range row {
			out[i*stride] = jpxClamp(int(float64(v)+float64(half)+0.5) >> uint(shift))
		}
	default:
		for i, v := range row {
			out[i*stride] = jpxClamp(int(float64(v)+float64(half)+0.5) << uint(-shift))
		}
	}
}

// jpxSample is one sample of jpxWriteRow, for the subsampled case.
func jpxSample(v float32, half, shift int) uint8 {
	n := int(float64(v) + float64(half) + 0.5)
	if shift > 0 {
		n >>= uint(shift)
	} else if shift < 0 {
		n <<= uint(-shift)
	}
	return jpxClamp(n)
}

func jpxClamp(v int) uint8 {
	return uint8(min(max(v, 0), 255))
}

// jpxComponents reports how many components a codestream carries, reading no
// further than its size marker. The image dictionary may leave the color space
// out and let the codestream say, ISO 32000-1 7.4.9, and a caller that only
// wants to know what the image is should not have to decode it.
func jpxComponents(data []byte) int {
	for i := 0; i+42 <= len(data); i++ {
		if data[i] == 0xff && data[i+1] == 0x4f && data[i+2] == 0xff && data[i+3] == 0x51 {
			return jpxU16(data, i+40)
		}
	}
	return 0
}

// jpxDecode reads a JPEG 2000 codestream, bare or wrapped in the JP2 boxes of
// ISO 15444-1 Annex I, and returns interleaved eight bit samples.
func jpxDecode(data []byte, indexed bool) (*jpxImage, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("%w: JPX stream is %d bytes", ErrInvalid, len(data))
	}
	if jpxU16(data, 0) == 0xff4f {
		ctx, err := jpxParseCodestream(data, 0, len(data))
		if ctx == nil {
			return nil, err
		}
		ctx.indexed = indexed
		return jpxAssemble(ctx)
	}

	var ctx *jpxContext
	var perr error
	pos := 0
	for pos+8 <= len(data) {
		header := 8
		box := int64(jpxU32(data, pos))
		kind := jpxU32(data, pos+4)
		pos += header
		if box == 1 {
			box = int64(jpxU32(data, pos))<<32 | int64(uint32(jpxU32(data, pos+4)))
			pos += 8
			header += 8
		}
		if box == 0 {
			box = int64(len(data) - pos + header)
		}
		if box < int64(header) {
			return nil, fmt.Errorf("%w: JP2 box is %d bytes", ErrInvalid, box)
		}
		size := int(box) - header
		if kind == 0x6a703268 { // jp2h, read its children
			continue
		}
		if kind == 0x6a703263 { // jp2c
			end := min(pos+size, len(data))
			ctx, perr = jpxParseCodestream(data, pos, end)
			if ctx != nil {
				break
			}
		}
		if size <= 0 {
			break
		}
		pos += size
	}
	if ctx == nil {
		if perr != nil {
			return nil, perr
		}
		return nil, fmt.Errorf("%w: JP2 has no codestream", ErrInvalid)
	}
	ctx.indexed = indexed
	return jpxAssemble(ctx)
}
