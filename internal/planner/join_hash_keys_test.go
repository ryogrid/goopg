package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// hashKeysFixture builds two tables that can be joined on two columns
// with a third, non-equijoin conjunct available for the residual tests.
// hashKeysFixture is two int4 relations joinable on two columns.
//
// The row counts are not decoration. Since M0127-P5.9-r the PG-shaped search
// reaches a FROM clause written `JOIN … ON` as well as a comma one, and it picks
// the join ALGORITHM from cost rather than from the predicate's shape. A
// relation whose size the planner cannot see is estimated at the one-row floor,
// and at one row per side a nested loop is the cheaper plan — so a fixture with
// no statistics would silently stop producing the hash join every test in this
// file is about. Sizing the fixture keeps the subject under test (which pairs a
// hash join PUBLISHES) separate from a cardinality question it does not ask.
func hashKeysFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	mk := func(name string, cols []catalog.Column) {
		tb, err := c.CreateTable(parser.ObjectName{Name: name}, cols)
		if err != nil {
			t.Fatal(err)
		}
		tb.Stats = &catalog.TableStats{RowCount: 100_000}
		tb.Stats.Columns = make([]catalog.ColumnStats, len(cols))
		for i := range cols {
			tb.Stats.Columns[i] = catalog.ColumnStats{NDistinct: 100_000}
		}
	}
	mk("l", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "v", Type: catalog.Type{Name: "int4"}},
	})
	mk("r", []catalog.Column{
		{Name: "a", Type: catalog.Type{Name: "int4"}},
		{Name: "b", Type: catalog.Type{Name: "int4"}},
		{Name: "w", Type: catalog.Type{Name: "int4"}},
	})
	return c
}

// firstHashJoin returns the topmost hash/merge join of a plan.
func firstHashJoin(t *testing.T, n Node) *Join {
	t.Helper()
	var found *Join
	visit(n, func(x Node) bool {
		if found != nil {
			return false
		}
		if j, ok := x.(*Join); ok && (j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge) {
			found = j
			return false
		}
		return true
	})
	if found == nil {
		t.Fatalf("no hash/merge join in plan:\n%s", planTreeString(n))
	}
	return found
}

// TestJoinHashKeysCollectsEveryEquiPair is the core P2.1 claim: a
// two-column equi-join publishes BOTH pairs, not just the one the
// executor hashes on. Before P2.1 the plan carried a single
// (LeftKey, RightKey) and the second equality survived only as a
// per-match residual re-check — which is what made a constant-pinned
// key column collapse the whole build side into one bucket
// (M0125-0035b / TPC-DS Q78).
func TestJoinHashKeysCollectsEveryEquiPair(t *testing.T) {
	c := hashKeysFixture(t)
	q := `select l.v from l join r on l.a = r.a and l.b = r.b`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	j := firstHashJoin(t, node)
	if len(j.HashKeys) != 2 {
		t.Fatalf("HashKeys = %d pairs, want 2; plan:\n%s", len(j.HashKeys), planTreeString(node))
	}
	// HashKeys[0] must be the executor's current pair BY POINTER:
	// isCanonicalKeyEquality identifies the canonical conjunct by
	// pointer identity, and reselectDegenerateHashKeys may have moved
	// the pair off the first ON-clause conjunct on purpose.
	if j.HashKeys[0].Left != j.LeftKey || j.HashKeys[0].Right != j.RightKey {
		t.Errorf("HashKeys[0] is not (LeftKey, RightKey) by pointer")
	}
	leftWidth := len(j.Left.Output())
	seen := map[[2]int]bool{}
	for i, k := range j.HashKeys {
		lc, ok1 := k.Left.(*ColumnRef)
		rc, ok2 := k.Right.(*ColumnRef)
		if !ok1 || !ok2 {
			t.Fatalf("HashKeys[%d] is not a ColumnRef pair: %T = %T", i, k.Left, k.Right)
		}
		if lc.Index >= leftWidth {
			t.Errorf("HashKeys[%d].Left index %d is not on the left input (width %d)", i, lc.Index, leftWidth)
		}
		if rc.Index < leftWidth {
			t.Errorf("HashKeys[%d].Right index %d is not on the right input (width %d)", i, rc.Index, leftWidth)
		}
		seen[[2]int{lc.Index, rc.Index}] = true
	}
	if len(seen) != 2 {
		t.Errorf("the two pairs are not distinct: %v", seen)
	}
	// The extra pairs must not ALIAS the predicate's own ColumnRef
	// nodes (M0097-0060: a later rebind of a predicate index would
	// otherwise silently move a key).
	for _, c := range splitAnd(j.Predicate) {
		bin, ok := c.(*BinaryOp)
		if !ok {
			continue
		}
		for i, k := range j.HashKeys[1:] {
			if k.Left == bin.Left || k.Left == bin.Right || k.Right == bin.Left || k.Right == bin.Right {
				t.Errorf("HashKeys[%d] aliases a Predicate node instead of a clone", i+1)
			}
		}
	}
}

