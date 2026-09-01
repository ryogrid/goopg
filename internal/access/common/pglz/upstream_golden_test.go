package pglz

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestCompressMatchesUpstreamPGLZ pins byte-identity with real PostgreSQL.
//
// The golden files were produced by linking postgres/src/common/pg_lzcompress.c
// (PG 18.3) into a frontend harness and running pglz_compress() with
// PGLZ_strategy_default over the same blobs benchBlobs() builds. review/260831
// NB-17 replaced the brute-force match search with upstream's hash chain plus
// the good_match/good_drop early exit, which is what makes the output stream
// identical rather than merely valid: the old search found longer matches than
// PG does and so emitted a different (smaller) stream. Keeping the streams
// identical means a goopg-written TOAST value is bit-for-bit what PG would have
// written, so any divergence here is a compatibility regression, not a tuning
// choice.
func TestCompressMatchesUpstreamPGLZ(t *testing.T) {
	blobs := benchBlobs()
	for _, name := range []string{"nodetree", "text"} {
		want, err := os.ReadFile(filepath.Join("testdata", name+".pglz"))
		if err != nil {
			t.Fatal(err)
		}
		got := Compress(blobs[name])
		if !bytes.Equal(got, want) {
			t.Errorf("%s: Compress produced %d bytes, upstream pglz_compress produced %d; streams differ",
				name, len(got), len(want))
			continue
		}
		back, err := Decompress(got, len(blobs[name]))
		if err != nil {
			t.Fatalf("%s: Decompress: %v", name, err)
		}
		if !bytes.Equal(back, blobs[name]) {
			t.Errorf("%s: round trip mismatch", name)
		}
	}
}
