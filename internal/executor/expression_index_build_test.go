package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/nbtree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// countIndexEntries walks every entry of idx's physical btree and returns how
// many are stored. Used by the expression-index build tests below, which assert
// on physical index contents rather than on query results: a goopg index scan
// can fall back to a sequential scan and would then report the right rows over
// an empty index, hiding exactly the bug under test.
func countIndexEntries(t *testing.T, ctx *Context, idx *catalog.Index) int {
	t.Helper()
	tree, err := nbtree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("nbtree.Open(%s): %v", idx.Name, err)
	}
	n := 0
	if err := tree.RangeScan(nil, nil, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		n++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan(%s): %v", idx.Name, err)
	}
	return n
}

func lookupIndexByName(t *testing.T, ctx *Context, name string) *catalog.Index {
	t.Helper()
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: name})
	if !ok {
		t.Fatalf("index %q not found in catalog", name)
	}
	return idx
}

// TestExpressionIndexBuildIndexesExistingRows covers the CREATE INDEX bulk-build
// path for expression key columns (M0119-0006).
//
// The bug: createBTreeIndex left cols[i] == nil for an expression key column
// (idx.Columns[i] == ""), and collectBTreeEntries skipped every such column, so
// an index whose only key column was an expression produced NO key bytes at all
// and every pre-existing row was dropped — `CREATE INDEX ON t(lower(b))` over a
// populated table built a physically EMPTY index. Rows inserted AFTERWARDS were
// indexed normally, because the runtime maintain path (encodeExprIndexKey in
// operators_storage.go) does evaluate the stored ColExprs. The two paths are
// siblings and must agree; this test pins the build side.
func TestExpressionIndexBuildIndexesExistingRows(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TABLE exprbuild_t (a int4, b text)",
		"INSERT INTO exprbuild_t VALUES (1, 'Alpha')",
		"INSERT INTO exprbuild_t VALUES (2, 'BRAVO')",
		"INSERT INTO exprbuild_t VALUES (3, 'charlie')",
		// Built over the three rows above — the regression case.
		"CREATE INDEX exprbuild_lower ON exprbuild_t (lower(b))",
		// Mixed plain + expression key columns.
		"CREATE INDEX exprbuild_mixed ON exprbuild_t (a, lower(b))",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}

	for _, name := range []string{"exprbuild_lower", "exprbuild_mixed"} {
		if got := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, name)); got != 3 {
			t.Errorf("%s: %d index entries after build over 3 pre-existing rows, want 3", name, got)
		}
	}

	// A row inserted after the build goes through the runtime maintain path;
	// the two paths must reach the same total.
	if err := runDDL(t, ctx, "INSERT INTO exprbuild_t VALUES (4, 'Delta')"); err != nil {
		t.Fatalf("INSERT after build: %v", err)
	}
	if got := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, "exprbuild_lower")); got != 4 {
		t.Errorf("exprbuild_lower: %d entries after post-build INSERT, want 4", got)
	}
}

// TestReindexExpressionIndexKeepsEntries covers the REINDEX side of the same
// gap (M0119-0006): rebuildIndex reuses the bulk-build path, so before the fix
// REINDEX on an expression index THREW AWAY the entries that INSERT-time
// maintenance had written, leaving an empty index behind — a silent data loss
// for anything that trusted the index.
func TestReindexExpressionIndexKeepsEntries(t *testing.T) {
	ctx, cleanup := newVMFixture(t)
	defer cleanup()
	ctx.CurrentDatabaseOid = catalog.DefaultDBOid

	for _, sql := range []string{
		"CREATE TABLE exprreidx_t (a int4, b text)",
		"CREATE INDEX exprreidx_lower ON exprreidx_t (lower(b))",
		"INSERT INTO exprreidx_t VALUES (1, 'Alpha')",
		"INSERT INTO exprreidx_t VALUES (2, 'BRAVO')",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
	}
	before := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, "exprreidx_lower"))
	if before != 2 {
		t.Fatalf("%d entries from the runtime maintain path, want 2", before)
	}

	if err := runDDL(t, ctx, "REINDEX INDEX exprreidx_lower"); err != nil {
		t.Fatalf("REINDEX: %v", err)
	}
	if got := countIndexEntries(t, ctx, lookupIndexByName(t, ctx, "exprreidx_lower")); got != before {
		t.Errorf("REINDEX left %d entries, want %d (rebuild must not drop expression keys)", got, before)
	}
}
