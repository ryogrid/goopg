package executor

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c-iii guards — the probe-key funnel.
//
// The same six scan sites B2-c-ii funnelled at the HIGH end built the LOW end
// (the equality probe / the range bounds) by calling `encodeBTreeKeyForColumn`
// per attribute and concatenating. Concatenation is the blob format's key
// layout; under the tuple format the key is one FormPGIndexTuple image and a
// short probe is a PIVOT carrying its own attribute count. These tests pin both
// branches, the prefix-only contract, and the funnel itself.

func probeCtxAndIndex(t *testing.T, oid uint32, cols ...catalog.Column) (*Context, *catalog.Index) {
	t.Helper()
	tbl := keyDescTable(cols...)
	names := make([]string, len(cols))
	for i := range cols {
		names[i] = cols[i].Name
	}
	return &Context{}, &catalog.Index{OID: oid, Name: "i", Table: tbl, Method: "btree", Columns: names}
}

func TestIndexProbeKeyBlobIsTheConcatenation(t *testing.T) {
	withBlobIndexKeys(t)
	// Gate off is the shipped state, so the funnel must reproduce the
	// pre-slice bytes exactly — including the multi-attribute case, where the
	// old code appended segment after segment with no separator.
	ctx, idx := probeCtxAndIndex(t, 300, col("a", "int4"), col("b", "text"))
	a, b := NewIntDatum(42), NewStringDatum("xyz")

	got, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{
		{col: &idx.Table.Columns[0], val: a},
		{col: &idx.Table.Columns[1], val: b},
	})
	if encErr != nil {
		t.Fatalf("indexProbeKey: %v", encErr)
	}
	ka, e1 := encodeBTreeKeyForColumn(a, &idx.Table.Columns[0], 0)
	kb, e2 := encodeBTreeKeyForColumn(b, &idx.Table.Columns[1], 0)
	if e1 != nil || e2 != nil {
		t.Fatalf("fixture encode failed: %v %v", e1, e2)
	}
	if want := append(append([]byte{}, ka...), kb...); !bytes.Equal(got, want) {
		t.Fatalf("blob probe = %x, want the concatenation %x", got, want)
	}

	// And a leading-attribute probe is still just the first segment: that byte
	// prefix is what makes the blob range scan position correctly.
	lead, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{{col: &idx.Table.Columns[0], val: a}})
	if encErr != nil {
		t.Fatalf("indexProbeKey(prefix): %v", encErr)
	}
	if !bytes.Equal(lead, ka) {
		t.Fatalf("blob prefix probe = %x, want %x", lead, ka)
	}
}

func TestIndexProbeKeyTupleIsAnIndexTuple(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx := probeCtxAndIndex(t, 301, col("a", "int4"), col("b", "text"))
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("fixture index is not describable; the test would pass for the wrong reason")
	}
	a, b := NewIntDatum(42), NewStringDatum("xyz")

	full, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{
		{col: &idx.Table.Columns[0], val: a},
		{col: &idx.Table.Columns[1], val: b},
	})
	if encErr != nil {
		t.Fatalf("indexProbeKey: %v", encErr)
	}
	// The image, and the SEARCH-key TID: zero, i.e. minus infinity in the
	// heapkeyspace tiebreaker, so an equality probe lands before every real
	// entry sharing its key attributes.
	want, _, err := pgIndexTupleKey(desc, []*catalog.Column{&idx.Table.Columns[0], &idx.Table.Columns[1]},
		[]Datum{a, b}, storage.ItemPointer{})
	if err != nil {
		t.Fatalf("pgIndexTupleKey: %v", err)
	}
	if !bytes.Equal(full, want) {
		t.Fatalf("tuple probe = %x, want %x", full, want)
	}
	if n := btree.BTreeTupleGetNAtts(full, 2); n != 2 {
		t.Errorf("full probe reports %d attributes, want 2", n)
	}

	// A one-attribute probe on the same index is NOT the first half of the
	// two-attribute image; it is a pivot stamped with its own attribute count,
	// which is what makes it minus infinity beyond attribute 1.
	prefix, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{{col: &idx.Table.Columns[0], val: a}})
	if encErr != nil {
		t.Fatalf("indexProbeKey(prefix): %v", encErr)
	}
	if n := btree.BTreeTupleGetNAtts(prefix, 2); n != 1 {
		t.Errorf("prefix probe reports %d attributes, want 1", n)
	}
	if bytes.HasPrefix(full, prefix) {
		t.Error("prefix probe is a byte prefix of the full key — the pivot stamp is missing")
	}
}

