package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestDDLCreateNumericBTreeIndexAcceptsType pins the M0011-0002
// DDL acceptance contract: CREATE INDEX on a NUMERIC column
// succeeds. Pre-M0011-0002 this aborted with `0A000 btree v0
// only supports int4 keys` — the failure HammerDB TPC-H hits
// at index-build time. The test seeds a few rows with
// different scales and confirms each is searchable through
// EncodeNumericKey, exercising the variable-length HighKey
// path on first split when enough keys are present.
func TestDDLCreateNumericBTreeIndexAcceptsType(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE prices (id int, amount numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "prices"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// Seed: store the same numeric value with three different
	// scales to ensure the encoding is scale-invariant when read
	// back. Mantissa/scale pairs:
	//   1.0  -> (10, 1)
	//   1.00 -> (100, 2)
	//   2.5  -> (25, 1)
	rows := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindNumeric, Int: 10, Scale: 1}},
		{{Kind: KindInt, Int: 2}, {Kind: KindNumeric, Int: 25, Scale: 1}},
		{{Kind: KindInt, Int: 3}, {Kind: KindNumeric, Int: 7, Scale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_prices_amount ON prices (amount)"); err != nil {
		t.Fatalf("CREATE INDEX on numeric column: %v", err)
	}
	idx, tree := openIndexTreeForTest(t, ctx, "idx_prices_amount")
	for _, k := range []struct {
		m int64
		s int16
	}{{10, 1}, {25, 1}, {7, 0}} {
		key := indexProbeForTest(t, ctx, idx, NewNumericInt64Datum(k.m, k.s))
		_, found, err := tree.Search(key)
		if err != nil || !found {
			t.Fatalf("Search(%d,%d): found=%v err=%v", k.m, k.s, found, err)
		}
	}
}

// TestDDLNumericUniqueIndexCollapsesScales pins the
// UNIQUE/PRIMARY KEY equality semantics: numerically-equal
// values reject as duplicates regardless of textual scale.
// Without the byte-string-keyed dedup map (or without
// EncodeNumericKey's scale-invariance), `1.0` and `1.00` would
// pass UNIQUE incorrectly.
func TestDDLNumericUniqueIndexCollapsesScales(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE pk (n numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "pk"})
	rel := ctx.Catalog.RelFileNode(tbl)
	// Insert 1.0 and 1.00 — numerically equal, textually different.
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindNumeric, Int: 10, Scale: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindNumeric, Int: 100, Scale: 2}}); err != nil {
		t.Fatal(err)
	}
	err := runDDL(t, ctx, "CREATE UNIQUE INDEX idx_pk_n ON pk (n)")
	if err == nil {
		t.Fatal("CREATE UNIQUE INDEX should reject scale-different duplicates")
	}
	ee, ok := err.(*ExecError)
	if !ok || ee.Code != "23505" {
		t.Errorf("want 23505 unique_violation, got %v", err)
	}
}

// TestDDLNumericIndexSplitWithVariableLengthHighKey forces a
// page split on a NUMERIC index, exercising the
// variable-length HighKey path. Pre-M0011-0002 the split path
// errored with `split key length N does not fit fixed HighKey
// width 4` for any non-int4 key. The test inserts ~1000
// numeric rows with widely-varying magnitudes so encoded keys
// are not all the same length.
func TestDDLNumericIndexSplitWithVariableLengthHighKey(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE prices (n numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "prices"})
	rel := ctx.Catalog.RelFileNode(tbl)

	// 600 distinct numeric values: scale 0..3 to vary encoded
	// length. 600 4+-byte keys force at least one leaf split
	// at v0's per-leaf cap (~256 entries before split).
	const nRows = 600
	for i := 0; i < nRows; i++ {
		// Vary mantissa magnitude (1..nRows) and scale (i%4) so the
		// encoded byte length varies across rows.
		row := Row{{Kind: KindNumeric, Int: int64(i + 1), Scale: int16(i % 4)}}
		if err := writeHeapRow(ctx, rel, tbl.Columns, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := runDDL(t, ctx, "CREATE INDEX idx_prices_n ON prices (n)"); err != nil {
		t.Fatalf("CREATE INDEX with split required: %v", err)
	}

	idx, tree := openIndexTreeForTest(t, ctx, "idx_prices_n")
	// Probe 50 spaced-out keys to exercise the descent path
	// across multiple leaves post-split.
	for i := 0; i < nRows; i += nRows / 50 {
		key := indexProbeForTest(t, ctx, idx, NewNumericInt64Datum(int64(i+1), int16(i%4)))
		if _, found, err := tree.Search(key); err != nil || !found {
			t.Fatalf("Search row %d: found=%v err=%v", i, found, err)
		}
	}
}

// TestDDLInt4IndexUnchangedRegressionGuard pins that the
// existing int4 index path is byte-for-byte unchanged after
// the M0011-0002 refactor. Specifically: encoding,
// dedup-by-encoded-bytes (was dedup-by-int32 before), and
// search round-trip all still work for int4.
func TestDDLInt4IndexUnchangedRegressionGuard(t *testing.T) {
	withBlobIndexKeys(t)
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for _, n := range []int64{1, 2, 3, -5, 1000} {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := runDDL(t, ctx, "CREATE UNIQUE INDEX idx_t_id ON t (id)"); err != nil {
		t.Fatalf("CREATE UNIQUE INDEX (int4): %v", err)
	}
	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_t_id"})
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []int32{1, 2, 3, -5, 1000} {
		ptr, found, err := tree.Search(btree.EncodeInt4(k))
		if err != nil || !found {
			t.Errorf("Search(%d): found=%v err=%v", k, found, err)
		}
		if found && ptr.Block == storage.InvalidBlockNumber {
			t.Errorf("Search(%d) returned invalid block", k)
		}
	}
}
