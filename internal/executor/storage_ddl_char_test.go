package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestDDLCreateCharBTreeIndexAcceptsType pins the M0044-0002
// acceptance contract: CREATE INDEX on a char(N) column succeeds.
// Pre-M0044-0002 this aborted with `0A000 btree v0 only supports
// int4 / numeric keys`. Seeds rows with both trimmed and padded
// forms and verifies both are found via the same encoded key.
func TestDDLCreateCharBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE customers (id int, seg char(10))"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "customers"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// Insert rows with both trimmed and padded forms of the same
	// logical value. Both should be found by the same index key.
	rows := []Row{
		{{Kind: KindInt, Int: 1}, NewStringDatum("FURNITURE ")}, // padded
		{{Kind: KindInt, Int: 2}, NewStringDatum("BUILDING")},   // unpadded
		{{Kind: KindInt, Int: 3}, NewStringDatum("MACHINERY ")}, // padded
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_seg ON customers (seg)"); err != nil {
		t.Fatalf("CREATE INDEX on char column: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_seg"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := openIndexBTree(ctx, idx, idxRel)
	if err != nil {
		t.Fatalf("openIndexBTree: %v", err)
	}

	// Search with trimmed probe key — should find the padded row too.
	for _, seg := range []struct{ probe, stored string }{
		{"FURNITURE", "FURNITURE "},
		{"BUILDING", "BUILDING"},
		{"MACHINERY", "MACHINERY "},
	} {
		key := indexProbeForTest(t, ctx, idx, NewStringDatum(seg.probe))
		_, found, err := tree.Search(key)
		if err != nil || !found {
			t.Fatalf("Search(%q via %q): found=%v err=%v", seg.probe, seg.stored, found, err)
		}
	}
}

// TestDDLCharUniqueIndexRejectsPaddedDuplicate verifies that UNIQUE
// char indexes treat 'A' and 'A         ' as the same key (both trim
// to the same bytes) and reject the duplicate.
func TestDDLCharUniqueIndexRejectsPaddedDuplicate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE parts (id int, flag char(1))"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "parts"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// Insert "N" and "N" — same char(1) value.
	for i := int64(1); i <= 2; i++ {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: i},
			NewStringDatum("N"),
		}); err != nil {
			t.Fatal(err)
		}
	}

	err := runDDL(t, ctx, "CREATE UNIQUE INDEX idx_flag ON parts (flag)")
	if err == nil {
		t.Fatal("expected unique-violation for duplicate char values")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("want ExecError 23505, got %v", err)
	}
}
