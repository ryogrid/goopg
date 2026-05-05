package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestDDLCreateVarcharBTreeIndexAcceptsType pins the M0044-0001
// acceptance contract: CREATE INDEX on a varchar(N) column succeeds.
// Pre-M0044-0001 this aborted with `0A000 btree v0 only supports
// int4 / numeric keys`. The test seeds a few string rows and verifies
// each value is searchable through the new EncodeVarchar path.
func TestDDLCreateVarcharBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE segments (id int, seg varchar(20))"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "segments"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindString, String: "FURNITURE"}},
		{{Kind: KindInt, Int: 2}, {Kind: KindString, String: "HOUSEHOLD"}},
		{{Kind: KindInt, Int: 3}, {Kind: KindString, String: "MACHINERY"}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_seg ON segments (seg)"); err != nil {
		t.Fatalf("CREATE INDEX on varchar column: %v", err)
	}
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_seg"})
	if !ok {
		t.Fatal("index not in catalog after CREATE INDEX")
	}
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatalf("btree.Open: %v", err)
	}
	for _, seg := range []string{"FURNITURE", "HOUSEHOLD", "MACHINERY"} {
		_, found, err := tree.Search(btree.EncodeVarchar([]byte(seg)))
		if err != nil || !found {
			t.Fatalf("Search(%q): found=%v err=%v", seg, found, err)
		}
	}
}

// TestDDLVarcharUniqueIndexRejectsDuplicates verifies that UNIQUE
// varchar indexes correctly detect duplicate values.
func TestDDLVarcharUniqueIndexRejectsDuplicates(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE parts (id int, ptype varchar(25))"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "parts"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// Insert two rows with the same ptype.
	for i := int64(1); i <= 2; i++ {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: i},
			{Kind: KindString, String: "PROMO BRUSHED STEEL"},
		}); err != nil {
			t.Fatal(err)
		}
	}

	err := runDDL(t, ctx, "CREATE UNIQUE INDEX idx_parts_ptype ON parts (ptype)")
	if err == nil {
		t.Fatal("expected unique-violation for duplicate varchar values")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Fatalf("want ExecError 23505, got %v", err)
	}
}

// TestDDLVarcharIndexSeqScanParity creates a varchar index and then
// scans using both RangeScan (index path) and a full table scan,
// verifying that the row counts match for a prefix query.
func TestDDLVarcharIndexSeqScanParity(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE mktseg (id int, seg varchar(10))"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "mktseg"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// 5 rows: 2 FURNITURE, 1 BUILDING, 1 HOUSEHOLD, 1 MACHINERY.
	data := []string{"FURNITURE", "BUILDING", "FURNITURE", "HOUSEHOLD", "MACHINERY"}
	for i, seg := range data {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{
			{Kind: KindInt, Int: int64(i + 1)},
			{Kind: KindString, String: seg},
		}); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_mktseg_seg ON mktseg (seg)"); err != nil {
		t.Fatalf("CREATE INDEX on varchar: %v", err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_mktseg_seg"})
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		t.Fatal(err)
	}

	// Count FURNITURE rows via RangeScan with exact key.
	targetKey := btree.EncodeVarchar([]byte("FURNITURE"))
	indexCount := 0
	if err := tree.RangeScan(targetKey, targetKey, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		indexCount++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if indexCount != 2 {
		t.Fatalf("RangeScan FURNITURE: got %d rows, want 2", indexCount)
	}

	// Count FURNITURE rows via sequential scan of heap.
	seqCount := 0
	for _, seg := range data {
		if seg == "FURNITURE" {
			seqCount++
		}
	}
	if seqCount != indexCount {
		t.Fatalf("parity: seqScan=%d indexScan=%d", seqCount, indexCount)
	}
}
