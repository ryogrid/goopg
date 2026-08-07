package planner

// M0127-P5.5-e-i — the coordinate carrier and the hash-join `createPlan` arm
// (createplanjoin.go).
//
// The arm is LIVE in production since M0127-P5.9 (2026-08-06):
// `GOOPG_PGSHAPED_DP` defaults ON and `planSelect` calls the search, so these
// tests are no longer its only observer — the planner bar (SPOT/DS05) sees it
// too. They remain the sharpest one. What they pin is the one thing
// a join arm can get wrong while still producing a runnable plan: the
// COORDINATES. Every fixture below deliberately puts the outer side SECOND in
// binding order, so a translation that silently did nothing would still build a
// `*Join` — with keys pointing at the wrong columns.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// cpjSchema is an n-column schema whose names carry the binding coordinate, so
// a mistranslation is legible in a failure message rather than a bare index.
func cpjSchema(prefix string, n int) Schema {
	s := make(Schema, n)
	for i := range s {
		s[i] = SchemaColumn{Name: prefix + string(rune('0'+i)), Type: catalog.Type{Name: "int4"}}
	}
	return s
}

// cpjLeafRel builds a level-1 rel exactly as `buildInitialRels` does: a leaf
// node, the binding offset its columns occupied before the search ran, and a
// prebuilt path over it. `width` columns starting at binding index `offset`.
func cpjLeafRel(relid int, offset, width int, prefix string) *RelOptInfo {
	leaf := &SeqScan{
		Table:  &catalog.Table{Name: prefix},
		Alias:  prefix,
		schema: cpjSchema(prefix, width),
	}
	rel := newRelOptInfo(relsetOf(relid), 100, 8)
	rel.baseLeaf = leaf
	rel.baseOffset = offset
	return rel
}

// cpjLeafPath is the rel's own scan path — a `PathSeqScan`, so the arm exercises
// the rebuild rather than the prebuilt passthrough.
func cpjLeafPath(rel *RelOptInfo) *Path {
	return &Path{Kind: PathSeqScan, Rel: rel, Rows: rel.Rows}
}

// cpjTwoRel is the standard fixture: relid 0 occupies binding columns 0-1 and
// relid 1 occupies binding columns 2-4. The returned paths are handed to the arm
// in the order (outer, inner) the caller wants.
func cpjTwoRel() (a, b *RelOptInfo) {
	return cpjLeafRel(0, 0, 2, "a"), cpjLeafRel(1, 2, 3, "b")
}

// cpjHashPath assembles a PathHashJoin over the two child paths, with the given
// keys and residual.
func cpjHashPath(outer, inner *Path, keys, residual []*restrictInfo) *Path {
	joinrel := newRelOptInfo(outer.Rel.Relids|inner.Rel.Relids, 500, 16)
	return &Path{
		Kind:     PathHashJoin,
		Rel:      joinrel,
		Rows:     500,
		Children: []*Path{outer, inner},
		HashKeys: keys,
		Residual: residual,
	}
}

