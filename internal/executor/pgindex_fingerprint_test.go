package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-viii guards — the fingerprint funnel.
//
// Every TREE-KEY producer now resolves `pgIndexKeyDesc` and switches format.
// These six callers must do the OPPOSITE: they compare (or hash) bytes derived
// from DIFFERENT heap tuples, so a tuple key's embedded heap TID would make
// equal values compare unequal forever. The tests below pin that the
// fingerprints ignore the gate entirely, that the NULLS NOT DISTINCT
// comparison still answers "unchanged" with the gate on, that the SSI hash
// pairing cannot be split by the flip, and that no site in the two fingerprint
// files reaches around the funnel to the raw encoders.

func TestIndexKeyFingerprintIgnoresTheTupleFormat(t *testing.T) {
	// With the gate ON and a describable index, the tree-key funnel emits a
	// tuple image while the fingerprint must still emit the blob concatenation.
	// If a later flip routes indexKeyColumnsChanged / ssiRecordHashIndexInsert
	// through indexEntryKey, this is what breaks first.
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 460, col("a", "int4"), col("b", "text"))
	if ctx.pgIndexKeyDesc(idx) == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	row := Row{NewIntDatum(42), NewStringDatum("xyz")}

	blob, err := encodeIndexKeyFromCols(nil, idx, cols, row, nil)
	if err != nil || blob == nil {
		t.Fatalf("fixture encodeIndexKeyFromCols = %x, %v", blob, err)
	}
	got, err := indexKeyFingerprint(idx, cols, row, nil)
	if err != nil {
		t.Fatalf("indexKeyFingerprint: %v", err)
	}
	if !bytes.Equal(got, blob) {
		t.Fatalf("fingerprint = %x, want the blob %x", got, blob)
	}
	entry, err := ctx.indexEntryKey(idx, cols, row, storage.ItemPointer{Block: 7, Offset: 3})
	if err != nil {
		t.Fatalf("indexEntryKey: %v", err)
	}
	if bytes.Equal(got, entry) {
		t.Fatalf("fingerprint followed the flip: it equals the tuple entry key %x", entry)
	}
}

