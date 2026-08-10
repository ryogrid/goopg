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

	blob, err := encodeIndexKeyFromCols(idx, cols, row, nil)
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

	want, eerr := encodeBTreeKeyForColumn(NewIntDatum(42), c, 0)
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

func TestHashIndexIsNeverDescribableSoTheSSIBucketPairingHolds(t *testing.T) {
	// The SSI hash bucket is computed from the WRITER's fingerprint
	// (ssiRecordHashIndexInsert) and from the READER's scan search key
	// (ssiRecordHashBucketRead, handed operators_index.go's loBytes, which comes
	// from the format-aware scan funnel). Those two agree only while the index
	// is not describable — otherwise the reader would hash a tuple image and the
	// writer a blob, and every rw-edge on a hash index would be lost silently.
	// buildPGIndexKeyDesc's access-method refusal is what guarantees it.
	withPGIndexTupleKeys(t)
	ctx, idx, _ := rowKeyCtxAndIndex(t, 463, col("a", "int4"))
	idx.Method = "hash"
	idx.DeclaredHash = true
	if desc := ctx.pgIndexKeyDesc(idx); desc != nil {
		t.Fatalf("a hash index became describable (%d key attrs); the SSI bucket pairing would split", desc.NKeyAtts())
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