// TestCreateHashJoinPlanTranslatesKeysIntoMergedCoordinates is the central test.
// The search chose relid 1 (binding columns 2-4) as the OUTER side, so the
// emitted row is `b0 b1 b2 a0 a1` and the clause `a.a0 = b.b1` — written in
// binding coordinates as `col(0) = col(3)` — must come back as
// `col(1) = col(3)`: the outer operand is b1, now at merged position 1, and the
// inner operand is a0, now at merged position 3.
//
// Both halves are load-bearing. If the arm did not ORIENT, LeftKey would be the
// `a` operand while `Left` is the `b` node; if it did not RENUMBER, LeftKey
// would still be col(0) — which exists in the merged row, as b0, so the plan
// would run and silently join on the wrong column.
func TestCreateHashJoinPlanTranslatesKeysIntoMergedCoordinates(t *testing.T) {
	a, b := cpjTwoRel()
	// `a.a0 = b.b1` in binding coordinates.
	key := equiClauseOn(a.Relids, b.Relids, 0, 3)
	p := cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{key}, nil)

	n, lay := createPlanNode(p)
	j, ok := n.(*Join)
	if !ok {
		t.Fatalf("createPlan(PathHashJoin) = %T, want *Join", n)
	}
	if got := []int(lay); len(got) != 5 || got[0] != 2 || got[1] != 3 || got[2] != 4 || got[3] != 0 || got[4] != 1 {
		t.Fatalf("layout = %v, want [2 3 4 0 1] (outer b's binding range then inner a's)", got)
	}
	lk, ok := j.LeftKey.(*ColumnRef)
	if !ok {
		t.Fatalf("LeftKey = %T, want *ColumnRef", j.LeftKey)
	}
	rk, ok := j.RightKey.(*ColumnRef)
	if !ok {
		t.Fatalf("RightKey = %T, want *ColumnRef", j.RightKey)
	}
	if lk.Index != 1 {
		t.Errorf("LeftKey.Index = %d, want 1 (binding 3 = b1, at merged position 1)", lk.Index)
	}
	if rk.Index != 3 {
		t.Errorf("RightKey.Index = %d, want 3 (binding 0 = a0, at merged position 3)", rk.Index)
	}
	// The originals must be untouched — the search still holds these clauses
	// and several passes match planner expressions by pointer identity.
	if key.leftKey.(*ColumnRef).Index != 0 || key.rightKey.(*ColumnRef).Index != 3 {
		t.Fatalf("the arm mutated the search's own clause: %v / %v", key.leftKey, key.rightKey)
	}
	if lk == key.rightKey || rk == key.leftKey {
		t.Fatal("the arm reused the clause's operand nodes rather than cloning them")
	}
}

// TestCreateHashJoinPlanMergedSchemaIsOuterThenInner: the emitted schema is the
// concatenation in child order, and the layout describes it position for
// position.
func TestCreateHashJoinPlanMergedSchemaIsOuterThenInner(t *testing.T) {
	a, b := cpjTwoRel()
	p := cpjHashPath(cpjLeafPath(b), cpjLeafPath(a),
		[]*restrictInfo{equiClauseOn(a.Relids, b.Relids, 0, 3)}, nil)
	j := createPlan(p).(*Join)

	want := []string{"b0", "b1", "b2", "a0", "a1"}
	got := j.Output()
	if len(got) != len(want) {
		t.Fatalf("merged schema has %d columns, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Name != want[i] {
			t.Errorf("merged column %d = %q, want %q", i, got[i].Name, want[i])
		}
	}
	if j.Left.Output()[0].Name != "b0" || j.Right.Output()[0].Name != "a0" {
		t.Fatalf("Left/Right are not (outer, inner): %q / %q", j.Left.Output()[0].Name, j.Right.Output()[0].Name)
	}
}

// TestCreateHashJoinPlanBuildSideIsTheChildOrder: `BuildLeft` is never set,
// because `generateHashJoinPaths` already decided the build side BY COST when it
// added the two orientations as separate paths. A second opinion here would be
// the uncosted name-tag rule 06 §2.1 retires.
func TestCreateHashJoinPlanBuildSideIsTheChildOrder(t *testing.T) {
	a, b := cpjTwoRel()
	key := equiClauseOn(a.Relids, b.Relids, 0, 3)
	for _, tc := range []struct {
		name         string
		outer, inner *RelOptInfo
	}{
		{"a drives", a, b},
		{"b drives", b, a},
	} {
		t.Run(tc.name, func(t *testing.T) {
			j := createPlan(cpjHashPath(cpjLeafPath(tc.outer), cpjLeafPath(tc.inner), []*restrictInfo{key}, nil)).(*Join)
			if j.BuildLeft {
				t.Fatal("BuildLeft set; the build side is Children[1] by convention, never re-decided here")
			}
			if j.Algo != JoinAlgoHash || j.Type != JoinTypeInner {
				t.Fatalf("Algo/Type = %v/%v, want hash/inner", j.Algo, j.Type)
			}
		})
	}
}

