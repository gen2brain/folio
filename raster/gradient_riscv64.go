//go:build riscv64 && riscv64.rva23u64 && !noasm

package raster

//go:noescape
func gradRadialRVV(p *gradParams, cu, cv float32, x int, idx *int32, w int)

func (g *Gradient) index(x, y, w int, idx []int32) {
	if g.radial && !g.linear && w > 0 {
		fy := float32(y) + 0.5
		gradRadialRVV(&g.p, float32(g.p.ic*fy), float32(g.p.id*fy), x, &idx[0], w)
		return
	}
	g.indexScalar(x, y, w, idx)
}
