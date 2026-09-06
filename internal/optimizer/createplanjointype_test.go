package optimizer

// C-03c (docs/design/planner-c03-jointype-search/DESIGN.md §4) — the
// `createPlanNode` jointype arms, landed INERT.
//
// These arms are UNREACHABLE from the live search: nothing files a non-INNER
// path today (C-03 DESIGN §3 — LEFT/RIGHT are peeled by `splitOuterSpine`,
// SEMI/ANTI declined at the seam gate, FULL declines the whole search), and
// C-04 is what changes that. "An unwinnable path is an untested path" has fired
// three times in this workstream — most recently C-19f, where letting a Gather
// win at a search root exposed two `createPlan` bugs that had been sitting in
// an arm nothing could reach. So every arm here is forced through with a
// hand-built path rather than waited for.
//
// The narrowing is the part that would bite: a searched SEMI planned at merged
// width misaligns every parent translation, and for a `NestedLoopIndexJoin` it
// is not even cosmetic — `Output` returns the schema field raw and the executor
// raises XX000 on the width mismatch.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// cpjJoinPath assembles a join path of the given kind and jointype over the
// standard two-rel fixture, with `outer` driving.
func cpjJoinPath(kind PathKind, jt parser.JoinType, outer, inner *Path, keys, residual []*restrictInfo) *Path {
	joinrel := newRelOptInfo(outer.Rel.Relids|inner.Rel.Relids, 500, 16)
	return &Path{
		Kind:     kind,
		Jointype: jt,
		Rel:      joinrel,
		Rows:     500,
		Children: []*Path{outer, inner},
		HashKeys: keys,
		Residual: residual,
	}
}

// TestPlanJoinTypeForMapsEveryGeneratedJointype: the two JoinType enums meet in
// exactly one place, so a jointype cannot reach a plan through one route and
// not another. That sibling-disagreement shape is this codebase's most reliable
// bug class.
func TestPlanJoinTypeForMapsEveryGeneratedJointype(t *testing.T) {
	for _, tc := range []struct {
		in   parser.JoinType
		want JoinType
	}{
		{parser.JoinInner, JoinTypeInner},
		{parser.JoinLeft, JoinTypeLeft},
		{parser.JoinRight, JoinTypeRight},
		{parser.JoinSemi, JoinTypeSemi},
		{parser.JoinAnti, JoinTypeAnti},
	} {
		if got := planJoinTypeFor(&Path{Jointype: tc.in}, "test"); got != tc.want {
			t.Errorf("planJoinTypeFor(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
	// A nil path is the unparameterised default every non-join caller would
	// see, and it must read as INNER rather than panicking.
	if got := planJoinTypeFor(nil, "test"); got != JoinTypeInner {
		t.Errorf("planJoinTypeFor(nil) = %v, want JoinTypeInner", got)
	}
	// FULL has no arm. It is declined at path generation, so arriving here is
	// a producer bug and must be loud rather than silently planned as an inner
	// join that drops the rows a full join exists to keep.
	for _, bad := range []parser.JoinType{parser.JoinFull, parser.JoinCross} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("planJoinTypeFor(%v) returned instead of refusing", bad)
				}
			}()
			planJoinTypeFor(&Path{Jointype: bad}, "test")
		}()
	}
}

// TestCreatePlanCarriesPathJointype: every join arm reads the path's jointype
// into the node's Type. All four arms are exercised, because "a jointype that
// reaches Join.Type through one route and not another" is precisely the defect
// this slice could introduce.
func TestCreatePlanCarriesPathJointype(t *testing.T) {
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft, parser.JoinRight} {
		want := planJoinTypeFor(&Path{Jointype: jt}, "test")

		a, b := cpjTwoRel()
		key := equiClauseOn(a.Relids, b.Relids, 0, 3)
		resid := plainClause(a.Relids | b.Relids)
		resid.clause = cpnEq(col(1), col(2))

		hash := createPlan(cpjJoinPath(PathHashJoin, jt, cpjLeafPath(b), cpjLeafPath(a),
			[]*restrictInfo{key}, nil)).(*Join)
		if hash.Type != want || hash.Algo != JoinAlgoHash {
			t.Errorf("hash arm: Type/Algo = %v/%v, want %v/hash", hash.Type, hash.Algo, want)
		}

		nl := createPlan(cpjJoinPath(PathNestLoop, jt, cpjLeafPath(b), cpjLeafPath(a),
			nil, []*restrictInfo{resid})).(*Join)
		if nl.Type != want || nl.Algo != JoinAlgoNestedLoop {
			t.Errorf("nestloop arm: Type/Algo = %v/%v, want %v/nestloop", nl.Type, nl.Algo, want)
		}
	}
}

