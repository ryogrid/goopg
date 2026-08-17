package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestSchemaColumnSourceTableIdxFromTableScan pins M0071-0009's
// per-binding SourceTableIdx assignment. A SeqScan's output schema
// must stamp every column with the binder's monotonic source idx
// (>= 1; 0 is the unknown / derived sentinel).
func TestSchemaColumnSourceTableIdxFromTableScan(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, `SELECT l_orderkey, l_suppkey FROM lineitem`), c)
	if err != nil {
		t.Fatal(err)
	}
	// Walk to the SeqScan and check schema identity.
	var scan *SeqScan
	visit(node, func(n Node) bool {
		if s, ok := n.(*SeqScan); ok {
			scan = s
			return false
		}
		return true
	})
	if scan == nil {
		t.Fatalf("no SeqScan in plan")
	}
	for i, c := range scan.Output() {
		if c.SourceTableIdx == 0 {
			t.Errorf("SeqScan column[%d] (%s) SourceTableIdx is 0 (unknown); expected >= 1", i, c.Name)
		}
	}
}

// TestColumnRefSourceTableIdxDistinctForSelfJoin pins that the
// binder gives different SourceTableIdx values to columns from
// different range bindings, even when those bindings refer to the
// same catalog table (Q21's lineitem l1/l2/l3 self-join shape).
//
// Without this, predRebind / resolveOuterIdx fall back to
// Name-only resolution and lose disambiguation power for
// self-joins.
func TestColumnRefSourceTableIdxDistinctForSelfJoin(t *testing.T) {
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_orderkey", Type: catalog.Type{Name: "numeric"}},
		{Name: "l_suppkey", Type: catalog.Type{Name: "numeric"}},
	}); err != nil {
		t.Fatal(err)
	}
	// Three lineitem aliases produce three distinct sourceIdx values.
	node, err := Plan(parseOne(t, `SELECT l1.l_suppkey, l2.l_suppkey, l3.l_suppkey
FROM lineitem l1, lineitem l2, lineitem l3
WHERE l1.l_orderkey = l2.l_orderkey AND l2.l_orderkey = l3.l_orderkey`), c)
	if err != nil {
		t.Fatal(err)
	}
	// The top Project's targets are three ColumnRefs to l_suppkey,
	// each from a different binding — they MUST have distinct
	// SourceTableIdx values.
	proj, ok := node.(*Project)
	if !ok {
		t.Fatalf("expected top Project, got %T", node)
	}
	if len(proj.Targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(proj.Targets))
	}
	seen := map[int16]bool{}
	for i, tgt := range proj.Targets {
		cr, ok := tgt.(*ColumnRef)
		if !ok {
			t.Fatalf("target[%d] is %T, want *ColumnRef", i, tgt)
		}
		if cr.SourceTableIdx == 0 {
			t.Errorf("target[%d] (%s) SourceTableIdx=0 (unknown); expected >=1", i, cr.Name)
			continue
		}
		if seen[cr.SourceTableIdx] {
			t.Errorf("target[%d] (%s) SourceTableIdx=%d repeats; self-join siblings must differ", i, cr.Name, cr.SourceTableIdx)
		}
		seen[cr.SourceTableIdx] = true
	}
}

// TestFindColumnIndexByNameAndSourceDisambiguates pins the
// findColumnIndexByNameAndSource helper used by predRebind and
// reresolveJoinByName. A schema with two columns of the same Name
// but different SourceTableIdx values must lookup correctly.
func TestFindColumnIndexByNameAndSourceDisambiguates(t *testing.T) {
	schema := Schema{
		{Name: "l_suppkey", SourceTableIdx: 1},
		{Name: "l_orderkey", SourceTableIdx: 1},
		{Name: "l_suppkey", SourceTableIdx: 2},
		{Name: "l_orderkey", SourceTableIdx: 2},
	}
	if got := findColumnIndexByNameAndSource(schema, "l_suppkey", 1, 0); got != 0 {
		t.Errorf("l_suppkey/src=1 → got %d, want 0", got)
	}
	if got := findColumnIndexByNameAndSource(schema, "l_suppkey", 2, 0); got != 2 {
		t.Errorf("l_suppkey/src=2 → got %d, want 2", got)
	}
	if got := findColumnIndexByNameAndSource(schema, "l_suppkey", 1, 100); got != 100 {
		t.Errorf("l_suppkey/src=1 with offset=100 → got %d, want 100", got)
	}
	// findUniqueColumnIndex (Name-only) returns -1 because
	// l_suppkey is ambiguous; SourceTableIdx-aware lookup wins.
	if got := findUniqueColumnIndex(schema, "l_suppkey", 0); got != -1 {
		t.Errorf("findUniqueColumnIndex on ambiguous Name → got %d, want -1", got)
	}
}
