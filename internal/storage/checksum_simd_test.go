//go:build goexperiment.simd && amd64

package storage

import (
	"math/rand"
	"testing"
)

// TestChecksumSIMDMatchesScalar is the equivalence gate for the AVX2 kernel.
//
// The oracle is the PRODUCTION scalar function, not a transcription of it, so
// the two cannot drift apart silently. The corpus deliberately includes the
// shapes a random corpus alone would miss:
//
//   - all-zero and all-0xFF pages (the degenerate mixing inputs),
//   - a page whose pd_checksum and pd_flags words are both non-zero, which is
//     the only way the 0xFFFF0000 masking special case is exercised at all,
//   - a page with pd_upper == 0 but non-zero elsewhere. PG's PageIsVerified
//     falls through to an all-zeros check and REJECTS such a page, while
//     goopg's VerifyPage accepts it (a pre-existing divergence recorded in
//     the design doc). The kernel must at least agree with the scalar routine
//     on it.
func TestChecksumSIMDMatchesScalar(t *testing.T) {
	if !simdChecksumAvailable() {
		t.Skip("AVX2 not available on this CPU")
	}
	rng := rand.New(rand.NewSource(20260908))

	pages := make([][]byte, 0, 260)
	zero := make([]byte, BlockSize)
	pages = append(pages, zero)
	ff := make([]byte, BlockSize)
	for i := range ff {
		ff[i] = 0xFF
	}
	pages = append(pages, ff)

	// pd_upper == 0 (bytes 14..15) but otherwise non-zero.
	pdUpperZero := make([]byte, BlockSize)
	for i := range pdUpperZero {
		pdUpperZero[i] = byte(i*31 + 7)
	}
	pdUpperZero[14], pdUpperZero[15] = 0, 0
	pages = append(pages, pdUpperZero)

	for n := 0; n < 256; n++ {
		p := make([]byte, BlockSize)
		rng.Read(p)
		// Guarantee a non-zero pd_checksum AND pd_flags so the mask matters.
		p[8], p[9] = byte(n|1), byte(n|1)
		p[10], p[11] = byte(n|2), byte(n|2)
		pages = append(pages, p)
	}

	for i, p := range pages {
		want := pageChecksumBlockScalar(p)
		got := pageChecksumBlockSIMD(p)
		if want != got {
			t.Fatalf("page %d: scalar=%#08x simd=%#08x", i, want, got)
		}
	}
	t.Logf("SIMD kernel agrees with the scalar oracle on %d pages", len(pages))
}

// TestChecksumSIMDDispatchAgrees pins that the public entry point returns the
// same value whichever path it dispatches to.
func TestChecksumSIMDDispatchAgrees(t *testing.T) {
	if !simdChecksumAvailable() {
		t.Skip("AVX2 not available on this CPU")
	}
	rng := rand.New(rand.NewSource(7))
	for n := 0; n < 64; n++ {
		p := make([]byte, BlockSize)
		rng.Read(p)
		if got, want := pageChecksumBlock(p), pageChecksumBlockScalar(p); got != want {
			t.Fatalf("dispatch mismatch: %#08x vs scalar %#08x", got, want)
		}
		if got, want := PageChecksum(p, BlockNumber(n)), uint16((pageChecksumBlockScalar(p)^uint32(n))%65535+1); got != want {
			t.Fatalf("PageChecksum mismatch: %d vs %d", got, want)
		}
	}
}

func BenchmarkPageChecksumScalar(b *testing.B) {
	p := make([]byte, BlockSize)
	rand.New(rand.NewSource(1)).Read(p)
	b.SetBytes(BlockSize)
	for i := 0; i < b.N; i++ {
		sinkChecksum = pageChecksumBlockScalar(p)
	}
}

func BenchmarkPageChecksumSIMD(b *testing.B) {
	if !simdChecksumAvailable() {
		b.Skip("AVX2 not available")
	}
	p := make([]byte, BlockSize)
	rand.New(rand.NewSource(1)).Read(p)
	b.SetBytes(BlockSize)
	for i := 0; i < b.N; i++ {
		sinkChecksum = pageChecksumBlockSIMD(p)
	}
}

var sinkChecksum uint32
