//go:build !(goexperiment.simd && amd64)

package storage

// This file provides the default-build stubs for the data-page checksum SIMD
// kernel. It is selected whenever the build is NOT `GOEXPERIMENT=simd` on
// amd64, which is every ordinary build of goopg.
//
// See docs/design/not_ralph/try-simd/DESIGN.md. The measured verdict there is
// that the checksum is 0.043%-0.4% of CPU across TPC-H, TPC-DS, COPY and OLTP
// workloads, so the SIMD kernel is an experiment, not a shipped optimisation.

// simdChecksumAvailable reports whether the vector kernel may be used. In the
// default build it never can.
func simdChecksumAvailable() bool { return false }

// pageChecksumBlockSIMD is unreachable in this build. It exists so that
// checksum.go has one call shape across both build configurations; calling it
// is a programming error rather than a silently wrong answer.
func pageChecksumBlockSIMD(page []byte) uint32 {
	panic("storage: pageChecksumBlockSIMD called in a non-SIMD build")
}