// TestJoinHashKeysOrientation pins the flip: `r.a = l.a` written
// right-side-first must still publish (left expr, right expr), because
// every consumer of the list — EXPLAIN today, the executor's key encode
// at P2.2 — is one-directional.
func TestJoinHashKeysOrientation(t *testing.T) {
	c := hashKeysFixture(t)
	q := `select l.v from l join r on r.a = l.a and r.b = l.b`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	j := firstHashJoin(t, node)
	leftWidth := len(j.Left.Output())
	if len(j.HashKeys) != 2 {
		t.Fatalf("HashKeys = %d pairs, want 2; plan:\n%s", len(j.HashKeys), planTreeString(node))
	}
	for i, k := range j.HashKeys {
		if exprSide(k.Left, leftWidth) != sideLeft || exprSide(k.Right, leftWidth) != sideRight {
			t.Errorf("HashKeys[%d] is not oriented (left, right)", i)
		}
	}
}

// TestJoinResidualKeepsNonEquijoinOnly is the other half of P2.1's
// contract: what an executor keying on the FULL list still has to
// evaluate per match. Both equalities must drop out; the range
// conjunct must stay.
func TestJoinResidualKeepsNonEquijoinOnly(t *testing.T) {
	c := hashKeysFixture(t)
	q := `select l.v from l join r on l.a = r.a and l.b = r.b and l.v < r.w`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	j := firstHashJoin(t, node)
	if len(j.HashKeys) != 2 {
		t.Fatalf("HashKeys = %d pairs, want 2; plan:\n%s", len(j.HashKeys), planTreeString(node))
	}
	// Predicate itself is deliberately NOT mutated — ~15 planner
	// consumers read it as the join's complete condition.
	if got := len(splitAnd(j.Predicate)); got != 3 {
		t.Errorf("Predicate lost conjuncts: %d, want 3 (P2.1 must not mutate it)", got)
	}
	res := j.Residual()
	if res == nil {
		t.Fatalf("Residual() dropped the non-equijoin conjunct entirely")
	}
	conj := splitAnd(res)
	if len(conj) != 1 {
		t.Fatalf("Residual() = %d conjuncts, want 1 (`l.v < r.w`)", len(conj))
	}
	bin, ok := conj[0].(*BinaryOp)
	if !ok || bin.Op != parser.OpLt {
		t.Errorf("Residual() kept %v, want the `<` conjunct", conj[0])
	}
}

// TestJoinResidualNilWhenAllEquijoin — the common case P2.2 exists to
// exploit: an all-equijoin ON clause leaves NO residual work at all,
// where today every extra conjunct costs an interpreted eval per
// candidate match.
func TestJoinResidualNilWhenAllEquijoin(t *testing.T) {
	c := hashKeysFixture(t)
	q := `select l.v from l join r on l.a = r.a and l.b = r.b`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	j := firstHashJoin(t, node)
	if res := j.Residual(); res != nil {
		t.Errorf("Residual() = %v, want nil for an all-equijoin ON clause", res)
	}
}

// TestJoinHashKeysSingleKey guards the degenerate-but-dominant case:
// one equality → exactly one pair, still pinned to (LeftKey, RightKey).
func TestJoinHashKeysSingleKey(t *testing.T) {
	c := hashKeysFixture(t)
	node, err := Plan(parseOne(t, `select l.v from l join r on l.a = r.a`), c)
	if err != nil {
		t.Fatal(err)
	}
	j := firstHashJoin(t, node)
	if len(j.HashKeys) != 1 {
		t.Fatalf("HashKeys = %d pairs, want 1", len(j.HashKeys))
	}
	if j.HashKeys[0].Left != j.LeftKey || j.HashKeys[0].Right != j.RightKey {
		t.Errorf("HashKeys[0] is not (LeftKey, RightKey) by pointer")
	}
	if res := j.Residual(); res != nil {
		t.Errorf("Residual() = %v, want nil", res)
	}
}

// TestJoinHashKeysEmptyForNonEquiJoin — a join with no usable equality
// stays on nested loop and publishes no list, so every consumer falls
// back to today's behaviour.
func TestJoinHashKeysEmptyForNonEquiJoin(t *testing.T) {
	c := hashKeysFixture(t)
	node, err := Plan(parseOne(t, `select l.v from l join r on l.a < r.a`), c)
	if err != nil {
		t.Fatal(err)
	}
	var checked bool
	visit(node, func(x Node) bool {
		j, ok := x.(*Join)
		if !ok {
			return true
		}
		checked = true
		if j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge {
			t.Fatalf("a `<`-only join picked %v", j.Algo)
		}
		if j.HashKeys != nil {
			t.Errorf("HashKeys populated on a non-equi join: %v", j.HashKeys)
		}
		if res := j.Residual(); res != j.Predicate {
			t.Errorf("Residual() must be the whole Predicate when HashKeys is empty")
		}
		return true
	})
	if !checked {
		t.Fatal("no join in the plan")
	}
}

