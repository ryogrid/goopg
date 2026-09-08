package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// A-01(ii) cut 2: the statement scope created in Plan()/PlanSchemaOnly
// is threaded through every re-entrant planning path (derived tables,
// scalar/ARRAY/IN/EXISTS sublinks, CTE bodies incl. DML, set-op
// branches, DML SELECT/FROM/USING sources, UPDATE multi-assign), so
// every §4 scan node carries a nonzero, statement-unique RTID.

// cut2Fixture builds the two-table catalog the cut-2 shapes run against.
func cut2Fixture(t *testing.T) *catalog.InMemory {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "ctr"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "numeric"}},
		{Name: "st", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "eq_r"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "numeric"}},
		{Name: "st", Type: catalog.Type{Name: "text"}},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// cut2Walk is the test-local full-tree walker: Node children for the
// kinds in these shapes plus Expr-hanging subplan bodies (the F3
// enumeration under test). The shared visit() helper only descends
// into Join/Filter/Project/Aggregate/Sort/Limit, which would hide
// exactly the nested scans these tests pin.
//
// Shared subtrees (a CTE body behind N consumers, one multi-assign
// row behind N elems) are visited once: they are one node with one
// RTID, and re-walking them would false-alarm the uniqueness check.
func cut2Walk(n Node, fn func(Node)) {
	seen := map[Node]struct{}{}
	var walk func(Node)
	walk = func(n Node) {
		if n == nil {
			return
		}
		if _, ok := seen[n]; ok {
			return
		}
		seen[n] = struct{}{}
		fn(n)
		switch x := n.(type) {
		case *Project:
			walk(x.Child)
		case *Result:
			walk(x.Child)
		case *Filter:
			walk(x.Child)
		case *Sort:
			walk(x.Child)
		case *Limit:
			walk(x.Child)
		case *Distinct:
			walk(x.Child)
		case *DistinctOn:
			walk(x.Child)
		case *Join:
			walk(x.Left)
			walk(x.Right)
		case *NestedLoopIndexJoin:
			walk(x.Outer)
			if x.InnerMemo != nil {
				walk(x.InnerMemo)
			} else {
				walk(x.Inner)
			}
		case *Aggregate:
			walk(x.Child)
		case *WindowAgg:
			walk(x.Child)
		case *SetOp:
			walk(x.Left)
			walk(x.Right)
		case *Insert:
			walk(x.Source)
		case *Update:
			walk(x.Child)
			for _, s := range x.FromScans {
				walk(s)
			}
		case *Delete:
			walk(x.Child)
			for _, s := range x.UsingScans {
				walk(s)
			}
		case *Merge:
			walk(x.Source)
		case *CTEScan:
			walk(x.Child)
		case *CTEDMLPrefix:
			for _, d := range x.DMls {
				walk(d)
			}
			walk(x.Body)
		case *RecursiveUnion:
			walk(x.Anchor)
			walk(x.Recursive)
		case *LockRows:
			walk(x.Child)
		case *Gather:
			walk(x.Child)
		case *ProjectSet:
			walk(x.Child)
		}
		for _, sp := range NodeSubplans(n) {
			walk(sp)
		}
	}

	walk(n)
}

// cut2ScanIDs collects (alias, RTID) for every §4-stamped scan kind in
// the plan. IndexScan/IndexOnlyScan/BitmapHeapScan carry the field but
// stay 0 until a later cut stamps them, so only SeqScan/CTEScan/
// MaterializedCTEScan are asserted here; the InMemory catalog builds
// no indexes, so every base read below is a SeqScan.
func cut2ScanIDs(node Node) (seqs [][2]any, ctes []int32) {
	cut2Walk(node, func(n Node) {
		switch s := n.(type) {
		case *SeqScan:
			seqs = append(seqs, [2]any{s.Alias, s.RTID})
		case *CTEScan:
			ctes = append(ctes, s.RTID)
		case *MaterializedCTEScan:
			ctes = append(ctes, s.RTID)
		}
	})
	return seqs, ctes
}

