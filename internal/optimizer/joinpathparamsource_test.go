package optimizer

// C-08 gate: PG joinpath.c:242-276 derivation table + the frame remap
// rule. Bit convention: statement leaf i = bit i; problem items renumber
// to 1<<i.

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

func psSJI(jt parser.JoinType, minL, minR RelSet) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		SynLefthand:  minL,
		SynRighthand: minR,
		MinLefthand:  minL,
		MinRighthand: minR,
		Jointype:     jt,
	}
}

// psItems builds a single-leaf-consecutive run [lo, lo+n).
func psItems(lo, n int) []joinlistRel {
	out := make([]joinlistRel, n)
	for i := range out {
		out[i] = joinlistRel{lo: lo + i, hi: lo + i + 1}
	}
	return out
}

func TestParamSourceRelsDerivation(t *testing.T) {
	left2 := psSJI(parser.JoinLeft, 0b001, 0b010)   // a LEFT JOIN b
	left3 := psSJI(parser.JoinLeft, 0b001, 0b110)   // a LEFT JOIN (b,c)
	full2 := psSJI(parser.JoinFull, 0b001, 0b010)   // a FULL JOIN b
	inner2 := psSJI(parser.JoinInner, 0b001, 0b010) // plain inner
	tests := []struct {
		name  string
		rel   RelSet
		sjis  []*SpecialJoinInfo
		items []joinlistRel
		want  RelSet
	}{
		{"empty list", 0b011, nil, psItems(0, 2), 0},
		{"empty items", 0b011, []*SpecialJoinInfo{left2}, nil, 0},
		{"nil entry skipped", 0b001, []*SpecialJoinInfo{nil, left2}, psItems(0, 2), 0},
		// Complete joinrel overlapping both hands: no constraint.
		{"inner complete", 0b011, []*SpecialJoinInfo{inner2}, psItems(0, 2), 0},
		{"left complete", 0b011, []*SpecialJoinInfo{left2}, psItems(0, 2), 0},
		// Partial RHS, LHS not yet joined: all−RHS may source.
		{"left partial RHS", 0b010, []*SpecialJoinInfo{left2}, psItems(0, 2), 0b001},
		// RHS complete without LHS: still building RHS, no constraint.
		{"left rhs-only joinrel", 0b010, []*SpecialJoinInfo{left2}, psItems(0, 1)[0:1], 0},
		// Three-rel: RHS pair without LHS.
		{"left3 rhs pair", 0b110, []*SpecialJoinInfo{left3}, psItems(0, 3), 0b001},
		{"left3 partial rhs", 0b010, []*SpecialJoinInfo{left3}, psItems(0, 3), 0b001},
		// FULL symmetric: LHS without RHS constrains the other way.
		{"full lhs-only", 0b001, []*SpecialJoinInfo{full2}, psItems(0, 2), 0b010},
		{"full rhs-only", 0b010, []*SpecialJoinInfo{full2}, psItems(0, 2), 0b001},
		{"full complete", 0b011, []*SpecialJoinInfo{full2}, psItems(0, 2), 0},
		// Constant conjunct (empty joinrelids): never overlaps.
		{"empty joinrelids", 0, []*SpecialJoinInfo{left2}, psItems(0, 2), 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := paramSourceRelsForProblem(tc.rel, tc.sjis, tc.items); got != tc.want {
				t.Errorf("paramSourceRelsForProblem(%04b) = %04b, want %04b",
					tc.rel, got, tc.want)
			}
		})
	}
}

func TestParamSourceRelsRemap(t *testing.T) {
	// Sub-problem over statement leaves [2,4): problem bit i =
	// statement leaf 2+i. SJI hands stay statement-global.
	sj := psSJI(parser.JoinLeft, 0b100, 0b1000) // stmt leaf 2 LEFT JOIN stmt leaf 3
	items := psItems(2, 2)
	// Problem joinrel {1} (= stmt leaf 3, the RHS): overlap RHS,
	// not LHS → all{0,1} − {1} = {0}.
	if got := paramSourceRelsForProblem(0b10, []*SpecialJoinInfo{sj}, items); got != 0b01 {
		t.Errorf("remapped partial RHS = %04b, want 0001", got)
	}
	// Problem joinrel {0} (= stmt leaf 2, the LHS): no RHS overlap → 0.
	if got := paramSourceRelsForProblem(0b01, []*SpecialJoinInfo{sj}, items); got != 0 {
		t.Errorf("remapped LHS = %04b, want 0", got)
	}
	// Remap-exactness: same SJI on the aligned problem (lo=0 run over
	// the same two leaves renumbered) must agree bit-for-bit.
	aligned := psSJI(parser.JoinLeft, 0b001, 0b010)
	if got, want := paramSourceRelsForProblem(0b10, []*SpecialJoinInfo{sj}, items),
		paramSourceRelsForProblem(0b10, []*SpecialJoinInfo{aligned}, psItems(0, 2)); got != want {
		t.Errorf("remap breaks equivalence: sub-problem %04b vs aligned %04b", got, want)
	}
}

func TestParamSourceRelsMisaligned(t *testing.T) {
	sj := psSJI(parser.JoinLeft, 0b001, 0b010)
	// Gapped run: items skip statement leaf 1 — not single-leaf-
	// consecutive, so the frame is unprovable → legacy 0.
	gapped := []joinlistRel{{lo: 0, hi: 1}, {lo: 2, hi: 3}}
	if got := paramSourceRelsForProblem(0b10, []*SpecialJoinInfo{sj}, gapped); got != 0 {
		t.Errorf("gapped items = %04b, want 0 (fail-closed)", got)
	}
	// Multi-leaf item: one item spanning two statement leaves.
	wide := []joinlistRel{{lo: 0, hi: 2}}
	if got := paramSourceRelsForProblem(0b01, []*SpecialJoinInfo{sj}, wide); got != 0 {
		t.Errorf("multi-leaf item = %04b, want 0 (fail-closed)", got)
	}
}