func TestIndexKeyFingerprintIsEqualForTwoHeapVersionsOfOneRow(t *testing.T) {
	// The property every fingerprint caller actually depends on: the same key
	// VALUES fingerprint identically no matter which heap tuple they came from.
	// (indexKeyColumnsChanged compares an old and a new row version; the SSI
	// hash writer must land in the bucket a reader computed from an expression.)
	withPGIndexTupleKeys(t)
	_, idx, cols := rowKeyCtxAndIndex(t, 461, col("a", "int4"), col("b", "text"))
	oldRow := Row{NewIntDatum(42), NewStringDatum("xyz")}
	newRow := Row{NewIntDatum(42), NewStringDatum("xyz")}

	a, err := indexKeyFingerprint(idx, cols, oldRow, nil)
	if err != nil {
		t.Fatalf("indexKeyFingerprint(old): %v", err)
	}
	b, err := indexKeyFingerprint(idx, cols, newRow, nil)
	if err != nil {
		t.Fatalf("indexKeyFingerprint(new): %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("equal key values fingerprinted differently: %x vs %x", a, b)
	}
	changed := Row{NewIntDatum(43), NewStringDatum("xyz")}
	c, err := indexKeyFingerprint(idx, cols, changed, nil)
	if err != nil {
		t.Fatalf("indexKeyFingerprint(changed): %v", err)
	}
	if bytes.Equal(a, c) {
		t.Fatal("a changed key column produced the same fingerprint")
	}
}

func TestIndexColumnFingerprintMatchesTheColumnEncoderAndIgnoresTheGate(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"))
	c := &tbl.Columns[0]

	want, eerr := encodeBTreeKeyForColumn(nil, NewIntDatum(42), c, 0)
	if eerr != nil {
		t.Fatalf("fixture encodeBTreeKeyForColumn: %v", eerr)
	}
	got, ferr := indexColumnFingerprint(NewIntDatum(42), c)
	if ferr != nil {
		t.Fatalf("indexColumnFingerprint: %v", ferr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("column fingerprint = %x, want %x", got, want)
	}
	other, ferr := indexColumnFingerprint(NewIntDatum(43), c)
	if ferr != nil {
		t.Fatalf("indexColumnFingerprint(43): %v", ferr)
	}
	if bytes.Equal(got, other) {
		t.Fatal("distinct values produced the same column fingerprint")
	}
}

func TestNNDKeyColumnsEqualStillSeesNoChangeUnderTheTupleFormat(t *testing.T) {
	// nndKeyColumnsEqual decides whether an UPDATE on a NULLS NOT DISTINCT index
	// may SKIP the uniqueness probe. It is called before the old tuple is
	// stamped dead, so a false "changed" makes the row conflict with itself on
	// the ON CONFLICT DO UPDATE path. Equal values must stay equal with the gate
	// on, including the NULL == NULL case that has no encoded bytes at all.
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 462, col("a", "int4"), col("b", "text"))
	idx.NullsNotDistinct = true
	if ctx.pgIndexKeyDesc(idx) == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	oldRow := Row{NewIntDatum(42), NullDatum}
	newRow := Row{NewIntDatum(42), NullDatum}
	if !nndKeyColumnsEqual(idx, cols, oldRow, newRow) {
		t.Fatal("nndKeyColumnsEqual reported a change for two identical NND keys")
	}
	if nndKeyColumnsEqual(idx, cols, oldRow, Row{NewIntDatum(43), NullDatum}) {
		t.Fatal("nndKeyColumnsEqual reported no change for a different key value")
	}
	if nndKeyColumnsEqual(idx, cols, oldRow, Row{NewIntDatum(42), NewStringDatum("x")}) {
		t.Fatal("nndKeyColumnsEqual reported no change for a NULL → non-NULL key column")
	}
}

func TestDeclaredHashIndexIsDescribableSoBothSSIHalvesMustFingerprint(t *testing.T) {
	// This test replaces `TestHashIndexIsNeverDescribableSoTheSSIBucketPairingHolds`,
	// which was VACUOUS: it built its fixture with `idx.Method = "hash"`, but
	// `CREATE INDEX ... USING hash` records `Method == "btree"` with only
	// `DeclaredHash` set (operators_ddl.go — goopg builds the index on the
	// B-tree substrate). So the access-method refusal it relied on never fires
	// for a real hash index, the reader's scan key DID become a tuple image at
	// the flip while the writer stayed on the blob, and all 18 conflicting
	// permutations of predicate-hash.spec silently stopped aborting
	// (M-NIGHTLY AI-20260811-014635-001).
	//
	// The invariant that actually holds is the one below: the index IS
	// describable, so BOTH SSI halves must go through the fingerprint —
	// `ssiHashProbeFingerprint` for the reader, `indexKeyFingerprint` for the
	// writer — and neither may hash the search key.
	withPGIndexTupleKeys(t)
	ctx, idx, cols := rowKeyCtxAndIndex(t, 463, col("a", "int4"))
	idx.DeclaredHash = true // Method stays "btree": what CREATE INDEX USING hash records
	if ctx.pgIndexKeyDesc(idx) == nil {
		t.Fatal("fixture hash index is not describable; the test would pass for the wrong reason")
	}

	v := NewIntDatum(20)
	parts := []indexProbeKeyPart{{col: &idx.Table.Columns[0], val: v}}
	reader := ssiHashProbeFingerprint(idx, parts)
	if len(reader) == 0 {
		t.Fatal("ssiHashProbeFingerprint returned nothing for an int4 equality probe")
	}
	writer, err := indexKeyFingerprint(idx, cols, Row{v}, nil)
	if err != nil || len(writer) == 0 {
		t.Fatalf("indexKeyFingerprint = %x, %v", writer, err)
	}
	if !bytes.Equal(reader, writer) {
		t.Fatalf("reader fingerprint %x != writer fingerprint %x: every rw-edge on a hash index would be lost", reader, writer)
	}
	if ssiHashBucket(reader) != ssiHashBucket(writer) {
		t.Fatalf("bucket %d != %d", ssiHashBucket(reader), ssiHashBucket(writer))
	}

	// Non-vacuity: the search key the SAME probe descends the tree with is a
	// different encoding. If a later change makes these equal again the test
	// above stops proving anything, so say so here rather than passing quietly.
	// (indexProbeKey returns a *ExecError, so it is bound to its own variable —
	// assigning it into an `error` would make a typed nil compare non-nil.)
	search, probeErr := ctx.indexProbeKey(idx, parts)
	if probeErr != nil {
		t.Fatalf("indexProbeKey: %v", probeErr)
	}
	if bytes.Equal(reader, search) {
		t.Fatalf("search key %x equals the fingerprint; this guard is no longer non-vacuous", search)
	}
}

// TestHashBucketReadCallSitesPassTheFingerprint pins the two reader call sites
// by source. The value test above proves the fingerprint is the right bytes; it
// cannot prove the operators HAND those bytes over — and handing over `loBytes`
// instead is exactly how this regressed, with every unit test still green.
func TestHashBucketReadCallSitesPassTheFingerprint(t *testing.T) {
	sites := 0
	for _, f := range []string{"operators_index.go", "operators_indexonly.go"} {
		src, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") || !strings.Contains(line, "ssiRecordHashBucketRead(") {
				continue
			}
			sites++
			if !strings.Contains(line, "hashProbeFingerprint") {
				t.Errorf("%s:%d hashes something other than the probe fingerprint: %s", f, i+1, trimmed)
			}
		}
	}
	if sites != 2 {
		t.Fatalf("found %d ssiRecordHashBucketRead call sites, want 2 (index scan + index-only scan)", sites)
	}
}

