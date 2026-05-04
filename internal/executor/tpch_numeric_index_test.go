package executor

import (
	"math/big"
	"testing"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestTPCHNumericSingleColumnIndexesAccepted reproduces the failure
// class HammerDB hits at the "CREATING TPCH INDEXES" stage of
// bench/tpch/run_all.sh:
//
//	ERROR: btree v0 only supports int4 keys, got "numeric"
//
// (See docs/milestones/0011-btree-numeric-key-support.md for the
// captured Vuser log.)
//
// Pre-M0011, every CREATE INDEX over a NUMERIC column aborted with
// that 0A000. The test loads the eight HammerDB-shape TPC-H tables,
// inserts a small consistent slice of rows via the live executor
// stack, then issues the 13 single-column NUMERIC indexes from
// HammerDB/src/postgresql/pgolap.tcl lines 511-544. Every one must
// land cleanly. After that, two equality lookups run through the
// real planner/executor path so the probe-key encoding (introduced
// alongside M0011-0002) is exercised end-to-end.
//
// The four multi-column composite indexes HammerDB also issues
// (PARTSUPP_PK, LINEITEM_PK, LINEITEM_PART_SUPP_FKIDX) are out of
// B-tree v0 scope — TestTPCHCompositeIndexStillRejected pins their
// continued 0A000 rejection so a future loop adding composite
// support can flip the assertion deterministically.
func TestTPCHNumericSingleColumnIndexesAccepted(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range tpch.DDL() {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("schema DDL: %v\nSQL: %s", err, ddl)
		}
	}
	for _, ins := range tpch.SampleInserts() {
		if err := runDDL(t, ctx, ins); err != nil {
			t.Fatalf("seed INSERT: %v\nSQL: %s", err, ins)
		}
	}

	// Single-column NUMERIC indexes — the slice M0011 unblocks.
	// Names mirror HammerDB's exact identifiers so a grep against
	// pgolap.tcl line 511-544 maps 1:1.
	type idxDDL struct {
		name string
		sql  string
	}
	indexes := []idxDDL{
		{"region_pk", "CREATE INDEX region_pk ON region (r_regionkey)"},
		{"nation_pk", "CREATE INDEX nation_pk ON nation (n_nationkey)"},
		{"supplier_pk", "CREATE INDEX supplier_pk ON supplier (s_suppkey)"},
		{"part_pk", "CREATE INDEX part_pk ON part (p_partkey)"},
		{"customer_pk", "CREATE INDEX customer_pk ON customer (c_custkey)"},
		{"orders_pk", "CREATE INDEX orders_pk ON orders (o_orderkey)"},
		{"order_customer_fkidx", "CREATE INDEX order_customer_fkidx ON orders (o_custkey)"},
		{"partsupp_part_fkidx", "CREATE INDEX partsupp_part_fkidx ON partsupp (ps_partkey)"},
		{"partsupp_supplier_fkidx", "CREATE INDEX partsupp_supplier_fkidx ON partsupp (ps_suppkey)"},
		{"supplier_nation_fkidx", "CREATE INDEX supplier_nation_fkidx ON supplier (s_nationkey)"},
		{"customer_nation_fkidx", "CREATE INDEX customer_nation_fkidx ON customer (c_nationkey)"},
		{"nation_regionkey_fkidx", "CREATE INDEX nation_regionkey_fkidx ON nation (n_regionkey)"},
		{"idx_lineitem_orderkey_fkidx", "CREATE INDEX idx_lineitem_orderkey_fkidx ON lineitem (l_orderkey)"},
	}
	for _, ix := range indexes {
		if err := runDDL(t, ctx, ix.sql); err != nil {
			t.Errorf("CREATE INDEX %s: %v", ix.name, err)
		}
	}

	// Sanity-check one of the indexes via a direct btree.Search so
	// the bytes line up between backfill and a fresh probe.
	idx, ok := ctx.Catalog.LookupIndex(parser.ObjectName{Name: "orders_pk"})
	if !ok {
		t.Fatal("orders_pk missing from catalog after CREATE INDEX")
	}
	tree, err := btree.Open(ctx.Pool, ctx.Catalog.IndexRelFileNode(idx))
	if err != nil {
		t.Fatalf("btree.Open(orders_pk): %v", err)
	}
	// SampleInserts inserts orders 1..8 with NUMERIC o_orderkey.
	for _, k := range []int64{1, 4, 8} {
		if _, found, err := tree.Search(btree.EncodeNumericKey(big.NewInt(k), 0)); err != nil || !found {
			t.Errorf("orders_pk search o_orderkey=%d: found=%v err=%v", k, found, err)
		}
	}

	// And exercise the planner/executor path on a simple equality
	// query so the M0011-0002 NumericConst→IndexScan wiring is
	// covered. orders has 8 rows seeded by SampleInserts; the
	// scan should land via IndexScan and return exactly one row.
	stmts, perr := parser.Parse("SELECT o_orderkey FROM orders WHERE o_orderkey = 4")
	if perr != nil {
		t.Fatalf("parse: %v", perr)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// The outer node is a Project wrapping the chosen scan;
	// drill down looking for an IndexScan to confirm the
	// NumericConst→IndexScan path landed (M0011-0002 planner
	// change).
	if !planContainsIndexScan(plan) {
		t.Errorf("expected an IndexScan somewhere in plan, got %T", plan)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("open: %v", err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = op.Close()
	if len(rows) != 1 {
		t.Errorf("WHERE o_orderkey = 4 returned %d rows, want 1", len(rows))
	}
}

// planContainsIndexScan walks a plan tree (depth-first) and reports
// whether any node is an IndexScan or IndexOnlyScan. Used by the TPC-H
// integration test to confirm the planner picked the index path even when
// the outer node is a Project / Filter wrapper.
func planContainsIndexScan(n planner.Node) bool {
	if _, ok := n.(*planner.IndexScan); ok {
		return true
	}
	if _, ok := n.(*planner.IndexOnlyScan); ok {
		return true
	}
	switch v := n.(type) {
	case *planner.Project:
		return planContainsIndexScan(v.Child)
	case *planner.Filter:
		return planContainsIndexScan(v.Child)
	}
	return false
}

// TestTPCHCompositeIndexSucceeds verifies composite btree indexes
// work end-to-end for the HammerDB TPC-H composite shapes
// (partsupp: ps_partkey, ps_suppkey; lineitem: l_linenumber,
// l_orderkey; lineitem: l_partkey, l_suppkey).
func TestTPCHCompositeIndexSucceeds(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, ddl := range tpch.DDL() {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("schema DDL: %v", err)
		}
	}
	for _, sql := range []string{
		"CREATE INDEX partsupp_pk ON partsupp (ps_partkey, ps_suppkey)",
		"CREATE INDEX lineitem_pk ON lineitem (l_linenumber, l_orderkey)",
		"CREATE INDEX lineitem_part_supp_fkidx ON lineitem (l_partkey, l_suppkey)",
	} {
		if err := runDDL(t, ctx, sql); err != nil {
			t.Errorf("composite CREATE INDEX should succeed: %s: %v", sql, err)
		}
	}
}
