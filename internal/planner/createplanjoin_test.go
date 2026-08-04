package planner

// M0127-P5.5-e-i — the coordinate carrier and the hash-join `createPlan` arm
// (createplanjoin.go).
//
// The arm is inert in production (`GOOPG_PGSHAPED_DP` OFF, no `planSelect`
// caller), so these tests are its only observer. What they pin is the one thing
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
// columns, so its child's coordinates survive it. This is the property the merge
// arm (P5.5-e-ii) depends on — its inputs are `PathSort` children.
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
	if _, isSort := j.Left.(*Sort); !isSort {
		t.Fatalf("outer child = %T, want *Sort", j.Left)
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
			want: "is not among this join's",
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
			want: "cannot be re-based onto a merged join row",
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