func TestIndexProbeKeyUndescribableIndexKeepsBlob(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx := probeCtxAndIndex(t, 302, col("a", "int4"))
	// An expression key is the canonical refusal (see
	// TestPGIndexKeyDescUndescribableIsNilNotError); such an index keeps the
	// blob path whole even with the gate on. Asserting the dual-format
	// property here rather than assuming it is the same discipline B2-c-ii
	// applied to the upper bound.
	idx.Columns = []string{"", "a"}
	if ctx.pgIndexKeyDesc(idx) != nil {
		t.Fatal("fixture index unexpectedly describable")
	}
	a := NewIntDatum(7)
	got, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{{col: &idx.Table.Columns[0], val: a}})
	if encErr != nil {
		t.Fatalf("indexProbeKey: %v", encErr)
	}
	want, e := encodeBTreeKeyForColumn(a, &idx.Table.Columns[0], 0)
	if e != nil {
		t.Fatalf("fixture encode failed: %v", e)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("undescribable index probe = %x, want the blob encoding %x", got, want)
	}
}

func TestIndexProbeKeyTupleRejectsNonLeadingAttributes(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx, idx := probeCtxAndIndex(t, 303, col("a", "int4"), col("b", "int4"))

	// Under the blob format a site that probed with the WRONG attribute just
	// produced a key that matched nothing. A pivot means "the first N
	// attributes", so the same mistake would silently scan the wrong range —
	// name it instead.
	if _, encErr := ctx.indexProbeKey(idx, []indexProbeKeyPart{{col: &idx.Table.Columns[1], val: NewIntDatum(1)}}); encErr == nil {
		t.Error("probing key attribute 1 with column b was accepted")
	}
	// More values than the index has key attributes cannot be a pivot at all.
	three := []indexProbeKeyPart{
		{col: &idx.Table.Columns[0], val: NewIntDatum(1)},
		{col: &idx.Table.Columns[1], val: NewIntDatum(2)},
		{col: &idx.Table.Columns[1], val: NewIntDatum(3)},
	}
	if _, encErr := ctx.indexProbeKey(idx, three); encErr == nil {
		t.Error("a 3-attribute probe on a 2-attribute index was accepted")
	}
}

func TestIndexProbeKeyIsTheOnlyScanSideEncoder(t *testing.T) {
	// The funnel only holds if it cannot be bypassed: a scan site calling
	// encodeBTreeKeyForColumn directly would hand a tuple-format tree a
	// concatenated blob, and the failure would surface as wrong ROWS, never as
	// a compile error.
	//
	// Scope is the three SCAN files. operators_storage.go is deliberately not
	// scanned: its remaining direct callers are the writer-side uniqueness and
	// index-maintenance paths, which the flip (B2-c) converts to
	// pgIndexTupleKeyFromRow — a different funnel. Its one scan site (the
	// UPDATE-by-index probe) is covered by the flip's own gates.
	const needle = "encodeBTreeKeyForColumn("
	for _, f := range []string{"operators_index.go", "operators_indexonly.go", "operators_bitmap.go"} {
		src, err := os.ReadFile(filepath.Clean(f))
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for i, line := range strings.Split(string(src), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "//") {
				continue // prose may name it
			}
			if strings.Contains(line, needle) {
				t.Errorf("%s:%d bypasses (*Context).indexProbeKey: %s", f, i+1, trimmed)
			}
		}
	}
}