// TestCreateHashJoinPlanEveryKeyAndResidualReachThePredicate: goopg's executor
// hashes on ONE pair and evaluates `Predicate` per matched pair, so a second
// equality omitted from the predicate would be enforced by nothing — the Q9
// multi-equality wrong-answer case. The residual must land there too, and both
// must be translated.
func TestCreateHashJoinPlanEveryKeyAndResidualReachThePredicate(t *testing.T) {
	a, b := cpjTwoRel()
	k1 := equiClauseOn(a.Relids, b.Relids, 0, 3) // a0 = b1
	k2 := equiClauseOn(a.Relids, b.Relids, 1, 2) // a1 = b0
	resid := &restrictInfo{
		relids: a.Relids | b.Relids,
		ecID:   noEquivClass,
		// `a.a1 < b.b2` — a join qual with no two-sided key split.
		clause: &BinaryOp{Op: parser.OpLt, Left: col(1), Right: col(4)},
	}
	p := cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{k1, k2}, []*restrictInfo{resid})
	j := createPlan(p).(*Join)

	if len(j.HashKeys) != 2 {
		t.Fatalf("HashKeys has %d pairs, want 2", len(j.HashKeys))
	}
	// HashKeys[0] IS (LeftKey, RightKey) by pointer (plan.go:840): the
	// single-pair and list views must not be able to disagree.
	if j.HashKeys[0].Left != j.LeftKey || j.HashKeys[0].Right != j.RightKey {
		t.Fatal("HashKeys[0] is not the same pair as LeftKey/RightKey")
	}
	conj := splitAnd(j.Predicate)
	if len(conj) != 3 {
		t.Fatalf("Predicate has %d conjuncts, want 3 (two keys + one residual)", len(conj))
	}
	// Second key: a1 (binding 1 -> merged 4) = b0 (binding 2 -> merged 0).
	if got := j.HashKeys[1].Left.(*ColumnRef).Index; got != 0 {
		t.Errorf("key 2 outer operand index = %d, want 0", got)
	}
	if got := j.HashKeys[1].Right.(*ColumnRef).Index; got != 4 {
		t.Errorf("key 2 inner operand index = %d, want 4", got)
	}
	// The residual keeps its own operand order (it has no side split) but both
	// operands are renumbered: binding 1 -> 4, binding 4 -> 2.
	last, ok := conj[2].(*BinaryOp)
	if !ok || last.Op != parser.OpLt {
		t.Fatalf("last conjunct = %#v, want the residual inequality", conj[2])
	}
	if last.Left.(*ColumnRef).Index != 4 || last.Right.(*ColumnRef).Index != 2 {
		t.Fatalf("residual operands = %d/%d, want 4/2",
			last.Left.(*ColumnRef).Index, last.Right.(*ColumnRef).Index)
	}
	if resid.clause.(*BinaryOp).Left.(*ColumnRef).Index != 1 {
		t.Fatal("the arm mutated the search's own residual clause")
	}
}

// TestCreateHashJoinPlanSortChildPassesLayoutThrough: a sort reorders rows, not
// columns, so its child's coordinates survive it.
//
// The second assertion is the P5.5-e-ii-a fix to the SORT arm. The pathkey is
// written in binding coordinates like every other expression the search holds —
// `col(2)` is rel b's first column, because b starts at binding offset 2 — while
// the emitted `*Sort` evaluates its key against its CHILD's row, where that
// column is index 0. Before the fix the key went through untranslated and the
// sort ordered by whichever column happened to sit at index 2 (b2). A rel at
// binding offset 0 hides the bug entirely, which is why the fixture puts b
// second.
func TestCreateHashJoinPlanSortChildPassesLayoutThrough(t *testing.T) {
	a, b := cpjTwoRel()
	sorted := &Path{
		Kind:     PathSort,
		Rel:      b,
		Rows:     b.Rows,
		Children: []*Path{cpjLeafPath(b)},
		Pathkeys: []PathKey{{Expr: col(2), SortAsc: true}},
	}
	p := cpjHashPath(sorted, cpjLeafPath(a), []*restrictInfo{equiClauseOn(a.Relids, b.Relids, 0, 3)}, nil)
	j, ok := createPlan(p).(*Join)
	if !ok {
		t.Fatalf("createPlan = %T, want *Join", createPlan(p))
	}
	s, isSort := j.Left.(*Sort)
	if !isSort {
		t.Fatalf("outer child = %T, want *Sort", j.Left)
	}
	if got := s.Keys[0].Expr.(*ColumnRef).Index; got != 0 {
		t.Errorf("Sort key index = %d, want 0 (binding 2 = b0, the child's first column)", got)
	}
	// Same answer as the unsorted fixture: the Sort is transparent.
	if got := j.LeftKey.(*ColumnRef).Index; got != 1 {
		t.Fatalf("LeftKey.Index = %d through a Sort, want 1", got)
	}
}

