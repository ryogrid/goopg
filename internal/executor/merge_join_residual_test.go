package executor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// M0125-0011 regression: a merge join's key is only the FIRST equality
// conjunct of its ON clause (splitEqualityForHash returns a single
// left/right pair). Before the fix runMergeJoin joined on that key alone
// and never evaluated Join.Predicate, so every remaining conjunct was
// silently dropped: a two-conjunct FULL OUTER JOIN produced exactly the
// row set of its single-key counterpart. Discovered on TPC-DS Q97, whose
// two-key FULL OUTER JOIN returned 2131274 rows where PostgreSQL returns
// 836302.
//
// The expected row sets below are the *measured* PostgreSQL 18.3 answers
// for this data (captured against the reference cluster, all four join
// types), not hand-derived ones.
//
// PostgreSQL reference: src/backend/executor/nodeMergejoin.c —
// EXEC_MJ_JOINTUPLES applies ExecQual(joinqual) after the mergeclauses
// have located the equal-key group, and MJ_FILL_OUTER/MJ_FILL_INNER
// null-extend the rows that the joinqual rejected.

// mergeResidualSide builds a Values node of two-column int rows.
func mergeResidualSide(pairs ...[2]int64) *planner.Values {
	rows := make([][]planner.Expr, 0, len(pairs))
	for _, p := range pairs {
		rows = append(rows, []planner.Expr{
			&planner.IntegerConst{Value: p[0]},
			&planner.IntegerConst{Value: p[1]},
		})
	}
	return &planner.Values{Rows: rows}
}

func mergeResidualColRef(idx int, name string) *planner.ColumnRef {
	return &planner.ColumnRef{Index: idx, Name: name, Type: catalog.Type{Name: "int4"}}
}

// formatRows renders rows as "a,b,c,d" with NULLs as "" so expected
// values stay readable.
func formatRows(rows []Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		parts := make([]string, 0, len(r))
		for _, d := range r {
			if d.IsNull() {
				parts = append(parts, "")
				continue
			}
			parts = append(parts, fmt.Sprintf("%d", d.Int))
		}
		out = append(out, strings.Join(parts, ","))
	}
	return out
}

func TestExecMergeJoinAppliesResidualConjuncts(t *testing.T) {
	// Merged row layout is [l.a, l.b, r.a, r.b], so leftWidth == 2.
	left := mergeResidualSide([2]int64{1, 10}, [2]int64{1, 20}, [2]int64{2, 30})
	right := mergeResidualSide([2]int64{1, 10}, [2]int64{1, 99}, [2]int64{3, 30})

	keyEq := func() *planner.BinaryOp {
		return &planner.BinaryOp{
			Op:    parser.OpEq,
			Left:  mergeResidualColRef(0, "a"),
			Right: mergeResidualColRef(2, "a"),
		}
	}
	// ON l.a = r.a AND l.b = r.b — the second conjunct is the residual
	// that used to be dropped.
	twoConjunct := &planner.BinaryOp{
		Op:   parser.OpAnd,
		Left: keyEq(),
		Right: &planner.BinaryOp{
			Op:    parser.OpEq,
			Left:  mergeResidualColRef(1, "b"),
			Right: mergeResidualColRef(3, "b"),
		},
	}

	build := func(jt planner.JoinType, pred planner.Expr) *planner.Join {
		return &planner.Join{
			Type:      jt,
			Algo:      planner.JoinAlgoMerge,
			Left:      left,
			Right:     right,
			Predicate: pred,
			LeftKey:   mergeResidualColRef(0, "a"),
			RightKey:  mergeResidualColRef(2, "a"),
		}
	}

	run := func(t *testing.T, plan *planner.Join) []string {
		t.Helper()
		op, err := Build(plan)
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		rows, err := Run(op, NewContext())
		if err != nil {
			t.Fatalf("Run: %v", err)
		}
		return formatRows(rows)
	}

	cases := []struct {
		name string
		jt   planner.JoinType
		want []string
	}{
		{
			name: "full",
			jt:   planner.JoinTypeFull,
			want: []string{"1,10,1,10", "1,20,,", ",,1,99", "2,30,,", ",,3,30"},
		},
		{
			name: "right",
			jt:   planner.JoinTypeRight,
			want: []string{"1,10,1,10", ",,1,99", ",,3,30"},
		},
		{
			name: "left",
			jt:   planner.JoinTypeLeft,
			want: []string{"1,10,1,10", "1,20,,", "2,30,,"},
		},
		{
			name: "inner",
			jt:   planner.JoinTypeInner,
			want: []string{"1,10,1,10"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := run(t, build(tc.jt, twoConjunct))
			if len(got) != len(tc.want) {
				t.Fatalf("row count = %d, want %d\n got=%v\nwant=%v",
					len(got), len(tc.want), got, tc.want)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Errorf("row[%d] = %q, want %q\n got=%v\nwant=%v",
						i, got[i], tc.want[i], got, tc.want)
				}
			}
		})
	}

	// The acceptance criterion from the fix_plan item: a two-key FULL
	// OUTER JOIN must NOT collapse onto its single-key counterpart. This
	// is the assertion that actually fails on the pre-fix executor —
	// both sides returned the identical 6-row set.
	t.Run("two_key_full_differs_from_single_key", func(t *testing.T) {
		twoKey := run(t, build(planner.JoinTypeFull, twoConjunct))
		singleKey := run(t, build(planner.JoinTypeFull, keyEq()))
		if len(singleKey) != 6 {
			t.Fatalf("single-key FULL rows = %d, want 6 (guards the fixture): %v",
				len(singleKey), singleKey)
		}
		if strings.Join(twoKey, "|") == strings.Join(singleKey, "|") {
			t.Fatalf("two-conjunct FULL OUTER JOIN collapsed onto its single-key "+
				"counterpart (residual conjunct dropped): %v", twoKey)
		}
	})
}
