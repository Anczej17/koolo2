//go:build !noise_gen

package buildnoise

// Default entropy values — overridden by noise_gen.go when built via build scripts.
var (
	entropy0 uint64 = 0x0
	entropy1 uint64 = 0x0
	entropy2 uint64 = 0x0
	entropy3 uint64 = 0x0
)