// TestCreateHashJoinPlanPanics: every precondition the arm promises, each with a
// message naming the defect it prevents.
func TestCreateHashJoinPlanPanics(t *testing.T) {
	a, b := cpjTwoRel()
	goodKey := equiClauseOn(a.Relids, b.Relids, 0, 3)

	cases := []struct {
		name string
		path func() *Path
		want string
	}{
		{
			name: "one child",
			path: func() *Path {
				p := cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{goodKey}, nil)
				p.Children = p.Children[:1]
				return p
			},
			want: "want exactly 2",
		},
		{
			name: "no hash keys",
			path: func() *Path { return cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), nil, nil) },
			want: "keys on nothing only as a nested loop",
		},
		{
			name: "parameterised",
			path: func() *Path {
				p := cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{goodKey}, nil)
				p.RequiredOuter = relsetOf(3)
				return p
			},
			want: "propagates a parameter rather than binding it",
		},
		{
			name: "key is not an equijoin",
			path: func() *Path {
				return cpjHashPath(cpjLeafPath(b), cpjLeafPath(a),
					[]*restrictInfo{plainClause(a.Relids | b.Relids)}, nil)
			},
			want: "is not an equijoin",
		},
		{
			name: "key sides do not match the join",
			path: func() *Path {
				// Both operands on the same side: `clause_sides_match_join`'s
				// refusal — hashing it would compare two columns of one input.
				bad := equiClauseOn(a.Relids, a.Relids, 0, 1)
				bad.rightRelids = a.Relids
				return cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{bad}, nil)
			},
			want: "does not match the join's",
		},
		{
			name: "child coordinates unknown",
			path: func() *Path {
				// A PathPrebuilt over a rel with no recorded leaf — the C0
				// bridge's shape. Its columns were laid out by the integer DP.
				opaque := newRelOptInfo(b.Relids, 100, 8)
				prebuilt := newPrebuiltPath(opaque, &SeqScan{schema: cpjSchema("x", 3)})
				return cpjHashPath(prebuilt, cpjLeafPath(a), []*restrictInfo{goodKey}, nil)
			},
			want: "column coordinates are unknown",
		},
		{
			name: "clause references a column outside the join",
			path: func() *Path {
				// Binding column 9 belongs to no input of this join.
				return cpjHashPath(cpjLeafPath(b), cpjLeafPath(a),
					[]*restrictInfo{equiClauseOn(a.Relids, b.Relids, 0, 9)}, nil)
			},
			want: "is not among the 5 output columns",
		},
		{
			name: "clause carries a non-positional reference",
			path: func() *Path {
				resid := &restrictInfo{
					relids: a.Relids | b.Relids,
					ecID:   noEquivClass,
					clause: &BinaryOp{Op: parser.OpEq, Left: col(0), Right: &CTIDExpr{}},
				}
				return cpjHashPath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{goodKey}, []*restrictInfo{resid})
			},
			want: "which is not positional and cannot be re-based",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("no panic; want one mentioning %q", tc.want)
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.want) {
					t.Fatalf("panic %q does not mention %q", r, tc.want)
				}
			}()
			createPlan(tc.path())
		})
	}
}

// TestBaseRelLayoutRefusesASynthesisedSchema: the layout's width comes from the
// EMITTED node, and a rebuild that produced a different width means a schema was
// synthesised rather than carried — at which point every clause over the rel is
// about to be mistranslated. Checked rather than assumed.
func TestBaseRelLayoutRefusesASynthesisedSchema(t *testing.T) {
	rel := cpjLeafRel(0, 0, 2, "a")
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "the schema was synthesised, not carried") {
			t.Fatalf("panic %q does not name the synthesised schema", r)
		}
	}()
	baseRelLayout(rel, &SeqScan{schema: cpjSchema("a", 3)})
}

// TestOutputLayoutRejectsADuplicateCoordinate: two output columns claiming one
// binding coordinate makes the inverse map ambiguous, and it means the same base
// relation reached this join through both children — the disjointness the
// enumerator exists to guarantee.
func TestOutputLayoutRejectsADuplicateCoordinate(t *testing.T) {
	defer func() {
		r := recover()
		msg, _ := r.(string)
		if !strings.Contains(msg, "both claim binding coordinate") {
			t.Fatalf("panic %q does not name the duplicate coordinate", r)
		}
	}()
	outputLayout{0, 1, 0}.bindingIndex()
}

