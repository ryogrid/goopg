package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Mirror the live HammerDB column ordering.
func TestPlanQ19LiveSQL(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_shipdate", Type: catalog.Type{Name: "date"}},
		{Name: "l_orderkey", Type: catalog.Type{Name: "int8"}},
		{Name: "l_discount", Type: catalog.Type{Name: "int8"}},
		{Name: "l_extendedprice", Type: catalog.Type{Name: "int8"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "int8"}},
		{Name: "l_quantity", Type: catalog.Type{Name: "int8"}},
		{Name: "l_returnflag", Type: catalog.Type{Name: "varchar"}},
		{Name: "l_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "l_linestatus", Type: catalog.Type{Name: "varchar"}},
		{Name: "l_tax", Type: catalog.Type{Name: "int8"}},
		{Name: "l_commitdate", Type: catalog.Type{Name: "date"}},
		{Name: "l_receiptdate", Type: catalog.Type{Name: "date"}},
		{Name: "l_shipmode", Type: catalog.Type{Name: "varchar"}},
		{Name: "l_linenumber", Type: catalog.Type{Name: "int8"}},
		{Name: "l_shipinstruct", Type: catalog.Type{Name: "varchar"}},
		{Name: "l_comment", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int8"}},
		{Name: "p_type", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_size", Type: catalog.Type{Name: "int8"}},
		{Name: "p_brand", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_name", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_container", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_mfgr", Type: catalog.Type{Name: "varchar"}},
		{Name: "p_retailprice", Type: catalog.Type{Name: "int8"}},
		{Name: "p_comment", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	q19 := `select sum(l_extendedprice* (1 - l_discount)) as revenue from lineitem, part where ( p_partkey = l_partkey and p_brand = 'Brand#12' and p_container in ('SM CASE', 'SM BOX', 'SM PACK', 'SM PKG') and l_quantity >= 1 and l_quantity <= 1 + 10 and p_size between 1 and 5 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON') or ( p_partkey = l_partkey and p_brand = 'Brand#23' and p_container in ('MED BAG', 'MED BOX', 'MED PKG', 'MED PACK') and l_quantity >= 10 and l_quantity <= 10 + 10 and p_size between 1 and 10 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON') or ( p_partkey = l_partkey and p_brand = 'Brand#34' and p_container in ('LG CASE', 'LG BOX', 'LG PACK', 'LG PKG') and l_quantity >= 20 and l_quantity <= 20 + 10 and p_size between 1 and 15 and l_shipmode in ('AIR', 'AIR REG') and l_shipinstruct = 'DELIVER IN PERSON')`
	node, err := Plan(parseOne(t, q19), c)
	if err != nil {
		t.Fatal(err)
	}
	s := planTreeString(node)
	t.Logf("plan:\n%s", s)
	if strings.Contains(s, "Cross") || strings.Contains(s, "CROSS") {
		t.Errorf("live Q19 still has CROSS:\n%s", s)
	}
	// containsCrossJoin walks the tree by Type/Algo; catches the
	// case where planTreeString's stringification doesn't print the
	// literal word "Cross".
	if containsCrossJoin(node) {
		t.Errorf("live Q19 still has Type=Cross:\n%s", s)
	}
	if !containsHashJoin(node) {
		t.Errorf("live Q19 missing Hash Join:\n%s", s)
	}
}

// TestInExprLiteralListPushdown pins the M0061-0002 fix:
// `walkColumnRefs` must not treat `col IN (literal, literal, ...)`
// as out-of-scope. Pre-fix, the predicate-classifier flagged the
// whole conjunct as `sideOutOfScope`, blocking pushdown into the
// CROSS join and leaving Q19 / Q22 stuck on Cartesian.
func TestInExprLiteralListPushdown(t *testing.T) {
	// M0127-P5.9: a legacy-rule assertion; see useLegacyEnumerator.
	useLegacyEnumerator(t)
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "tl"}, []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int8"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "tr"}, []catalog.Column{
		{Name: "x", Type: catalog.Type{Name: "int8"}},
		{Name: "y", Type: catalog.Type{Name: "varchar"}},
	}); err != nil {
		t.Fatal(err)
	}
	// `b IN (1,2,3)` is a literal-list IN. Without the fix, the
	// classifier treats this conjunct as out-of-scope and refuses
	// to push it past the join, leaving the join CROSS.
	sql := "SELECT a FROM tl, tr WHERE a = x AND b IN (1, 2, 3) AND y = 'foo'"
	node, err := Plan(parseOne(t, sql), c)
	if err != nil {
		t.Fatal(err)
	}
	if containsCrossJoin(node) {
		t.Errorf("CROSS join survives despite literal-list IN; pushdown should succeed:\n%s", planTreeString(node))
	}
	if !containsHashJoin(node) {
		t.Errorf("expected Hash Join keyed on a=x:\n%s", planTreeString(node))
	}
}
