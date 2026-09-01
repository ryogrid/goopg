package pglz

import (
	"bytes"
	"fmt"
	"math/rand"
	"testing"
)

// benchBlobs are the shapes the TOAST write path actually meets: a repetitive
// catalog blob (pg_rewrite.ev_action-like), English-ish text, and
// incompressible random bytes (the worst case for the match search, since no
// candidate ever produces a long match).
func benchBlobs() map[string][]byte {
	var nodeTree bytes.Buffer
	for i := 0; nodeTree.Len() < 64<<10; i++ {
		fmt.Fprintf(&nodeTree, "{TARGETENTRY :expr {VAR :varno 1 :varattno %d :vartype 23 :vartypmod -1 :varcollid 0 :varlevelsup 0} :resno %d :resname col%d :ressortgroupref 0 :resorigtbl 0 :resorigcol 0 :resjunk false}", i%16, i, i%16)
	}
	var text bytes.Buffer
	words := []string{"the", "quick", "brown", "fox", "jumps", "over", "lazy", "dog", "postgres", "compression"}
	r := rand.New(rand.NewSource(1))
	for text.Len() < 64<<10 {
		text.WriteString(words[r.Intn(len(words))])
		text.WriteByte(' ')
	}
	rnd := make([]byte, 64<<10)
	r.Read(rnd)
	return map[string][]byte{"nodetree": nodeTree.Bytes(), "text": text.Bytes(), "random": rnd}
}

// BenchmarkCompress guards the hash-chain matcher of review/260831 NB-17. The
// previous brute-force window scan was quadratic in the value size; a
// regression shows up here as a large slowdown on the 64 KiB inputs.
func BenchmarkCompress(b *testing.B) {
	for name, blob := range benchBlobs() {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(blob)))
			for b.Loop() {
				Compress(blob)
			}
		})
	}
}

// TestCompressRatioAndRoundTrip keeps the matcher honest about the thing the
// speedup could plausibly cost: compression ratio. It also round-trips every
// shape through Decompress.
func TestCompressRatioAndRoundTrip(t *testing.T) {
	wantRatio := map[string]float64{"nodetree": 0.25, "text": 0.75}
	for name, blob := range benchBlobs() {
		comp := Compress(blob)
		back, err := Decompress(comp, len(blob))
		if err != nil {
			t.Fatalf("%s: Decompress: %v", name, err)
		}
		if !bytes.Equal(back, blob) {
			t.Fatalf("%s: round-trip mismatch", name)
		}
		if want, ok := wantRatio[name]; ok {
			if got := float64(len(comp)) / float64(len(blob)); got > want {
				t.Errorf("%s: compressed to %.3f of input, want <= %.3f", name, got, want)
			}
		}
	}
}

// BenchmarkDecompress guards review/260831 NB-18: a non-overlapping match is
// one copy, not a byte-at-a-time loop.
func BenchmarkDecompress(b *testing.B) {
	for name, blob := range benchBlobs() {
		comp := Compress(blob)
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(blob)))
			for b.Loop() {
				if _, err := Decompress(comp, len(blob)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