func cut2AssertAllNonzeroDistinct(t *testing.T, node Node) {
	t.Helper()
	seqs, ctes := cut2ScanIDs(node)
	seen := map[int32]string{}
	for _, s := range seqs {
		rtid := s[1].(int32)
		if rtid == 0 {
			t.Errorf("SeqScan (alias %q) RTID is 0; cut 2 threads every re-entrant path", s[0])
			continue
		}
		if prev, ok := seen[rtid]; ok {
			t.Errorf("RTID %d shared by %q and %q; must be statement-unique", rtid, prev, s[0])
		}
		seen[rtid] = "seq:" + s[0].(string)
	}
	for i, rtid := range ctes {
		if rtid == 0 {
			t.Errorf("CTE consumer[%d] RTID is 0; cut 2 threads CTE references", i)
			continue
		}
		if prev, ok := seen[rtid]; ok {
			t.Errorf("RTID %d shared by %q and cte-consumer[%d]; must be statement-unique", rtid, prev, i)
		}
		seen[rtid] = "cte"
	}
}

// TestRTIDDistinctAcrossCorrelatedSublink is the TPC-DS Q30 shape at
// planner level: a correlated scalar sublink over the same-named
// relation as the outer query. The outer binding and the inner scan
// must get distinct statement-unique ids — under the per-level
// SourceTableIdx counter they shared one, which is the collision the
// M0125-0039 cols guard degrades on.
//
// The count(*) aggregate keeps the shape a SubPlan (canUnnestSubquery
// rejects the star spelling), so the inner scan is genuinely nested.
func TestRTIDDistinctAcrossCorrelatedSublink(t *testing.T) {
	c := cut2Fixture(t)
	node, err := Plan(parseOne(t, `SELECT c1.id FROM ctr c1 WHERE c1.id > (SELECT count(*) FROM ctr c2 WHERE c2.st = c1.st)`), c)
	if err != nil {
		t.Fatal(err)
	}
	seqs, _ := cut2ScanIDs(node)
	byAlias := map[string]int32{}
	for _, s := range seqs {
		byAlias[s[0].(string)] = s[1].(int32)
	}
	outer, ok := byAlias["c1"]
	if !ok {
		t.Fatalf("no outer SeqScan (alias c1) in plan; scans: %v", seqs)
	}
	inner, ok := byAlias["c2"]
	if !ok {
		t.Fatalf("no inner SeqScan (alias c2) in plan; scans: %v", seqs)
	}
	if outer == 0 || inner == 0 {
		t.Errorf("outer/inner RTIDs are %d/%d; both must be nonzero", outer, inner)
	}
	if outer == inner {
		t.Errorf("outer binding and inner same-name relation share RTID %d; must differ", outer)
	}
	cut2AssertAllNonzeroDistinct(t, node)
}

// TestRTIDDerivedSelfJoinCollisionNowDistinct is the M0125-0039
// cols-guard shape at planner level: an outer base scan over the same
// table a derived table self-joins below it. The guard refused to
// qualify because the flattened subquery's binding could share its id
// with a base relation inside it; with statement-unique RTIDs every
// scan here is distinct, which is what lets that case qualify instead
// of degrading.
func TestRTIDDerivedSelfJoinCollisionNowDistinct(t *testing.T) {
	c := cut2Fixture(t)
	node, err := Plan(parseOne(t, `SELECT o.st, t.s1 FROM eq_r o, (SELECT a.st AS s1 FROM eq_r a, eq_r b WHERE a.id = b.id) t WHERE t.s1 <> o.st`), c)
	if err != nil {
		t.Fatal(err)
	}
	seqs, _ := cut2ScanIDs(node)
	if len(seqs) != 3 {
		t.Fatalf("expected 3 SeqScans (o, a, b), got %v", seqs)
	}
	cut2AssertAllNonzeroDistinct(t, node)
}