// TestJoinHashKeysInsideSublink covers the reach the plan-node walk
// alone does not have: a sublink body is a plan hanging off an
// EXPRESSION, so a join inside it is invisible to planChildren. EXPLAIN
// renders those subplans, and P2.2's executor runs them, so an unfilled
// list there would be a silent hole.
func TestJoinHashKeysInsideSublink(t *testing.T) {
	c := hashKeysFixture(t)
	q := `select l.v from l where exists (
	        select 1 from l l2 join r on l2.a = r.a and l2.b = r.b where l2.v = l.v)`
	node, err := Plan(parseOne(t, q), c)
	if err != nil {
		t.Fatal(err)
	}
	var inner []*Join
	walkPlanExprsDeep(node, 0, func(e Expr, _ int) {
		var p Node
		switch x := e.(type) {
		case *SubqueryExpr:
			p = x.Plan
		case *ExistsExpr:
			p = x.Plan
		case *InExpr:
			p = x.Plan
		}
		if p == nil {
			return
		}
		visit(p, func(n Node) bool {
			if j, ok := n.(*Join); ok && (j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge) {
				inner = append(inner, j)
			}
			return true
		})
	})
	// The EXISTS may be unnested into a semi-join instead of staying a
	// sublink; either way every hash join reachable in the finished
	// plan must carry its list. Collect the top-level ones too.
	visit(node, func(n Node) bool {
		if j, ok := n.(*Join); ok && (j.Algo == JoinAlgoHash || j.Algo == JoinAlgoMerge) {
			inner = append(inner, j)
		}
		return true
	})
	if len(inner) == 0 {
		t.Fatalf("no hash join anywhere in:\n%s", planTreeString(node))
	}
	multi := 0
	for _, j := range inner {
		if len(j.HashKeys) == 0 {
			t.Errorf("hash join with an unfilled HashKeys list:\n%s", planTreeString(node))
		}
		if len(j.HashKeys) > 1 {
			multi++
		}
	}
	if multi == 0 {
		t.Errorf("the two-column inner join never published both pairs; plan:\n%s", planTreeString(node))
	}
}

// TestSplitEqualityForHashMatchesFirstPair is the anti-drift guard for
// the refactor: splitEqualityForHash (the single-pair view every
// existing construction site uses) must stay exactly the first element
// of splitAllEqualitiesForHash (the full-list view). They now share
// forEachEqualityForHash, and this test is what makes a future
// divergence fail loudly rather than produce a key list whose head is
// not the key the executor hashes on.
func TestSplitEqualityForHashMatchesFirstPair(t *testing.T) {
	mkCol := func(idx int) Expr { return &ColumnRef{Index: idx, Name: "c"} }
	eq := func(a, b Expr) Expr { return &BinaryOp{Op: parser.OpEq, Left: a, Right: b} }
	lt := func(a, b Expr) Expr { return &BinaryOp{Op: parser.OpLt, Left: a, Right: b} }
	and := func(es ...Expr) Expr { return combineAnd(es) }

	cases := []struct {
		name      string
		pred      Expr
		wantPairs int
	}{
		{"single", eq(mkCol(0), mkCol(3)), 1},
		{"two", and(eq(mkCol(0), mkCol(3)), eq(mkCol(1), mkCol(4))), 2},
		{"flipped-first", and(eq(mkCol(3), mkCol(0)), eq(mkCol(1), mkCol(4))), 2},
		{"residual-between", and(eq(mkCol(0), mkCol(3)), lt(mkCol(2), mkCol(5)), eq(mkCol(1), mkCol(4))), 2},
		{"none", lt(mkCol(0), mkCol(3)), 0},
		{"same-side", eq(mkCol(0), mkCol(1)), 0},
	}
	const leftWidth = 3
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			all := splitAllEqualitiesForHash(tc.pred, leftWidth)
			if len(all) != tc.wantPairs {
				t.Fatalf("splitAllEqualitiesForHash = %d pairs, want %d", len(all), tc.wantPairs)
			}
			l, r, ok := splitEqualityForHash(tc.pred, leftWidth)
			if ok != (tc.wantPairs > 0) {
				t.Fatalf("splitEqualityForHash ok=%v, want %v", ok, tc.wantPairs > 0)
			}
			if !ok {
				return
			}
			if l != all[0].Left || r != all[0].Right {
				t.Errorf("splitEqualityForHash is not splitAllEqualitiesForHash[0]")
			}
		})
	}
}
