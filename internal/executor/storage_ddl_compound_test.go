package executor

import (
	"math/big"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// TestDDLCompoundTimestampNumericIndex verifies the TPC-H canonical
// (l_shipdate timestamp, l_orderkey numeric) compound index.
// Rows with the same shipdate are discriminated by orderkey.
func TestDDLCompoundTimestampNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE lineitem (id int, shipdate timestamp, orderkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "lineitem"})
	rel := ctx.Catalog.RelFileNode(tbl)

	date1995 := time.Date(1995, 1, 15, 0, 0, 0, 0, time.UTC)
	date1996 := time.Date(1996, 3, 1, 0, 0, 0, 0, time.UTC)

	rows := []Row{
		// (id, shipdate, orderkey)
		{{Kind: KindInt, Int: 1}, {Kind: KindTime, Time: date1995}, {Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0}},
		{{Kind: KindInt, Int: 2}, {Kind: KindTime, Time: date1995}, {Kind: KindNumeric, NumericMantissa: 2, NumericScale: 0}},
		{{Kind: KindInt, Int: 3}, {Kind: KindTime, Time: date1996}, {Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ship_order ON lineitem (shipdate, orderkey)"); err != nil {
		t.Fatalf("CREATE INDEX (timestamp, numeric): %v", err)
	}

	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_ship_order"})
	if !ok {
		t.Fatal("compound index not in catalog")
	}
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}

	// Probe: (1995-01-15, 2) — should find exactly 1 row.
	micros := date1995.Sub(pgEpoch).Microseconds()
	probeKey := append(btree.EncodeTimestamp(micros), btree.EncodeNumericKey(big.NewInt(2), 0)...)
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (1995-01-15, 2): found=%v err=%v", found, err)
	}

	// Range scan for all rows with shipdate in [1995-01-15, 1995-01-15].
	loKey := append(btree.EncodeTimestamp(micros), btree.EncodeNumericKey(big.NewInt(-9999999), 0)...)
	hiKey := append(btree.EncodeTimestamp(micros), btree.EncodeNumericKey(big.NewInt(9999999), 0)...)
	count := 0
	if err := tree.RangeScan(loKey, hiKey, func(_ []byte, _ storage.ItemPointer) (bool, error) {
		count++
		return true, nil
	}); err != nil {
		t.Fatalf("RangeScan: %v", err)
	}
	if count != 2 {
		t.Fatalf("range scan for 1995-01-15 rows: got %d, want 2", count)
	}
}

// TestDDLCompoundCharNumericIndex verifies the TPC-H
// (c_mktsegment char(10), c_custkey numeric) compound index.
// Padded and unpadded char values collapse to the same key prefix.
func TestDDLCompoundCharNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE customer (id int, mktseg char(10), custkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "customer"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []Row{
		{
			{Kind: KindInt, Int: 1},
			{Kind: KindString, String: "BUILDING  "}, // padded char(10)
			{Kind: KindNumeric, NumericMantissa: 5, NumericScale: 0},
		},
		{
			{Kind: KindInt, Int: 2},
			{Kind: KindString, String: "BUILDING  "},
			{Kind: KindNumeric, NumericMantissa: 7, NumericScale: 0},
		},
		{
			{Kind: KindInt, Int: 3},
			{Kind: KindString, String: "FURNITURE "},
			{Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0},
		},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_seg_cust ON customer (mktseg, custkey)"); err != nil {
		t.Fatalf("CREATE INDEX (char, numeric): %v", err)
	}

	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_seg_cust"})
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}

	// Probe with unpadded "BUILDING" — should find the custkey=5 row
	// because trim collapses "BUILDING  " and "BUILDING" to the same prefix.
	probeKey := append(btree.EncodeChar([]byte("BUILDING")), btree.EncodeNumericKey(big.NewInt(5), 0)...)
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (BUILDING, 5): found=%v err=%v", found, err)
	}

	// Total rows in index = 3.
	totalCount := 0
	if err := tree.RangeScan(
		append(btree.EncodeChar([]byte("")), btree.EncodeNumericKey(big.NewInt(-9999999), 0)...),
		append(btree.EncodeChar([]byte("ZZZZZ")), btree.EncodeNumericKey(big.NewInt(9999999), 0)...),
		func(_ []byte, _ storage.ItemPointer) (bool, error) {
			totalCount++
			return true, nil
		},
	); err != nil {
		t.Fatalf("full range scan: %v", err)
	}
	if totalCount != 3 {
		t.Fatalf("full range scan: got %d rows, want 3", totalCount)
	}
}