// TestRTIDThreadedAcrossStatementShapes sweeps the remaining cut-2
// threading paths: every §4 scan in each shape must carry a nonzero,
// pairwise-distinct RTID.
func TestRTIDThreadedAcrossStatementShapes(t *testing.T) {
	c := cut2Fixture(t)
	shapes := map[string]string{
		"cte-body":           `WITH c AS (SELECT id FROM eq_r) SELECT * FROM c c1, c c2 WHERE c1.id = c2.id`,
		"dml-cte":            `WITH x AS (INSERT INTO eq_r VALUES (1, 'a') RETURNING *) SELECT * FROM x`,
		"union-branches":     `SELECT id FROM eq_r UNION ALL SELECT id FROM eq_r`,
		"intersect-branch":   `SELECT id FROM eq_r INTERSECT SELECT id FROM eq_r`,
		"insert-select":      `INSERT INTO eq_r SELECT * FROM eq_r`,
		"update-from":        `UPDATE eq_r AS o SET st = f.st FROM eq_r AS f WHERE o.id = f.id`,
		"delete-using":       `DELETE FROM eq_r AS o USING eq_r AS f WHERE o.id = f.id`,
		"merge-source":       `MERGE INTO eq_r AS o USING eq_r AS f ON o.id = f.id WHEN MATCHED THEN UPDATE SET st = f.st`,
		"exists":             `SELECT id FROM eq_r a WHERE EXISTS (SELECT 1 FROM eq_r b WHERE b.id = a.id)`,
		"in-subquery":        `SELECT id FROM eq_r a WHERE a.id IN (SELECT id FROM eq_r b WHERE b.st <> 'z')`,
		"array-subquery":     `SELECT ARRAY(SELECT id FROM eq_r b) FROM eq_r a WHERE a.id = 1`,
		"update-multiassign": `UPDATE eq_r SET (id, st) = (SELECT f.id, f.st FROM eq_r f WHERE f.id = eq_r.id)`,
	}
	for name, sql := range shapes {
		t.Run(name, func(t *testing.T) {
			node, err := Plan(parseOne(t, sql), c)
			if err != nil {
				t.Fatal(err)
			}
			seqs, ctes := cut2ScanIDs(node)
			if len(seqs)+len(ctes) == 0 {
				t.Fatalf("no §4 scans in plan for %q", sql)
			}
			cut2AssertAllNonzeroDistinct(t, node)
		})
	}
}

// TestRTIDThreadedIntoOnConflict pins the ON CONFLICT arm: DO UPDATE
// SET/WHERE resolve through planOnConflict/applyUpdateAssign, which
// thread the statement scope into the multi-assign subquery planner.
func TestRTIDThreadedIntoOnConflict(t *testing.T) {
	c := catalog.NewInMemory()
	tbl, err := c.CreateTable(parser.ObjectName{Name: "eq_r"}, []catalog.Column{
		{Name: "id", Type: catalog.Type{Name: "numeric"}},
		{Name: "st", Type: catalog.Type{Name: "text"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "eq_r_id_uniq"}, tbl, []string{"id"}, true, "btree", false); err != nil {
		t.Fatal(err)
	}
	node, err := Plan(parseOne(t, `INSERT INTO eq_r VALUES (1, 'a') ON CONFLICT (id) DO UPDATE SET st = 'b' WHERE eq_r.id > (SELECT count(*) FROM eq_r)`), c)
	if err != nil {
		t.Fatal(err)
	}
	seqs, _ := cut2ScanIDs(node)
	if len(seqs) == 0 {
		t.Fatalf("no SeqScans in ON CONFLICT plan")
	}
	cut2AssertAllNonzeroDistinct(t, node)
}

// TestRTIDDeterministicAcrossPlans pins the §3.2 cache-safety claim:
// allocation order is first-encounter order during planning, a pure
// function of (statement, catalog), so planning the same statement
// twice yields the same RTID sequence.
func TestRTIDDeterministicAcrossPlans(t *testing.T) {
	c := cut2Fixture(t)
	sql := `SELECT c1.id FROM ctr c1 WHERE c1.id > (SELECT count(*) FROM ctr c2 WHERE c2.st = c1.st)`
	ids := func() []int32 {
		node, err := Plan(parseOne(t, sql), c)
		if err != nil {
			t.Fatal(err)
		}
		var out []int32
		cut2Walk(node, func(n Node) {
			if s, ok := n.(*SeqScan); ok {
				out = append(out, s.RTID)
			}
		})
		return out
	}
	a, b := ids(), ids()
	if len(a) != len(b) {
		t.Fatalf("plan shapes differ across identical plans: %v vs %v", a, b)
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("RTIDs not reproducible: %v vs %v", a, b)
		}
	}
}