func TestFingerprintFunnelIsNotBypassed(t *testing.T) {
	// The funnel only holds if it cannot be reached around. A site that
	// fingerprints with the raw encoders is not wrong today — it is the same
	// bytes — but it is a site the flip's reviewer never sees, and the failure
	// (a lost conflict, a re-probed index) is silent either way. Allow the raw
	// encoders only inside the two blob KEY builders that legitimately own them.
	allowed := map[string]bool{
		"encodeIndexKeyFromCols": true, // the blob key builder itself
		"encodeExprIndexKey":     true, // the expression-key blob writer (B2-c-vii)
	}
	needles := []string{"encodeBTreeKeyForColumn(", "encodeIndexKeyFromCols("}
	for _, f := range []string{"operators_storage.go", "ssi.go"} {
		src, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		fn := ""
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(line, "func ") {
				fn = topLevelFuncName(line)
			}
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may name them
			}
			if allowed[fn] {
				continue
			}
			for _, needle := range needles {
				if strings.Contains(line, needle) {
					t.Errorf("%s:%d (in %s) bypasses the fingerprint funnel: %s", f, i+1, fn, trimmed)
				}
			}
		}
	}
}

// topLevelFuncName extracts the declared name from a `func ` line, method
// receiver included or not. Returns "" when the line is not a declaration this
// scan can attribute (a func literal assigned at top level, say), which makes
// the allowlist deny rather than allow.
func topLevelFuncName(line string) string {
	rest := strings.TrimPrefix(line, "func ")
	if strings.HasPrefix(rest, "(") {
		if end := strings.Index(rest, ")"); end >= 0 {
			rest = strings.TrimSpace(rest[end+1:])
		}
	}
	if open := strings.Index(rest, "("); open > 0 {
		return strings.TrimSpace(rest[:open])
	}
	return ""
}