// M0127-P5.5-e-ii-a — the merge-join arm.
//
// The merge arm shares its whole prologue with the hash arm (the tests above
// cover it once), so what these pin is the two things that are merge-ONLY and
// that a green plan hides: the key list is the SORT ORDER, and goopg's merge
// operator sorts its inputs itself so an explicit `PathSort` child must be
// absorbed rather than emitted.

// cpjMergePath assembles a PathMergeJoin over two child paths. `keys` is the
// ordered merge-clause list `sortInnerAndOuter` produces — group order, which is
// the chosen pathkey order.
func cpjMergePath(outer, inner *Path, keys, residual []*restrictInfo, resultKeys []PathKey) *Path {
	joinrel := newRelOptInfo(outer.Rel.Relids|inner.Rel.Relids, 500, 16)
	return &Path{
		Kind:     PathMergeJoin,
		Rel:      joinrel,
		Rows:     500,
		Children: []*Path{outer, inner},
		HashKeys: keys,
		Residual: residual,
		Pathkeys: resultKeys,
	}
}

// cpjSortOver is the explicit sort `sortPathFor` builds for a merge input:
// ascending, nulls last, keys in BINDING coordinates.
func cpjSortOver(sub *Path, bindingCols ...int) *Path {
	keys := make([]PathKey, len(bindingCols))
	for i, c := range bindingCols {
		keys[i] = PathKey{Expr: col(c), SortAsc: true}
	}
	return &Path{Kind: PathSort, Rel: sub.Rel, Rows: sub.Rows, Children: []*Path{sub}, Pathkeys: keys}
}

// TestCreateMergeJoinPlanKeyOrderIsTheSortOrder. goopg's merge executor sorts
// each side by the key TUPLE in `Join.HashKeys` order (`mergeSideKeyExprs`), so
// the order this arm publishes is not a presentation detail — it is
// `outersortkeys` / `innersortkeys`. The fixture hands the arm two clauses in an
// order the arm must not "tidy": the SECOND clause's operands are the ones that
// would sort first if the list were re-derived from column position.
func TestCreateMergeJoinPlanKeyOrderIsTheSortOrder(t *testing.T) {
	a, b := cpjTwoRel()
	k1 := equiClauseOn(a.Relids, b.Relids, 1, 4) // a1 = b2
	k2 := equiClauseOn(a.Relids, b.Relids, 0, 2) // a0 = b0
	// Outer is b (binding 2-4), inner is a (binding 0-1): merged row is
	// b0 b1 b2 a0 a1.
	p := cpjMergePath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{k1, k2}, nil,
		[]PathKey{{Expr: col(4), SortAsc: true}, {Expr: col(2), SortAsc: true}})

	j, ok := createPlan(p).(*Join)
	if !ok {
		t.Fatalf("createPlan(PathMergeJoin) = %T, want *Join", createPlan(p))
	}
	if j.Algo != JoinAlgoMerge || j.Type != JoinTypeInner {
		t.Fatalf("Algo/Type = %v/%v, want merge/inner", j.Algo, j.Type)
	}
	if j.BuildLeft {
		t.Error("BuildLeft set on a merge join, which has no build side")
	}
	if len(j.HashKeys) != 2 {
		t.Fatalf("HashKeys has %d pairs, want 2", len(j.HashKeys))
	}
	// Pair 0 is clause k1, oriented outer-first: b2 (binding 4 -> merged 2)
	// against a1 (binding 1 -> merged 4).
	if got := j.HashKeys[0].Left.(*ColumnRef).Index; got != 2 {
		t.Errorf("key 0 outer operand = %d, want 2 (b2)", got)
	}
	if got := j.HashKeys[0].Right.(*ColumnRef).Index; got != 4 {
		t.Errorf("key 0 inner operand = %d, want 4 (a1)", got)
	}
	// Pair 1 is clause k2: b0 (binding 2 -> merged 0) against a0 (binding 0 ->
	// merged 3). Its operands sort BEFORE pair 0's; the order must still be the
	// producer's.
	if got := j.HashKeys[1].Left.(*ColumnRef).Index; got != 0 {
		t.Errorf("key 1 outer operand = %d, want 0 (b0)", got)
	}
	if got := j.HashKeys[1].Right.(*ColumnRef).Index; got != 3 {
		t.Errorf("key 1 inner operand = %d, want 3 (a0)", got)
	}
	if j.HashKeys[0].Left != j.LeftKey || j.HashKeys[0].Right != j.RightKey {
		t.Fatal("HashKeys[0] is not the same pair as LeftKey/RightKey")
	}
}

