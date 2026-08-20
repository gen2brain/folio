//go:build !amd64 || noasm

package raster

func simdTiers() []string { return []string{"native"} }

func setTier(string) {}
