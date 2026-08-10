package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0130-S11.4 slice 3b-2c-ii-B2-c (the flip) — shared test helpers for the
// suites that inspect an index's btree DIRECTLY rather than through a query.
//
// Those tests all predate the tuple key format and all encoded the same
// assumption twice over: they opened the tree with `btree.Open` (which cannot
// name a key descriptor, so the tree compares bytewise) and probed it with
// `btree.EncodeInt4`/`EncodeVarchar`/… (the blob encoding). Both were true by
// construction while goopg had ONE key format; neither is a property those
// tests are about. "Can a timestamp column be indexed and found again?" is a
// question about the type, not about how the key is laid out.
//
// So they now go through the engine's own two funnels — `openIndexBTree` for
// the tree and `indexProbeKey` for the search key — which is what the executor
// itself uses and therefore tracks whichever format the index resolves to. The
// format-SPECIFIC assertions did not move: the blob encoding still has its own
// byte-for-byte guards (pgindex_probekey_test.go and friends), pinned with
// `withBlobIndexKeys`.

// openIndexTreeForTest looks up a named index and opens its btree the way the
// executor does — with the key descriptor when the index is describable, and
// without when it is not.
func openIndexTreeForTest(t *testing.T, ctx *Context, idxName string) (*catalog.Index, *btree.BTree) {
	t.Helper()
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: idxName})
	if !ok {
		t.Fatalf("index %q not in catalog", idxName)
	}
	tree, err := openIndexBTree(ctx, idx, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("openIndexBTree(%s): %v", idxName, err)
	}
	return idx, tree
}

// indexProbeForTest builds the equality search key for idx's leading key
// column, in whichever format that index uses. It is the test-side spelling of
// what an index scan does for `WHERE <leading column> = <val>`.
func indexProbeForTest(t *testing.T, ctx *Context, idx *catalog.Index, val Datum) []byte {
	t.Helper()
	cols := pgIndexKeyColumns(idx)
	if len(cols) == 0 || cols[0] == nil {
		// An index this layer cannot resolve columns for is a blob index by
		// definition; fall back to the same per-column encoder the blob path
		// uses so the helper still works for it.
		col, ok := ctx.Catalog.LookupColumn(idx.Table, idx.Columns[0])
		if !ok {
			t.Fatalf("index %q: leading key column %q not in catalog", idx.Name, idx.Columns[0])
		}
		key, encErr := encodeBTreeKeyForColumn(val, col, 0)
		if encErr != nil {
			t.Fatalf("encodeBTreeKeyForColumn(%s): %v", idx.Name, encErr)
		}
		return key
	}
	key, err := ctx.indexProbeKey(idx, []indexProbeKeyPart{{col: cols[0], val: val}})
	if err != nil {
		t.Fatalf("indexProbeKey(%s): %v", idx.Name, err)
	}
	return key
}

// withBlobIndexKeys forces the BLOB key format for the duration of a test, the
// mirror of pgindex_btree_test.go's withPGIndexTupleKeys.
//
// It is not a way to opt out of the flip: the blob path is still LIVE for every
// index `buildPGIndexKeyDesc` refuses (an expression key, an explicit operator
// class, a non-bytewise collation, a type with no comparator or a type whose
// goopg image is not PostgreSQL's — numeric and uuid today), so a tree's format
// is a per-index property and the blob encoders' byte-for-byte guards have to
// keep running. This is how they pin it now that the gate defaults to on.
func withBlobIndexKeys(t *testing.T) {
	t.Helper()
	prev := pgIndexTupleKeys
	pgIndexTupleKeys = false
	t.Cleanup(func() { pgIndexTupleKeys = prev })
}