// TestCreateMergeJoinPlanKeyOrderSurvivesFillJoinHashKeys is the reason the arm
// folds every key into `Predicate` in key order. `Join.HashKeys` is REBUILT from
// `Predicate` at the tail of `Plan()` (join_hash_keys.go), so whatever this arm
// publishes is overwritten by the conjunct order — and for a merge join that
// list is the sort order. If the two ever disagreed, the plan would sort by one
// tuple and merge-compare by another, which silently drops matching rows.
func TestCreateMergeJoinPlanKeyOrderSurvivesFillJoinHashKeys(t *testing.T) {
	a, b := cpjTwoRel()
	k1 := equiClauseOn(a.Relids, b.Relids, 1, 4)
	k2 := equiClauseOn(a.Relids, b.Relids, 0, 2)
	p := cpjMergePath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{k1, k2}, nil,
		[]PathKey{{Expr: col(4), SortAsc: true}, {Expr: col(2), SortAsc: true}})
	j := createPlan(p).(*Join)

	before := make([][2]int, len(j.HashKeys))
	for i, kp := range j.HashKeys {
		before[i] = [2]int{kp.Left.(*ColumnRef).Index, kp.Right.(*ColumnRef).Index}
	}
	fillOneJoinHashKeys(j)
	if len(j.HashKeys) != len(before) {
		t.Fatalf("the rebuild changed the key count: %d -> %d", len(before), len(j.HashKeys))
	}
	for i, kp := range j.HashKeys {
		got := [2]int{kp.Left.(*ColumnRef).Index, kp.Right.(*ColumnRef).Index}
		if got != before[i] {
			t.Errorf("key %d after the rebuild = %v, want %v", i, got, before[i])
		}
	}
}

// TestCreateMergeJoinPlanAbsorbsSortChildren. PG's `create_mergejoin_plan`
// MATERIALISES a Sort from `outersortkeys`/`innersortkeys` because its executor
// requires sorted input; goopg's `JoinAlgoMerge` operator sorts both inputs
// itself (`openMergeJoin`). Emitting the path's explicit `PathSort` children as
// `*Sort` nodes would therefore sort each side twice — a cost
// `tryMergeJoinPath` never charged. The arm steps over them.
func TestCreateMergeJoinPlanAbsorbsSortChildren(t *testing.T) {
	a, b := cpjTwoRel()
	key := equiClauseOn(a.Relids, b.Relids, 0, 2) // a0 = b0
	p := cpjMergePath(
		cpjSortOver(cpjLeafPath(b), 2),
		cpjSortOver(cpjLeafPath(a), 0),
		[]*restrictInfo{key}, nil,
		[]PathKey{{Expr: col(2), SortAsc: true}})

	j := createPlan(p).(*Join)
	if _, isSort := j.Left.(*Sort); isSort {
		t.Error("outer child is a *Sort; the merge operator re-sorts it, so the node is doubled work")
	}
	if _, isSort := j.Right.(*Sort); isSort {
		t.Error("inner child is a *Sort; the merge operator re-sorts it, so the node is doubled work")
	}
	if _, isScan := j.Left.(*SeqScan); !isScan {
		t.Fatalf("outer child = %T, want the *SeqScan under the absorbed sort", j.Left)
	}
	// Absorbing must be coordinate-neutral: a sort passes its child's layout
	// through, so the keys land exactly where they do without one.
	if got := j.LeftKey.(*ColumnRef).Index; got != 0 {
		t.Errorf("LeftKey.Index = %d, want 0 (b0)", got)
	}
	if got := j.RightKey.(*ColumnRef).Index; got != 3 {
		t.Errorf("RightKey.Index = %d, want 3 (a0)", got)
	}
	if names := j.Output(); len(names) != 5 || names[0].Name != "b0" || names[3].Name != "a0" {
		t.Fatalf("merged schema is not outer++inner after absorption: %v", names)
	}
}

