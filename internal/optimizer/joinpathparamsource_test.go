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
	// statement leaf 2+i. SJI hands are statement-global on the way IN and
	// are put into item space by `sjInfosInItemSpace` (the C-04a Q72 fix
	// moved the remap there, out of this function, so that every consumer
	// of the list — `joinIsLegal` first — reads the same coordinates).
	sj := psSJI(parser.JoinLeft, 0b100, 0b1000) // stmt leaf 2 LEFT JOIN stmt leaf 3
	items := psItems(2, 2)
	sjis, err := sjInfosInItemSpace([]*SpecialJoinInfo{sj}, items)
	if err != nil {
		t.Fatal(err)
	}
	// Problem joinrel {1} (= stmt leaf 3, the RHS): overlap RHS,
	// not LHS → all{0,1} − {1} = {0}.
	if got := paramSourceRelsForProblem(0b10, sjis, items); got != 0b01 {
		t.Errorf("remapped partial RHS = %04b, want 0001", got)
	}
	// Problem joinrel {0} (= stmt leaf 2, the LHS): no RHS overlap → 0.
	if got := paramSourceRelsForProblem(0b01, sjis, items); got != 0 {
		t.Errorf("remapped LHS = %04b, want 0", got)
	}
	// Remap-exactness: same SJI on the aligned problem (lo=0 run over
	// the same two leaves renumbered) must agree bit-for-bit.
	aligned := psSJI(parser.JoinLeft, 0b001, 0b010)
	if got, want := paramSourceRelsForProblem(0b10, sjis, items),
		paramSourceRelsForProblem(0b10, []*SpecialJoinInfo{aligned}, psItems(0, 2)); got != want {
		t.Errorf("remap breaks equivalence: sub-problem %04b vs aligned %04b", got, want)
	}
}

// TestSJInfosInItemSpace pins the C-04a Q72 mechanism at the function that
// fixes it. Q72's joinlist after `join_collapse_limit` splits its nine inner
// links is `[sub(leaves 0..7), d3, promotion, catalog_returns]`: a 4-item
// problem whose two LEFT SJIs name leaves 9 and 10 as their nullable sides.
// Un-remapped, no item-space joinrel ever overlaps bit 9 or 10, `joinIsLegal`
// finds neither SJI relevant, and both LEFT links come back as INNER joins —
// 100 → 84 rows on the SF0.5 oracle.
func TestSJInfosInItemSpace(t *testing.T) {
	items := []joinlistRel{
		{lo: 0, hi: 8}, // the sub-problem: leaves 0..7 as ONE item
		{lo: 8, hi: 9},
		{lo: 9, hi: 10},
		{lo: 10, hi: 11},
	}
	promo := psSJI(parser.JoinLeft, 0b000000001, 0b1000000000)       // {cs} LEFT {promotion}
	promo.SynLefthand = 0b111111111                                    // the whole 9-leaf chain
	cr := psSJI(parser.JoinLeft, 0b000000001, 0b10000000000)          // {cs} LEFT {catalog_returns}
	cr.SynLefthand = 0b1111111111                                      // chain + promotion
	got, err := sjInfosInItemSpace([]*SpecialJoinInfo{promo, cr}, items)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d SJIs, want 2", len(got))
	}
	// Both hands land on items: {cs} is inside item 0; promotion IS item 2;
	// catalog_returns IS item 3.
	if got[0].MinLefthand != 0b0001 || got[0].MinRighthand != 0b0100 {
		t.Errorf("promotion SJI = L %04b R %04b, want L 0001 R 0100", got[0].MinLefthand, got[0].MinRighthand)
	}
	if got[0].SynLefthand != 0b0011 || got[0].SynRighthand != 0b0100 {
		t.Errorf("promotion SJI syn = L %04b R %04b, want L 0011 R 0100", got[0].SynLefthand, got[0].SynRighthand)
	}
	if got[1].MinLefthand != 0b0001 || got[1].MinRighthand != 0b1000 {
		t.Errorf("catalog_returns SJI = L %04b R %04b, want L 0001 R 1000", got[1].MinLefthand, got[1].MinRighthand)
	}
	// The inputs are untouched: the remap is a copy, because the same list
	// is remapped again, differently, by every sub-problem.
	if promo.MinRighthand != 0b1000000000 || cr.MinRighthand != 0b10000000000 {
		t.Fatal("sjInfosInItemSpace mutated its input")
	}

	// Inside the 8-leaf sub-problem the same two SJIs have their nullable
	// sides OUTSIDE the window. They must keep a non-empty RHS (the marker
	// bit just above the window) so `joinIsLegal`'s first test skips them
	// exactly as it skipped the leaf-space bits 9 and 10 before — zeroing
	// the hand would make `relsSubset(0, x)` true in three consumers.
	sub := make([]joinlistRel, 8)
	for i := range sub {
		sub[i] = joinlistRel{lo: i, hi: i + 1}
	}
	inner, err := sjInfosInItemSpace([]*SpecialJoinInfo{promo, cr}, sub)
	if err != nil {
		t.Fatal(err)
	}
	outside := RelSet(1) << 8
	for i, sj := range inner {
		if sj.MinRighthand != outside {
			t.Errorf("sub-problem SJI %d RHS = %#x, want the outside marker %#x", i, sj.MinRighthand, outside)
		}
		if sj.MinLefthand != 0b1 {
			t.Errorf("sub-problem SJI %d LHS = %04b, want 0001 ({cs} is leaf 0)", i, sj.MinLefthand)
		}
		if got := sj.SynLefthand & (outside - 1); got != 0b11111111 {
			t.Errorf("sub-problem SJI %d syn LHS window part = %08b, want all eight leaves", i, got)
		}
	}
	// The identity case: single-leaf items from leaf 0 change nothing.
	id, err := sjInfosInItemSpace([]*SpecialJoinInfo{psSJI(parser.JoinLeft, 0b01, 0b10)}, psItems(0, 2))
	if err != nil {
		t.Fatal(err)
	}
	if id[0].MinLefthand != 0b01 || id[0].MinRighthand != 0b10 {
		t.Errorf("identity remap = L %04b R %04b", id[0].MinLefthand, id[0].MinRighthand)
	}
	// nil in, nil out — the seam's no-outer-join fast paths key on length.
	if got, _ := sjInfosInItemSpace(nil, items); got != nil {
		t.Errorf("nil list remapped to %v", got)
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
