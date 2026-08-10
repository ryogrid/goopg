package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// M0130-S11.4 slice 3b-2c-ii-A guards.
//
// The plumbing's whole job is that a descriptor REACHES the tree, and that an
// index this layer refuses to describe still opens with nil instead of
// failing. Both are invisible with the gate off (the shipped state), so these
// tests flip `pgIndexTupleKeys` for their duration — that is what the var
// exists for. The third property, memoisation, is not a micro-optimisation:
// the callers are per-row index maintenance, and re-deriving a REFUSAL per row
// is the expensive case (buildPGIndexKeyDesc walks every key column before it
// can conclude it cannot describe the index).

// withPGIndexTupleKeys turns the gate on for the duration of a test.
func withPGIndexTupleKeys(t *testing.T) {
	t.Helper()
	prev := pgIndexTupleKeys
	pgIndexTupleKeys = true
	t.Cleanup(func() { pgIndexTupleKeys = prev })
}

func TestPGIndexKeyDescGateOffYieldsNil(t *testing.T) {
	// The gate defaults to ON since the flip (3b-2c-ii-B2-c); it survives as a
	// var because the blob path stays live for the indexes the resolver refuses,
	// and this pins that turning it off really does put EVERY index back on that
	// path rather than leaving a half-flipped mix.
	withBlobIndexKeys(t)
	tbl := keyDescTable(col("a", "int4"))
	idx := &catalog.Index{OID: 100, Name: "i", Table: tbl, Method: "btree", Columns: []string{"a"}}
	// Sanity: this index IS describable, so a nil result here can only come
	// from the gate — otherwise the test would pass for the wrong reason.
	if _, err := buildPGIndexKeyDesc(idx); err != nil {
		t.Fatalf("fixture index is not describable: %v", err)
	}
	ctx := &Context{}
	if got := ctx.pgIndexKeyDesc(idx); got != nil {
		t.Fatalf("gate off: descriptor = %+v, want nil (the on-disk keys are blobs)", got)
	}
	if ctx.pgKeyDescCache != nil {
		t.Errorf("gate off must not even allocate the cache, got %v", ctx.pgKeyDescCache)
	}
}

func TestPGIndexKeyDescReachesOptions(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"), col("b", "text"))
	idx := &catalog.Index{OID: 101, Name: "i", Table: tbl, Method: "btree", Columns: []string{"a", "b"}}
	ctx := &Context{}
	desc := ctx.pgIndexKeyDesc(idx)
	if desc == nil {
		t.Fatal("descriptor = nil for a describable int4/text index")
	}
	if desc.NKeyAtts() != 2 {
		t.Fatalf("NKeyAtts = %d, want 2", desc.NKeyAtts())
	}
	// indexBTreeOptions is what the three helpers hand to the btree package;
	// if the descriptor is dropped here, every open site silently reverts to
	// bytewise and the flip in 3b-2c-ii-B mis-orders instead of failing.
	opts := indexBTreeOptions(ctx, idx)
	if opts.KeyDesc != desc {
		t.Errorf("indexBTreeOptions dropped the descriptor: got %v, want %v", opts.KeyDesc, desc)
	}
}

func TestPGIndexKeyDescUndescribableIsNilNotError(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"))
	// An expression key (Columns[i] == "") is the canonical refusal: its
	// output type is not recorded in the catalog, so no comparator can be
	// named for it. Such an index must keep the blob path, not break.
	idx := &catalog.Index{OID: 102, Name: "i", Table: tbl, Method: "btree", Columns: []string{""}}
	ctx := &Context{}
	if got := ctx.pgIndexKeyDesc(idx); got != nil {
		t.Fatalf("expression index: descriptor = %+v, want nil", got)
	}
	if got := indexBTreeOptions(ctx, idx).KeyDesc; got != nil {
		t.Fatalf("expression index: Options.KeyDesc = %+v, want nil", got)
	}
}

func TestPGIndexKeyDescMemoisesBothOutcomes(t *testing.T) {
	withPGIndexTupleKeys(t)
	tbl := keyDescTable(col("a", "int4"))
	ok := &catalog.Index{OID: 103, Name: "ok", Table: tbl, Method: "btree", Columns: []string{"a"}}
	bad := &catalog.Index{OID: 104, Name: "bad", Table: tbl, Method: "btree", Columns: []string{""}}
	ctx := &Context{}

	first := ctx.pgIndexKeyDesc(ok)
	if first == nil {
		t.Fatal("describable index yielded nil")
	}
	if second := ctx.pgIndexKeyDesc(ok); second != first {
		t.Errorf("success not memoised: got a different descriptor on the second call")
	}
	if ctx.pgIndexKeyDesc(bad) != nil {
		t.Fatal("undescribable index yielded a descriptor")
	}
	// The refusal must be CACHED, not merely nil — a present-but-nil entry.
	// Without the comma-ok read in pgIndexKeyDesc this map lookup would miss
	// forever and re-derive the failure for every row written.
	if _, cached := ctx.pgKeyDescCache[bad.OID]; !cached {
		t.Error("refusal not memoised: bad.OID absent from the cache")
	}
	if len(ctx.pgKeyDescCache) != 2 {
		t.Errorf("cache size = %d, want 2", len(ctx.pgKeyDescCache))
	}

	// Cache is keyed by OID and consulted before the catalog, so a stale entry
	// wins for the rest of the statement. That is the documented contract
	// (ALTER TABLE holds AccessExclusiveLock); assert it rather than leave it
	// to be discovered.
	mutated := &catalog.Index{OID: 103, Name: "ok", Table: keyDescTable(col("a", "text")), Method: "btree", Columns: []string{"a"}}
	if got := ctx.pgIndexKeyDesc(mutated); got != first {
		t.Error("same-OID lookup did not hit the memo")
	}
}

// A nil index must not panic: the arbiter/leaf-index open sites can reach the
// helper with no catalog index resolved.
func TestPGIndexKeyDescNilIndex(t *testing.T) {
	withPGIndexTupleKeys(t)
	ctx := &Context{}
	if got := ctx.pgIndexKeyDesc(nil); got != nil {
		t.Fatalf("nil index: descriptor = %+v, want nil", got)
	}
}