// TestCreateMergeJoinPlanPanics: the merge-only preconditions. The two ordering
// refusals are guards for P5.4c-ii's ordered index paths — goopg's merge
// comparator is fixed ascending/nulls-last, so a path promising any other
// ordering promises something the emitted node will not deliver.
func TestCreateMergeJoinPlanPanics(t *testing.T) {
	a, b := cpjTwoRel()
	key := equiClauseOn(a.Relids, b.Relids, 0, 2)
	asc := []PathKey{{Expr: col(2), SortAsc: true}}

	cases := []struct {
		name string
		path func() *Path
		want string
	}{
		{
			name: "no merge clauses",
			path: func() *Path { return cpjMergePath(cpjLeafPath(b), cpjLeafPath(a), nil, nil, asc) },
			want: "no ordering to merge on",
		},
		{
			name: "parameterised",
			path: func() *Path {
				p := cpjMergePath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{key}, nil, asc)
				p.RequiredOuter = relsetOf(3)
				return p
			},
			want: "propagates a parameter rather than binding it",
		},
		{
			name: "descending result ordering",
			path: func() *Path {
				return cpjMergePath(cpjLeafPath(b), cpjLeafPath(a), []*restrictInfo{key}, nil,
					[]PathKey{{Expr: col(2), SortAsc: false}})
			},
			want: "result pathkey 0 is descending, nulls last",
		},
		{
			name: "nulls-first sort child",
			path: func() *Path {
				sorted := cpjSortOver(cpjLeafPath(b), 2)
				sorted.Pathkeys[0].NullsFirst = true
				return cpjMergePath(sorted, cpjLeafPath(a), []*restrictInfo{key}, nil, asc)
			},
			want: "outer sort key 0 is ascending, nulls first",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				r := recover()
				if r == nil {
					t.Fatalf("no panic; want one mentioning %q", tc.want)
				}
				if msg, _ := r.(string); !strings.Contains(msg, tc.want) {
					t.Fatalf("panic %q does not mention %q", r, tc.want)
				}
			}()
			createPlan(tc.path())
		})
	}
}

// TestCreateHashJoinPlanThreadsPathRows verifies M0128-P3.2: the join plan
// carries the search's post-qual row estimates (OuterRows / InnerRows) from
// the child paths, so the executor's buildGeometry can read them instead of
// recomputing via EstimateRows(buildNode) — which dispatches to seqScanRows
// and ignores on-scan quals (09 §3.23).
func TestCreateHashJoinPlanThreadsPathRows(t *testing.T) {
	a, b := cpjTwoRel()
	// Give the paths deliberately distinct row counts, different from the
	// hardcoded 100 in cpjLeafRel.
	outerPath := cpjLeafPath(a)
	outerPath.Rows = 150
	innerPath := cpjLeafPath(b)
	innerPath.Rows = 30

	key := equiClauseOn(a.Relids, b.Relids, 0, 3)
	p := cpjHashPath(outerPath, innerPath, []*restrictInfo{key}, nil)
	j, ok := createPlan(p).(*Join)
	if !ok {
		t.Fatalf("createPlan = %T, want *Join", createPlan(p))
	}
	if j.OuterRows != 150 {
		t.Errorf("OuterRows = %v, want 150 — the search's post-qual estimate for the outer side", j.OuterRows)
	}
	if j.InnerRows != 30 {
		t.Errorf("InnerRows = %v, want 30 — the search's post-qual estimate for the build/inner side", j.InnerRows)
	}

	// Zero path Rows → zero plan Rows, which tells buildGeometry to fall back
	// to EstimateRows (the legacy path). This exercises the fallback guard.
	outerPath.Rows = 0
	innerPath.Rows = 0
	p2 := cpjHashPath(outerPath, innerPath, []*restrictInfo{key}, nil)
	j2, ok := createPlan(p2).(*Join)
	if !ok {
		t.Fatalf("createPlan = %T, want *Join", createPlan(p2))
	}
	if j2.OuterRows != 0 || j2.InnerRows != 0 {
		t.Errorf("OuterRows/InnerRows = %v/%v, want 0/0 — zero path Rows means \"come back to EstimateRows\"",
			j2.OuterRows, j2.InnerRows)
	}

	// The default-nil case: a Join built by the legacy planner has zero values
	// and must not crash the executor.
	legacy := &Join{}
	if legacy.OuterRows != 0 || legacy.InnerRows != 0 {
		t.Error("a zero-value Join must have zero OuterRows/InnerRows")
	}
}
