//go:build arm64 && !noasm

package raster

//go:noescape
func gradRadialNEON(p *gradParams, cu, cv float32, x int, idx *int32, w int)

func (g *Gradient) index(x, y, w int, idx []int32) {
	if g.radial && !g.linear && w >= 4 {
		fy := float32(y) + 0.5
		k := w &^ 3
		gradRadialNEON(&g.p, float32(g.p.ic*fy), float32(g.p.id*fy), x, &idx[0], k)
		if k < w {
			g.indexScalar(x+k, y, w-k, idx[k:])
		}
		return
	}
	g.indexScalar(x, y, w, idx)
}
