package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestAnySublinkSubqueryScanIsStripped pins M0146-0201's second half. A
// pulled-up ANY sublink whose body is not simple becomes a subquery RTE
// named ANY_subquery (convert_ANY_sublink_to_join); its one output is read
// only by the link equality, so PG's setrefs removes the SubqueryScan
// (trivial_subqueryscan) and TPC-H Q18 prints the HashAggregate directly
// under the Hash. goopg counted the wrapper's consumption by
// SourceTableIdx, but the link's reference carries the body's numbering,
// which collided with the outer query's first FROM item (`t1.a`, `t1.b`)
// and kept the label.
func TestAnySublinkSubqueryScanIsStripped(t *testing.T) {
	cat := subqueryScanFixture(t)
	plan := planSQL(t, cat,
		"SELECT t1.b FROM t1 WHERE t1.a IN (SELECT t2.a FROM t2 GROUP BY t2.a HAVING count(*) > 1)")
	if sq := findSubqueryScan(plan); sq != nil {
		t.Fatalf("Subquery Scan on %q survived; PG's setrefs removes the ANY_subquery wrapper", sq.Alias)
	}
	if !subtreeHolds(plan, func(n Node) bool { _, ok := n.(*Aggregate); return ok }) {
		t.Fatal("the grouped body was lost with its wrapper")
	}
}

// TestSubqueryScanIsParallelSafeWhenItsBodyIs pins M0146-0201's first half:
// the `Subquery Scan` label has no expression of its own, so the safety walk
// descends it (PG's SubqueryScanPath is parallel_safe when its subpath is).
// Refusing it as an unmodelled node marked TPC-H Q18's ANY_subquery rel
// parallel-unsafe and left the whole join tree without partial paths. A
// Limit inside still refuses (limit_needed).
func TestSubqueryScanIsParallelSafeWhenItsBodyIs(t *testing.T) {
	cat := subqueryScanFixture(t)
	t2, ok := cat.LookupTable(parser.ObjectName{Name: "t2"})
	if !ok {
		t.Fatal("fixture: no t2")
	}
	safe := &SubqueryScan{Alias: "ANY_subquery", Child: &Aggregate{Child: &SeqScan{Table: t2}}}
	if !subtreeHasNoParallelHazard(safe, cat) {
		t.Fatal("a Subquery Scan over a parallel-safe body must be parallel-safe")
	}
	limited := &SubqueryScan{Alias: "s", Child: &Limit{Child: &SeqScan{Table: t2}}}
	if subtreeHasNoParallelHazard(limited, cat) {
		t.Fatal("a Limit under the Subquery Scan must still refuse (limit_needed)")
	}
}