// TestSearchedSemiAntiPlansLeftOnly is C-03c's named gate: a semi/anti joinrel
// plans to a left-only Semi/Anti join. Both halves of the publication are
// checked — the SCHEMA and the LAYOUT — because they are produced by different
// expressions and `Join.Output`'s own narrowing hides a wrong schema while
// leaving a wrong layout to misalign every parent.
func TestSearchedSemiAntiPlansLeftOnly(t *testing.T) {
	for _, jt := range []parser.JoinType{parser.JoinSemi, parser.JoinAnti} {
		t.Run(joinTypeName(jt), func(t *testing.T) {
			want := planJoinTypeFor(&Path{Jointype: jt}, "test")
			a, b := cpjTwoRel()
			resid := plainClause(a.Relids | b.Relids)
			resid.clause = cpnEq(col(1), col(2))
			// The nested loop is the only arm a searched SEMI/ANTI can reach
			// (C-03b: the keyed operators decline).
			p := cpjJoinPath(PathNestLoop, jt, cpjLeafPath(b), cpjLeafPath(a),
				nil, []*restrictInfo{resid})
			node, lay := createPlanNode(p)
			j, ok := node.(*Join)
			if !ok {
				t.Fatalf("built %T, want *Join", node)
			}
			if j.Type != want {
				t.Fatalf("Type = %v, want %v", j.Type, want)
			}
			left := j.Left.Output()
			if got := j.Output(); len(got) != len(left) {
				t.Errorf("Output width %d, want %d (Left only)", len(got), len(left))
			}
			// The schema FIELD, not just the derived Output: the unnesting
			// producer stores left-only there (nl_index_join.go:684-691) and
			// the two producers must agree.
			if len(j.schema) != len(left) {
				t.Errorf("schema field width %d, want %d — a merged schema beside a "+
					"narrowing Output is exactly the sibling disagreement C-03c "+
					"exists to prevent", len(j.schema), len(left))
			}
			if len(lay) != len(left) {
				t.Fatalf("published layout width %d, want %d — a merged layout "+
					"misaligns every parent translation", len(lay), len(left))
			}
			for i := range left {
				if j.Output()[i].Name != left[i].Name {
					t.Errorf("published column %d = %q, want %q",
						i, j.Output()[i].Name, left[i].Name)
				}
			}
		})
	}
}

// TestNonSemiAntiPublishesMergedRow is the control for the test above: for every
// other jointype the publication is still the full merged row, so the narrowing
// is a SEMI/ANTI rule and not a width change the whole search would feel.
func TestNonSemiAntiPublishesMergedRow(t *testing.T) {
	for _, jt := range []parser.JoinType{parser.JoinInner, parser.JoinLeft, parser.JoinRight} {
		a, b := cpjTwoRel()
		key := equiClauseOn(a.Relids, b.Relids, 0, 3)
		node, lay := createPlanNode(cpjJoinPath(PathHashJoin, jt, cpjLeafPath(b), cpjLeafPath(a),
			[]*restrictInfo{key}, nil))
		j := node.(*Join)
		wantWidth := len(j.Left.Output()) + len(j.Right.Output())
		if len(j.Output()) != wantWidth || len(lay) != wantWidth {
			t.Errorf("%v: published %d cols / %d layout entries, want %d — only SEMI "+
				"and ANTI narrow", jt, len(j.Output()), len(lay), wantWidth)
		}
	}
}
