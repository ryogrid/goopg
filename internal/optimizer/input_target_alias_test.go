package optimizer

import "testing"

// M0146-0030 — the three input-target derivations map key reads to input
// positions BY NAME. A column-alias list (`FROM (SELECT f1 …) ss(z)`) renames
// the binding but not the leaf's output schema, so a key named `z` meets an
// input column named `f1`: the mapping cannot be trusted and the derivation
// must decline (unknown), never publish a keep without the key — which the
// coverage assert then turned into a planner panic (regress join.sql:3217,
// arrays.sql:682).
func TestInputTargetDerivationsDeclineAnAbsentKeyName(t *testing.T) {
	if keep, ok := deriveSortInputKeep(sitSort([]string{"f1", "g"}, []SortKey{{Expr: sitCol("z", 0)}}), nil); ok {
		t.Fatalf("Sort: key z absent from input [f1 g] must decline, got keep %v", keep)
	}
	if keep, ok := deriveAggregateInputKeep(gitAgg([]string{"f1", "g"}, []Expr{gitCol("z", 0)}, nil), nil); ok {
		t.Fatalf("Aggregate: group input z absent from input must decline, got keep %v", keep)
	}
	if keep, ok := deriveWindowInputKeep(witWin([]string{"f1", "g"}, nil, []SortKey{{Expr: sitCol("z", 0)}}, nil, nil), nil); ok {
		t.Fatalf("WindowAgg: order key z absent from input must decline, got keep %v", keep)
	}
	// A present key still derives.
	if keep, ok := deriveSortInputKeep(sitSort([]string{"f1", "z"}, []SortKey{{Expr: sitCol("z", 1)}}), nil); !ok || len(keep) != 1 || keep[0] != 1 {
		t.Fatalf("Sort: present key z must derive keep [1], got %v ok=%v", keep, ok)
	}
}