// TestDDLCompoundVarcharNumericIndex verifies (p_type varchar(25),
// p_partkey numeric) — the TPC-H part table index shape.
func TestDDLCompoundVarcharNumericIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE part (id int, ptype varchar(25), partkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "part"})
	rel := ctx.Catalog.RelFileNode(tbl)

	rows := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindString, String: "ECONOMY ANODIZED BRASS"}, {Kind: KindNumeric, NumericMantissa: 10, NumericScale: 0}},
		{{Kind: KindInt, Int: 2}, {Kind: KindString, String: "ECONOMY ANODIZED BRASS"}, {Kind: KindNumeric, NumericMantissa: 20, NumericScale: 0}},
		{{Kind: KindInt, Int: 3}, {Kind: KindString, String: "PROMO BRUSHED STEEL"}, {Kind: KindNumeric, NumericMantissa: 5, NumericScale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_type_part ON part (ptype, partkey)"); err != nil {
		t.Fatalf("CREATE INDEX (varchar, numeric): %v", err)
	}

	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_type_part"})
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}

	probeKey := append(
		btree.EncodeVarchar([]byte("ECONOMY ANODIZED BRASS")),
		btree.EncodeNumericKey(big.NewInt(20), 0)...,
	)
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("compound probe (ECONOMY ANODIZED BRASS, 20): found=%v err=%v", found, err)
	}
}

// TestDDLCompoundThreeColumnIndex verifies a 3-column
// (o_orderdate timestamp, o_custkey numeric, o_orderkey numeric) index.
func TestDDLCompoundThreeColumnIndex(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, "CREATE TABLE orders (id int, orderdate timestamp, custkey numeric, orderkey numeric)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "orders"})
	rel := ctx.Catalog.RelFileNode(tbl)

	d1 := time.Date(1996, 1, 2, 0, 0, 0, 0, time.UTC)
	d2 := time.Date(1996, 12, 1, 0, 0, 0, 0, time.UTC)

	rows := []Row{
		{{Kind: KindInt, Int: 1}, {Kind: KindTime, Time: d1}, {Kind: KindNumeric, NumericMantissa: 370, NumericScale: 0}, {Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0}},
		{{Kind: KindInt, Int: 2}, {Kind: KindTime, Time: d1}, {Kind: KindNumeric, NumericMantissa: 370, NumericScale: 0}, {Kind: KindNumeric, NumericMantissa: 2, NumericScale: 0}},
		{{Kind: KindInt, Int: 3}, {Kind: KindTime, Time: d2}, {Kind: KindNumeric, NumericMantissa: 100, NumericScale: 0}, {Kind: KindNumeric, NumericMantissa: 1, NumericScale: 0}},
	}
	for _, r := range rows {
		if err := writeHeapRow(ctx, rel, tbl.Columns, r); err != nil {
			t.Fatal(err)
		}
	}

	if err := runDDL(t, ctx, "CREATE INDEX idx_ord3 ON orders (orderdate, custkey, orderkey)"); err != nil {
		t.Fatalf("CREATE INDEX (timestamp, numeric, numeric): %v", err)
	}

	idx, _ := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "idx_ord3"})
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatal(err)
	}

	// Probe: (1996-01-02, 370, 2) — should hit row id=2.
	micros := d1.Sub(pgEpoch).Microseconds()
	probeKey := append(
		btree.EncodeTimestamp(micros),
		append(
			btree.EncodeNumericKey(big.NewInt(370), 0),
			btree.EncodeNumericKey(big.NewInt(2), 0)...,
		)...,
	)
	_, found, err := tree.Search(probeKey)
	if err != nil || !found {
		t.Fatalf("3-column compound probe: found=%v err=%v", found, err)
	}

	// Index should contain all 3 rows.
	count := 0
	if err := tree.RangeScan(
		btree.EncodeTimestamp(dateMicrosCompound(1990, 1, 1)),
		btree.EncodeTimestamp(dateMicrosCompound(2030, 1, 1)),
		func(_ []byte, _ storage.ItemPointer) (bool, error) {
			count++
			return true, nil
		},
	); err != nil {
		t.Fatalf("full range scan: %v", err)
	}
	if count != 3 {
		t.Fatalf("3-column index: got %d rows, want 3", count)
	}
}

// dateMicrosCompound is a local helper for this file.
func dateMicrosCompound(year, month, day int) int64 {
	t := time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	return t.Sub(pgEpoch).Microseconds()
}
