//go:build noasm || (!amd64 && !arm64 && !(riscv64 && riscv64.rva23u64 && go1.26))

package raster

func (g *Gradient) index(x, y, w int, idx []int32) {
	g.indexScalar(x, y, w, idx)
}
