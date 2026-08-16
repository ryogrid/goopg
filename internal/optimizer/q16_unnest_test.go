package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestPlanQ16NotInUnnestsWithRealSchema is a permanent regression
// guard for TPC-H Q16 (M0122-0011 follow-up). A same-day deferral-
// ledger row filed after the initial non-correlated-NOT-IN unnest
// feature landed (be47cc93) reported that Q16's real join shape did
// not route through the new unnest path against a live,
// HammerDB-loaded server, with the root cause left unisolated.
//
// Re-investigating: this test plans Q16 against a schema mirroring
// the real HammerDB-equivalent one (PRIMARY KEY indexes on
// part/partsupp/supplier plus ANALYZE-like row-count stats, see
// internal/testutil/tpch/index_utilisation_test.go's hammerdbPKs) and
// the unnest DOES fire — confirmed independently against a freshly
// rebuilt server on the real bench/tpch/runtime_goopg/data SF1
// dataset (EXPLAIN shows a separate supplier probe join; row count
// 18192, matching the canonical baseline). The original "not
// triggering" observation could not be reproduced against any
// committed revision of internal/planner/unnest.go (including the
// very first be47cc93 version) once realistic indexes/stats are
// present, so it was most likely a stale/un-rebuilt server binary at
// observation time rather than a planner defect. This test pins the
// now-verified-correct behaviour so a future regression is caught.
func TestPlanQ16NotInUnnestsWithRealSchema(t *testing.T) {
	cat, err := tpch.Catalog()
	if err != nil {
		t.Fatalf("tpch.Catalog: %v", err)
	}
	mem, ok := cat.(*catalog.InMemory)
	if !ok {
		t.Fatalf("expected *catalog.InMemory, got %T", cat)
	}
	partsupp, ok := mem.LookupTable(parser.ObjectName{Name: "partsupp"})
	if !ok {
		t.Fatal("partsupp table not found")
	}
	part, ok := mem.LookupTable(parser.ObjectName{Name: "part"})
	if !ok {
		t.Fatal("part table not found")
	}
	supplier, ok := mem.LookupTable(parser.ObjectName{Name: "supplier"})
	if !ok {
		t.Fatal("supplier table not found")
	}
	// Mirrors index_utilisation_test.go's hammerdbPKs row-count
	// order of magnitude (SF1: partsupp=800k, part=200k, supplier=10k).
	partsupp.Stats = &catalog.TableStats{RowCount: 800000}
	part.Stats = &catalog.TableStats{RowCount: 200000}
	supplier.Stats = &catalog.TableStats{RowCount: 10000}
	if _, err := mem.CreateIndex(parser.ObjectName{Name: "part_pk"}, part, []string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatalf("CreateIndex part_pk: %v", err)
	}
	if _, err := mem.CreateIndex(parser.ObjectName{Name: "partsupp_pk"}, partsupp, []string{"ps_partkey", "ps_suppkey"}, true, "btree", true); err != nil {
		t.Fatalf("CreateIndex partsupp_pk: %v", err)
	}
	if _, err := mem.CreateIndex(parser.ObjectName{Name: "supplier_pk"}, supplier, []string{"s_suppkey"}, true, "btree", true); err != nil {
		t.Fatalf("CreateIndex supplier_pk: %v", err)
	}

	node, err := Plan(parseOne(t, tpch.Queries()[16]), cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	j := findFirstJoinByType(node, JoinTypeAnti)
	if j == nil {
		t.Fatalf("Q16: expected a NullAware Anti join from the NOT IN unnest, got plan:\n%s", planTreeString(node))
	}
	if !j.NullAware {
		t.Error("Q16: Anti join from NOT IN must be NullAware")
	}
}
