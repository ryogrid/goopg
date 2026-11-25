package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// TestPartSchemeMatching pins the partitionwise-join preconditions of
// M0148-0001c3a9 slice 2 against PG's: find_partition_scheme identity
// (strategy, key count, key type), partition_bounds_equal (partition by
// partition, PartitionDesc order) and have_partkey_equi_join (an equi-join
// clause per key position).
func TestPartSchemeMatching(t *testing.T) {
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "b", Type: catalog.Type{Name: "int4"}}, {Name: "c", Type: catalog.Type{Name: "text"}}}
	parent := func(oid uint32, method, key string) *catalog.Table {
		return &catalog.Table{OID: oid, PartitionKey: []string{key}, PartitionMethod: method, Columns: cols}
	}
	rng := func(p *catalog.Table, name, from, to string) *catalog.Table {
		return &catalog.Table{Name: name, PartitionParentOID: p.OID, PartitionBounds: []catalog.PartitionBound{{ChildName: name,
			FromValues: []string{from}, ToValues: []string{to}}}}
	}
	list := func(p *catalog.Table, name string, vals ...string) *catalog.Table {
		lits := make([]string, len(vals))
		for i, v := range vals {
			lits[i] = "'" + v + "'"
			if v == "NULL" {
				lits[i] = "NULL"
			}
		}
		return &catalog.Table{Name: name, PartitionParentOID: p.OID, PartitionBounds: []catalog.PartitionBound{{ChildName: name, InValues: vals, InValueLiterals: lits}}}
	}
	p1, p2, p3 := parent(1, "RANGE", "a"), parent(2, "RANGE", "b"), parent(3, "RANGE", "a")
	s1 := newPartScheme(p1, []*catalog.Table{rng(p1, "p1_1", "0", "250"), rng(p1, "p1_2", "250", "500")})
	s2 := newPartScheme(p2, []*catalog.Table{rng(p2, "p2_1", "0", "250"), rng(p2, "p2_2", "250", "500")})
	s3 := newPartScheme(p3, []*catalog.Table{rng(p3, "p3_1", "0", "250"), rng(p3, "p3_2", "250", "600")})
	if s1 == nil || s2 == nil || s3 == nil {
		t.Fatalf("newPartScheme declined a one-level column-keyed table")
	}
	if s2.keyPos[0] != 1 {
		t.Errorf("key b at position %d, want 1", s2.keyPos[0])
	}
	if !partitionBoundsEqual(s1, s2) {
		t.Errorf("equal range bounds on int keys did not match")
	}
	if partitionBoundsEqual(s1, s3) {
		t.Errorf("a different upper bound matched")
	}
	pc := parent(4, "RANGE", "c")
	sc := newPartScheme(pc, []*catalog.Table{rng(pc, "pc_1", "0", "250"), rng(pc, "pc_2", "250", "500")})
	if samePartitionScheme(s1, sc) {
		t.Errorf("int and text keys share a scheme")
	}
	pl1, pl2 := parent(5, "LIST", "c"), parent(6, "LIST", "c")
	l1 := newPartScheme(pl1, []*catalog.Table{list(pl1, "l1a", "x", "y"), list(pl1, "l1b", "NULL", "z")})
	l2 := newPartScheme(pl2, []*catalog.Table{list(pl2, "l2a", "y", "x"), list(pl2, "l2b", "z", "NULL")})
	l3 := newPartScheme(pl2, []*catalog.Table{list(pl2, "l3a", "y", "x"), list(pl2, "l3b", "z")})
	if !partitionBoundsEqual(l1, l2) {
		t.Errorf("equal list value sets in another order did not match")
	}
	if partitionBoundsEqual(l1, l3) {
		t.Errorf("a list partition without NULL matched one with NULL")
	}
	// Scope: an expression key or a sub-partitioned member carries no scheme.
	pe := parent(7, "RANGE", "a")
	pe.PartitionKeyExprs = []parser.Expr{&parser.RowExpr{}}
	if newPartScheme(pe, []*catalog.Table{rng(pe, "e1", "0", "1"), rng(pe, "e2", "1", "2")}) != nil {
		t.Errorf("expression-keyed table got a scheme")
	}
	if newPartScheme(p1, []*catalog.Table{rng(p1, "x1", "0", "1"), {Name: "deep", PartitionParentOID: 99}}) != nil {
		t.Errorf("a grandchild leaf got a scheme")
	}

	// have_partkey_equi_join: t1.a = t2.b keys the two schemes; t1.b = t2.b
	// (a non-key column on one side) does not.
	ref := func(src int16, name string) *ColumnRef {
		return &ColumnRef{Name: name, SourceTableIdx: src, Type: catalog.Type{Name: "int4"}}
	}
	eq := func(l, r *ColumnRef) *restrictInfo { return &restrictInfo{isEquijoin: true, leftKey: l, rightKey: r} }
	if !partkeyEquiJoined(s1, s2, 0, 1, []*restrictInfo{eq(ref(1, "b"), ref(0, "a"))}) {
		t.Errorf("t2.b = t1.a not recognised as a partition-key equi-join")
	}
	if partkeyEquiJoined(s1, s2, 0, 1, []*restrictInfo{eq(ref(0, "b"), ref(1, "b"))}) {
		t.Errorf("t1.b = t2.b accepted although t1.b is not t1's key")
	}
	if partkeyEquiJoined(s1, s2, 0, 1, []*restrictInfo{{leftKey: ref(0, "a"), rightKey: ref(1, "b")}}) {
		t.Errorf("a non-equijoin clause accepted")
	}
}

// TestPartSchemeStampedOnAppendrel checks the planner stamps the scheme on a
// one-level partitioned table's appendrel, with the partitions in
// PartitionDesc order (created out of order here).
func TestPartSchemeStampedOnAppendrel(t *testing.T) {
	cat := catalog.NewInMemory()
	cols := []catalog.Column{{Name: "a", Type: catalog.Type{Name: "int4"}}, {Name: "b", Type: catalog.Type{Name: "int4"}}}
	p, err := cat.CreateTable(parser.ObjectName{Name: "prt"}, cols)
	if err != nil {
		t.Fatal(err)
	}
	p.PartitionKey, p.PartitionMethod = []string{"b"}, "RANGE"
	for _, c := range []struct{ name, from, to string }{{"prt_p2", "250", "500"}, {"prt_p1", "0", "250"}} {
		ct, err := cat.CreateTable(parser.ObjectName{Name: c.name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		ct.PartitionParentOID = p.OID
		ct.PartitionBounds = []catalog.PartitionBound{{ChildName: c.name, FromValues: []string{c.from}, ToValues: []string{c.to}}}
		cat.RegisterPartitionChild(p.OID, ct.OID)
	}
	stmts, err := parser.Parse("SELECT * FROM prt")
	if err != nil {
		t.Fatal(err)
	}
	plan, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	var found *partScheme
	var walk func(n Node)
	walk = func(n Node) {
		if n == nil || found != nil {
			return
		}
		if so, ok := n.(*SetOp); ok && so.partScheme != nil {
			found = so.partScheme
			return
		}
		for _, c := range lowerNodeChildren(n) {
			walk(c)
		}
	}
	walk(plan)
	if found == nil {
		t.Fatalf("no partition scheme on the appendrel (root %T)", plan)
	}
	if len(found.parts) != 2 || found.parts[0].Name != "prt_p1" || found.parts[1].Name != "prt_p2" || found.keyPos[0] != 1 {
		t.Errorf("scheme parts %v keyPos %v, want [prt_p1 prt_p2] [1]", []string{found.parts[0].Name, found.parts[1].Name}, found.keyPos)
	}
}
