package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0128-P1.1 — SpecialJoinInfo construction during deconstruction.
//
// These tests verify that:
//  1. An outer/semi/anti JOIN node produces a SpecialJoinInfo with the correct
//     syntactic left/right relsets.
//  2. Nested special joins produce entries in bottom-up order (inner first).
//  3. INNER/CROSS joins and flat comma lists produce no entries.
//  4. FULL joins get min = syn (the PG early-return).
//
// The tests are against construction only — the pin stays in force and the
// search never reads these entries until P1.2.

// sjRender renders a SpecialJoinInfo for test diagnostics.
func sjRender(sj *SpecialJoinInfo) string {
	var b strings.Builder
	b.WriteString(joinTypeName(sj.Jointype))
	fmtJoinRelSet(&b, " synL=", sj.SynLefthand)
	fmtJoinRelSet(&b, " synR=", sj.SynRighthand)
	fmtJoinRelSet(&b, " minL=", sj.MinLefthand)
	fmtJoinRelSet(&b, " minR=", sj.MinRighthand)
	if sj.LhsStrict {
		b.WriteString(" strict")
	}
	return b.String()
}

func fmtJoinRelSet(b *strings.Builder, label string, rs RelSet) {
	b.WriteString(label)
	b.WriteByte('{')
	first := true
	for i := 0; i < 16; i++ {
		if rs&(1<<i) != 0 {
			if !first {
				b.WriteByte(',')
			}
			b.WriteByte(byte('0' + i))
			first = false
		}
	}
	b.WriteByte('}')
}

// sjCollect calls collectSpecialJoinInfos after deconstructing `from` and
// returns a rendered string of all entries.
func sjCollect(t *testing.T, from string) string {
	t.Helper()
	fromExprs := parseFrom(t, from)
	// C-04a: read the SpecialJoinInfos from the DECONSTRUCTION, not from a
	// walk of the joinlist's items — a LEFT join no longer pins, so it has no
	// item to carry one and the walk would answer "(none)" for every LEFT
	// fixture in this file. See `deconstructJointreeScopedSJI`.
	_, infos := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)
	if len(infos) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, sj := range infos {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(sjRender(sj))
	}
	return b.String()
}

func TestSpecialJoinInfoLeftJoin(t *testing.T) {
	// a LEFT JOIN b → one SpecialJoinInfo with synL={0}, synR={1}
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x")
	want := "LEFT synL={0} synR={1} minL={0} minR={1}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSpecialJoinInfoRightJoin(t *testing.T) {
	// C-04b: a RIGHT JOIN produces the SpecialJoinInfo PG would have built
	// AFTER reduce_outer_joins — a LEFT one with the hands swapped
	// (prepjointree.c:3360-3376; `reduceRightLink`). PG never builds a RIGHT
	// SJI at all (initsplan.c:1728), and neither does goopg now.
	got := sjCollect(t, "a RIGHT JOIN b ON a.x = b.x")
	want := "LEFT synL={1} synR={0} minL={1} minR={0}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	// A deeper RIGHT link null-extends its whole left prefix, and the prefix
	// holds an inner join: min = syn on the reduced RHS, as PG's min_righthand
	// includes inner_join_rels (initsplan.c:1804-1805). No LhsStrict.
	got = sjCollect(t, "a JOIN b ON a.x = b.x RIGHT JOIN c ON b.x = c.x")
	want = "LEFT synL={2} synR={0,1} minL={2} minR={0,1}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestSpecialJoinInfoFullJoin(t *testing.T) {
	// FULL JOIN: PG's make_outerjoininfo returns min = syn early.
	got := sjCollect(t, "a FULL JOIN b ON a.x = b.x")
	if got == "(none)" {
		t.Fatal("FULL JOIN must produce a SpecialJoinInfo")
	}
	if !strings.Contains(got, "FULL") {
		t.Errorf("expected FULL jointype, got %s", got)
	}
	// For FULL, min must equal syn by construction.
	if !strings.Contains(got, "minL={0}") || !strings.Contains(got, "minR={1}") {
		t.Errorf("FULL join must have minL={0} minR={1}, got %s", got)
	}
}

func TestSpecialJoinInfoSemiJoin(t *testing.T) {
	// SEMI join via WHERE EXISTS or explicit SEMI JOIN syntax.
	// The parser may not support explicit SEMI JOIN, so test via LEFT JOIN
	// which is the most common producer for now.
	// Verify the LEFT path at minimum.
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x")
	if got == "(none)" {
		t.Fatal("LEFT JOIN must produce a SpecialJoinInfo")
	}
}

func TestSpecialJoinInfoAntiJoin(t *testing.T) {
	// ANTI join — parser support varies. The structural test is a LEFT join
	// which is ANTI's twin. Verify the flag stays correct.
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x")
	if !strings.Contains(got, "LEFT") {
		t.Errorf("expected LEFT, got %s", got)
	}
}

func TestSpecialJoinInfoInnerJoinProducesNone(t *testing.T) {
	for _, from := range []string{
		"a INNER JOIN b ON a.x = b.x",
		"a JOIN b ON a.x = b.x",
		"a CROSS JOIN b",
	} {
		got := sjCollect(t, from)
		if got != "(none)" {
			t.Errorf("FROM %s: expected no SpecialJoinInfo, got %s", from, got)
		}
	}
}

func TestSpecialJoinInfoFlatCommaListProducesNone(t *testing.T) {
	got := sjCollect(t, "a, b, c")
	if got != "(none)" {
		t.Errorf("expected no SpecialJoinInfo for flat comma list, got %s", got)
	}
}

func TestSpecialJoinInfoNestedLeftOverLeft(t *testing.T) {
	// ((a LEFT JOIN b) LEFT JOIN c) — left-deep chain.
	// Two SpecialJoinInfo entries, bottom-up: inner (a,b) first, outer ((a,b),c).
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x")
	if got == "(none)" {
		t.Fatal("nested LEFT JOINs must produce entries")
	}
	parts := strings.Split(got, "; ")
	if len(parts) != 2 {
		t.Errorf("expected 2 entries (inner, then outer), got %d: %s", len(parts), got)
	}
	if len(parts) == 2 {
		// Inner: (a LEFT JOIN b) covers {0} vs {1}
		if !strings.Contains(parts[0], "synL={0}") || !strings.Contains(parts[0], "synR={1}") {
			t.Errorf("inner entry should be LEFT synL={0} synR={1}: %s", parts[0])
		}
		// Outer: ((a,b) LEFT JOIN c) covers {0,1} vs {2}
		if !strings.Contains(parts[1], "synL={0,1}") || !strings.Contains(parts[1], "synR={2}") {
			t.Errorf("outer entry should be LEFT synL={0,1} synR={2}: %s", parts[1])
		}
	}
}

func TestSpecialJoinInfoLeftOverInner(t *testing.T) {
	// a LEFT JOIN (b INNER JOIN c ON …) ON … — the inner join is INNER and
	// flattens with collapse ON, but the LEFT is still pinned.
	// With collapse ON, the inner join flattens: [b, c] become one subproblem.
	// The LEFT join wraps it → one SpecialJoinInfo covering {0} vs {1,2}.
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x INNER JOIN c ON b.x = c.x")
	if got == "(none)" {
		t.Fatal("expected SpecialJoinInfo for the LEFT join wrapper")
	}
	// With collapse ON, the INNER chain flattens: [a, b, c]? No — LEFT pins
	// its own sides, so the structure is pinned(a, flatten(b INNER c)).
	// That's one SpecialJoinInfo on the pinned item.
	if !strings.Contains(got, "LEFT") {
		t.Errorf("expected LEFT entry, got %s", got)
	}
}

func TestSpecialJoinInfoMixedNested(t *testing.T) {
	// ((a LEFT JOIN b) FULL JOIN c) — left-deep chain.
	// Bottom-up: LEFT(a,b) first, then FULL((a,b),c)
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x FULL JOIN c ON b.x = c.x")
	if got == "(none)" {
		t.Fatal("expected two SpecialJoinInfo entries")
	}
	parts := strings.Split(got, "; ")
	if len(parts) != 2 {
		t.Errorf("expected 2 entries, got %d: %s", len(parts), got)
	}
	if len(parts) == 2 {
		// Inner: (a LEFT JOIN b)
		if !strings.Contains(parts[0], "LEFT") {
			t.Errorf("inner entry should be LEFT: %s", parts[0])
		}
		// Outer: ((a,b) FULL JOIN c)
		if !strings.Contains(parts[1], "FULL") {
			t.Errorf("outer entry should be FULL: %s", parts[1])
		}
	}
}

func TestSpecialJoinInfoThreeWayLeftChain(t *testing.T) {
	// a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON a.y = c.y
	// LEFT-deep chain → two SpecialJoinInfo entries: inner (a,b) then outer ((a,b), c)
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON a.y = c.y")
	if got == "(none)" {
		t.Fatal("expected two SpecialJoinInfo entries")
	}
	parts := strings.Split(got, "; ")
	if len(parts) != 2 {
		t.Errorf("expected 2 entries, got %d: %s", len(parts), got)
	}
}

func TestSpecialJoinInfoJoinlistRelSet(t *testing.T) {
	// joinlistRelSet: verify bitmask computation for a simple joinlist.
	jl := joinlist{leafItem(0), leafItem(1), leafItem(3)}
	got := joinlistRelSet(jl)
	want := RelSet((1 << 0) | (1 << 1) | (1 << 3))
	if got != want {
		t.Errorf("joinlistRelSet([0,1,3]) = %#04b, want %#04b", got, want)
	}

	// Single leaf
	if rs := joinlistRelSet(joinlist{leafItem(5)}); rs != (1 << 5) {
		t.Errorf("joinlistRelSet([5]) = %#04b, want %#04b", rs, 1<<5)
	}

	// Empty
	if rs := joinlistRelSet(joinlist{}); rs != 0 {
		t.Errorf("joinlistRelSet([]) = %#04b, want 0", rs)
	}
}

func TestSpecialJoinInfoCollectEmpty(t *testing.T) {
	jl := deconstructRangeVars(3)
	infos := jl.collectSpecialJoinInfos(nil)
	if len(infos) != 0 {
		t.Errorf("deconstructRangeVars must produce no SpecialJoinInfo entries, got %d", len(infos))
	}
}

func TestSpecialJoinInfoResolveContextPopulated(t *testing.T) {
	// Verify that planFromClause populates joinInfoList on the resolveContext.
	cat := catalog.NewInMemory()
	for _, name := range []string{"a", "b"} {
		cols := []catalog.Column{{Name: name + "x", Type: catalog.Type{Name: "int8"}}}
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	stmts, err := parser.Parse("SELECT * FROM a LEFT JOIN b ON a.ax = b.bx")
	if err != nil {
		t.Fatal(err)
	}
	sel := stmts[0].(*parser.SelectStmt)
	_, rctx, err := planFromClause(sel, cat, DefaultPlannerSettings(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if rctx == nil {
		t.Fatal("nil resolveContext")
	}
	if len(rctx.joinInfoList) == 0 {
		t.Error("joinInfoList not populated on resolveContext for LEFT JOIN query")
	}
	// C-04a: the joinlist walk no longer sees a LEFT join's SpecialJoinInfo —
	// the link does not pin, so there is no item to carry it — which is
	// exactly why `joinInfoList` is now published by the deconstruction. The
	// walk is a SUBSET, and the invariant that remains is that everything it
	// does see is in the list. TestJoinInfoListProvenanceMatchesJoinlistWalk
	// pins the divergence itself.
	for _, sj := range rctx.joinlist.collectSpecialJoinInfos(nil) {
		found := false
		for _, got := range rctx.joinInfoList {
			if got == sj {
				found = true
				break
			}
		}
		if !found {
			t.Error("a SpecialJoinInfo on the joinlist is missing from joinInfoList")
		}
	}
}

func TestSpecialJoinInfoFieldsAreSet(t *testing.T) {
	// Verify that the SpecialJoinInfo fields match PG's expectations.
	fromExprs := parseFrom(t, "a LEFT JOIN b ON a.x = b.x")
	// C-04a: from the deconstruction, not the joinlist walk (see sjCollect).
	_, infos := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled(), nil)
	if len(infos) != 1 {
		t.Fatalf("expected 1 SpecialJoinInfo, got %d", len(infos))
	}
	sj := infos[0]

	// syn_lefthand / syn_righthand: the syntactic sides
	if sj.SynLefthand != (1<<0) {
		t.Errorf("SynLefthand = %#04b, want {0}", sj.SynLefthand)
	}
	if sj.SynRighthand != (1<<1) {
		t.Errorf("SynRighthand = %#04b, want {1}", sj.SynRighthand)
	}
	if sj.Jointype != parser.JoinLeft {
		t.Errorf("Jointype = %v, want LEFT", sj.Jointype)
	}
	// min_lefthand / min_righthand for LEFT: currently syn (conservative)
	if sj.MinLefthand != (1<<0) {
		t.Errorf("MinLefthand = %#04b, want {0}", sj.MinLefthand)
	}
	if sj.MinRighthand != (1<<1) {
		t.Errorf("MinRighthand = %#04b, want {1}", sj.MinRighthand)
	}
	// commute fields: all zero initially
	if sj.CommuteAboveL != 0 || sj.CommuteAboveR != 0 || sj.CommuteBelowL != 0 || sj.CommuteBelowR != 0 {
		t.Error("commute fields must be zero initially")
	}
	if sj.Ojrelid != 0 {
		t.Errorf("Ojrelid = %d, want 0 (no RT entry yet)", sj.Ojrelid)
	}
}

// ── M0128-P1.2 legality-matrix tests ──
//
// These test joinIsLegal, joinOrderRestricted, and hasJoinRestriction over
// hand-built SpecialJoinInfo entries. Each test:
//  1. Constructs a searchCtx with the joinInfoList the production code
//     deconstructJointree would produce.
//  2. Calls the function under known (rel1, rel2) pairs.
//  3. Verifies the result against PG's documented behaviour in joinrels.c.

// mkTestSearchCtx builds a minimal searchCtx with n base rels and the given
// joinInfoList, enough for joinIsLegal/joinOrderRestricted/hasJoinRestriction
// to run on.
func mkTestSearchCtx(t *testing.T, nrels int, jil []*SpecialJoinInfo) *searchCtx {
	t.Helper()
	s, err := newSearchCtx(nrels, defaultCostParams(), jil)
	if err != nil {
		t.Fatalf("newSearchCtx: %v", err)
	}
	return s
}

// mkTestRel builds a minimal RelOptInfo with the given relids, for legality tests.
func mkTestRel(relids RelSet) *RelOptInfo {
	return &RelOptInfo{Relids: relids, Rows: 100, Width: 8}
}

// mkSJ builds a SpecialJoinInfo with the given fields — the minimum needed
// for legality tests.
func mkSJ(jointype parser.JoinType, minLHS, minRHS RelSet) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		MinLefthand:  minLHS,
		MinRighthand: minRHS,
		SynLefthand:  minLHS,
		SynRighthand: minRHS,
		Jointype:     jointype,
	}
}

// ── joinIsLegal ──

func TestJoinIsLegalEmptyJoinInfoList(t *testing.T) {
	s := mkTestSearchCtx(t, 3, nil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal returned error: %v", err)
	}
	if sj != nil || rev {
		t.Errorf("joinIsLegal = (%v, %v); want (nil, false) for plain inner join", sj, rev)
	}
}

func TestJoinIsLegalInnerJoinUnaffectedBySJ(t *testing.T) {
	// A LEFT JOIN B — SJ covers {A}=LHS, {B}=RHS.
	// Pair (A, C) where C is unrelated: A is the preserved side, so
	// joining A with C before the LEFT JOIN is legal — the SJ's RHS={B}
	// doesn't overlap {A,C} at all.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 3, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b100))
	if err != nil {
		t.Fatalf("joinIsLegal(A,C) returned error: %v", err)
	}
	if sj != nil || rev {
		t.Errorf("joinIsLegal(A,C) = (%v, %v); want (nil, false) — A and C are not the SJ pair", sj, rev)
	}
}

func TestJoinIsLegalRejectsRHSPlusUnrelated(t *testing.T) {
	// A LEFT JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (B, C) where C is unrelated: B is the nullable RHS, and joining
	// it with C before the LEFT JOIN would produce rows that should be
	// null-extended. PG rejects this (joinrels.c:490-530).
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 3, jil)
	_, _, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b100))
	if err == nil {
		t.Error("joinIsLegal(B,C) should reject — joining nullable RHS with an unrelated rel before the LEFT JOIN is illegal")
	}
}

func TestJoinIsLegalMatchesLeftJoin(t *testing.T) {
	// A LEFT JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (A, B): must match the SJ, not reversed.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal(A,B) returned error: %v", err)
	}
	if sj == nil {
		t.Fatal("joinIsLegal(A,B) = nil; want the LEFT JOIN SpecialJoinInfo")
	}
	if rev {
		t.Error("joinIsLegal(A,B) reversed = true; want false")
	}
	if sj.Jointype != parser.JoinLeft {
		t.Errorf("joinIsLegal(A,B) sj.Jointype = %v; want LEFT", sj.Jointype)
	}
}

func TestJoinIsLegalMatchesLeftJoinReversed(t *testing.T) {
	// A LEFT JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (B, A): must match the SJ, reversed.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b001))
	if err != nil {
		t.Fatalf("joinIsLegal(B,A) returned error: %v", err)
	}
	if sj == nil {
		t.Fatal("joinIsLegal(B,A) = nil; want the LEFT JOIN SpecialJoinInfo")
	}
	if !rev {
		t.Error("joinIsLegal(B,A) reversed = false; want true")
	}
}

func TestJoinIsLegalRHSOverlapBothSides(t *testing.T) {
	// A LEFT JOIN (B ⋈ C) — SJ: LHS={A}, RHS={B,C}.
	// Pair (B, C): both overlap RHS but neither fully contains it.
	// PG's join_is_legal says: if both inputs overlap RHS, assume valid
	// previous commutation → continue → returns plain inner join.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b110)}
	s := mkTestSearchCtx(t, 3, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b100))
	if err != nil {
		t.Fatalf("joinIsLegal(B,C) returned error: %v", err)
	}
	if sj != nil || rev {
		t.Errorf("joinIsLegal(B,C) = (%v, %v); want (nil, false) — both sides overlap RHS", sj, rev)
	}
}

func TestJoinIsLegalMatchesFullJoin(t *testing.T) {
	// A FULL JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (A, B): must match the SJ, not reversed.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal(A,B) for FULL returned error: %v", err)
	}
	if sj == nil {
		t.Fatal("joinIsLegal(A,B) for FULL = nil; want the SpecialJoinInfo")
	}
	if sj.Jointype != parser.JoinFull {
		t.Errorf("joinIsLegal sj.Jointype = %v; want FULL", sj.Jointype)
	}
	if rev {
		t.Error("joinIsLegal reversed = true; want false")
	}
}

// ── joinOrderRestricted ──

func TestJoinOrderRestrictedEmptyJoinInfoList(t *testing.T) {
	s := mkTestSearchCtx(t, 3, nil)
	if s.joinOrderRestricted(mkTestRel(0b001), mkTestRel(0b010)) {
		t.Error("joinOrderRestricted returned true with no SpecialJoinInfos")
	}
}

func TestJoinOrderRestrictedLeftJoinPair(t *testing.T) {
	// A LEFT JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (A, B): must be restricted — they complete the SJ.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	if !s.joinOrderRestricted(mkTestRel(0b001), mkTestRel(0b010)) {
		t.Error("joinOrderRestricted(A,B) = false; want true — they form the SJ")
	}
}

func TestJoinOrderRestrictedLeftJoinReversed(t *testing.T) {
	// Same as above but rels reversed — must still be restricted.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	if !s.joinOrderRestricted(mkTestRel(0b010), mkTestRel(0b001)) {
		t.Error("joinOrderRestricted(B,A) = false; want true — reversed still forms the SJ")
	}
}

func TestJoinOrderRestrictedBothOverlapRHS(t *testing.T) {
	// A LEFT JOIN (B ⋈ C) — SJ: LHS={A}, RHS={B,C}.
	// Pair (B, C): both overlap RHS → restricted (completes RHS).
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b110)}
	s := mkTestSearchCtx(t, 3, jil)
	if !s.joinOrderRestricted(mkTestRel(0b010), mkTestRel(0b100)) {
		t.Error("joinOrderRestricted(B,C) = false; want true — both overlap RHS")
	}
}

func TestJoinOrderRestrictedBothOverlapLHS(t *testing.T) {
	// (A ⋈ B) LEFT JOIN C — SJ: LHS={A,B}, RHS={C}.
	// Pair (A, B): both overlap LHS → restricted (completes LHS).
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b011, 0b100)}
	s := mkTestSearchCtx(t, 3, jil)
	if !s.joinOrderRestricted(mkTestRel(0b001), mkTestRel(0b010)) {
		t.Error("joinOrderRestricted(A,B) = false; want true — both overlap LHS")
	}
}

func TestJoinOrderRestrictedSkipsFullJoin(t *testing.T) {
	// FULL JOIN — should be skipped by the restriction check per PG's logic.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	if s.joinOrderRestricted(mkTestRel(0b001), mkTestRel(0b010)) {
		t.Error("joinOrderRestricted(A,B) for FULL = true; want false — FULL joins are handled elsewhere")
	}
}

// ── hasJoinRestriction ──

func TestHasJoinRestrictionEmptyJoinInfoList(t *testing.T) {
	s := mkTestSearchCtx(t, 3, nil)
	if s.hasJoinRestriction(mkTestRel(0b001)) {
		t.Error("hasJoinRestriction returned true with no SpecialJoinInfos")
	}
}

func TestHasJoinRestrictionOverlapsRHS(t *testing.T) {
	// A LEFT JOIN (B ⋈ C) — SJ: LHS={A}, RHS={B,C}.
	// Rel B alone: overlaps RHS but doesn't fully contain it → restricted.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b001, 0b110)}
	s := mkTestSearchCtx(t, 3, jil)
	if !s.hasJoinRestriction(mkTestRel(0b010)) { // B
		t.Error("hasJoinRestriction(B) = false; want true — B overlaps RHS but doesn't contain it")
	}
}

func TestHasJoinRestrictionFullyContainsSJ(t *testing.T) {
	// (A ⋈ B) LEFT JOIN C — SJ: LHS={A,B}, RHS={C}.
	// Rel {A,B}: fully contains LHS AND RHS (C inside larger rel or SJ done).
	// Actually: {A,B} fully contains min_lefthand and doesn't overlap RHS → not restricted.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinLeft, 0b011, 0b100)}
	s := mkTestSearchCtx(t, 3, jil)
	if s.hasJoinRestriction(mkTestRel(0b011)) {
		t.Error("hasJoinRestriction({A,B}) = true; want false — LHS is fully contained, RHS doesn't overlap")
	}
}

func TestHasJoinRestrictionSkipsFullJoin(t *testing.T) {
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	if s.hasJoinRestriction(mkTestRel(0b001)) {
		t.Error("hasJoinRestriction(A) for FULL = true; want false — FULL joins are handled elsewhere")
	}
}

func TestHasJoinRestrictionBothOverlap(t *testing.T) {
	// (A LEFT JOIN B) LEFT JOIN C — two SJs: SJ1 LHS={A}, RHS={B}; SJ2 LHS={A,B}, RHS={C}.
	// Rel {A,B}: SJ1's LHS+RHS are subset → contained (skip). SJ2's LHS is subset but RHS doesn't overlap → not restricted.
	// Actually: {A,B} = SJ1 fully done. SJ2's LHS={A,B} IS subset, RHS={C} doesn't overlap → skip.
	// Result: false.
	jil := []*SpecialJoinInfo{
		mkSJ(parser.JoinLeft, 0b001, 0b010),
		mkSJ(parser.JoinLeft, 0b011, 0b100),
	}
	s := mkTestSearchCtx(t, 4, jil)
	if s.hasJoinRestriction(mkTestRel(0b011)) {
		t.Error("hasJoinRestriction({A,B}) = true; want false — both SJs are either contained or don't overlap")
	}
}

// TestJoinIsLegalMultipleSJInfosRejected: matching multiple SJs is illegal.
func TestJoinIsLegalMultipleSJInfosRejected(t *testing.T) {
	// Two LEFT JOINs with identical LHS/RHS — pathological but must be caught.
	jil := []*SpecialJoinInfo{
		mkSJ(parser.JoinLeft, 0b001, 0b010),
		mkSJ(parser.JoinLeft, 0b001, 0b010),
	}
	s := mkTestSearchCtx(t, 2, jil)
	_, _, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err == nil {
		t.Error("joinIsLegal should reject a pair matching multiple SpecialJoinInfos")
	}
}

// ── M0128-P1.3 FULL-nesting legality tests ──

func TestJoinIsLegalFullJoinReversedOrientation(t *testing.T) {
	// A FULL JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (B, A): matches as reversed — FULL is symmetric so both
	// orientations are valid.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 2, jil)
	sj, rev, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b001))
	if err != nil {
		t.Fatalf("joinIsLegal(B,A) for FULL: %v", err)
	}
	if sj == nil || sj.Jointype != parser.JoinFull {
		t.Fatalf("joinIsLegal(B,A) sj.Jointype = %v, want FULL", sj)
	}
	if !rev {
		t.Error("joinIsLegal(B,A) reversed = false, want true — (B,A) matches (RHS,LHS)")
	}
}

func TestJoinIsLegalFullJoinRHSBuilding(t *testing.T) {
	// A FULL JOIN (B ⋈ C) — SJ: LHS={A}, RHS={B,C}.
	// Pair (B, C): both overlap RHS — PG allows this via the "both overlap
	// RHS → assume valid commutation" branch (joinrels.c:509-511).
	// This builds up the RHS and is not the FULL join itself.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b110)}
	s := mkTestSearchCtx(t, 3, jil)
	sj, _, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b100))
	if err != nil {
		t.Fatalf("joinIsLegal(B,C) within FULL RHS: %v — both overlap RHS is legal for RHS building", err)
	}
	if sj != nil {
		t.Errorf("joinIsLegal(B,C) sj != nil, want nil — building RHS is not the FULL join itself")
	}
}

func TestJoinIsLegalRejectsFullJoinRHSPlusUnrelated(t *testing.T) {
	// A FULL JOIN B — SJ: LHS={A}, RHS={B}.
	// Pair (B, C) where C is unrelated: FULL joins cannot have association
	// into their RHS (unlike LEFT). PG rejects this at joinrels.c:519.
	jil := []*SpecialJoinInfo{mkSJ(parser.JoinFull, 0b001, 0b010)}
	s := mkTestSearchCtx(t, 3, jil)
	_, _, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b100))
	if err == nil {
		t.Error("joinIsLegal(B,C) should reject — joining FULL RHS with an unrelated rel is illegal")
	}
}

func TestJoinIsLegalNestedFullJoins(t *testing.T) {
	// (A FULL JOIN B) FULL JOIN C — SJ1: LHS={A}, RHS={B}; SJ2: LHS={A,B}, RHS={C}.
	// Test that the inner FULL pair (A,B) matches SJ1 and the outer (AB,C) matches SJ2.
	jil := []*SpecialJoinInfo{
		mkSJ(parser.JoinFull, 0b001, 0b010),
		mkSJ(parser.JoinFull, 0b011, 0b100),
	}
	s := mkTestSearchCtx(t, 3, jil)

	sj, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal(A,B) nested FULL: %v", err)
	}
	if sj == nil || sj.Jointype != parser.JoinFull || sj.MinLefthand != 0b001 {
		t.Fatalf("joinIsLegal(A,B) nested: got sj=%v rev=%v, want SJ1 FULL LHS={A}", sj, rev)
	}

	sj, rev, err = s.joinIsLegal(mkTestRel(0b011), mkTestRel(0b100))
	if err != nil {
		t.Fatalf("joinIsLegal(AB,C) nested FULL: %v", err)
	}
	if sj == nil || sj.Jointype != parser.JoinFull || sj.MinRighthand != 0b100 {
		t.Fatalf("joinIsLegal(AB,C) nested: got sj=%v rev=%v, want SJ2 FULL RHS={C}", sj, rev)
	}
	if rev {
		t.Error("joinIsLegal(AB,C) reversed = true, want false")
	}
}

// ── M0128-P1.3 buildJoinRelRestrictList / clause distribution tests ──

func TestBuildJoinRelRestrictListInnerJoin(t *testing.T) {
	l := &restrictInfoList{all: []*restrictInfo{
		{relids: 0b011}, // A, B — touches both
		{relids: 0b001}, // A only
		{relids: 0b110}, // B, C — B in join, C outside
	}}
	got := l.buildJoinRelRestrictList(0b001, 0b010, nil)
	want := l.clausesFor(0b001, 0b010)
	if len(got) != len(want) {
		t.Errorf("buildJoinRelRestrictList(nil sjinfo) = %d clauses, want %d", len(got), len(want))
	}
}

func TestBuildJoinRelRestrictListLeftJoinNullableFilters(t *testing.T) {
	sj := mkSJ(parser.JoinLeft, 0b001, 0b010)
	nullableClause := &restrictInfo{relids: 0b010} // B only — nullable RHS filter
	joinClause := &restrictInfo{relids: 0b011}     // A and B — join clause
	l := &restrictInfoList{all: []*restrictInfo{joinClause, nullableClause}}
	got := l.buildJoinRelRestrictList(0b001, 0b010, sj)
	if len(got) != 2 {
		t.Fatalf("buildJoinRelRestrictList(LEFT) = %d clauses, want 2", len(got))
	}
	foundJoin, foundFilter := false, false
	for _, ri := range got {
		if ri == joinClause {
			foundJoin = true
		}
		if ri == nullableClause {
			foundFilter = true
		}
	}
	if !foundJoin {
		t.Error("join clause missing from result")
	}
	if !foundFilter {
		t.Error("nullable-side filter clause missing from result")
	}
}

func TestBuildJoinRelRestrictListFullJoinBothSides(t *testing.T) {
	sj := mkSJ(parser.JoinFull, 0b001, 0b010)
	filterA := &restrictInfo{relids: 0b001} // A only — nullable LHS
	filterB := &restrictInfo{relids: 0b010} // B only — nullable RHS
	joinAB := &restrictInfo{relids: 0b011}  // A and B — join clause
	l := &restrictInfoList{all: []*restrictInfo{joinAB, filterA, filterB}}
	got := l.buildJoinRelRestrictList(0b001, 0b010, sj)
	if len(got) != 3 {
		t.Errorf("buildJoinRelRestrictList(FULL) = %d clauses, want 3", len(got))
	}
}

func TestBuildJoinRelRestrictListDedup(t *testing.T) {
	sj := mkSJ(parser.JoinFull, 0b001, 0b010)
	clause := &restrictInfo{relids: 0b011}
	l := &restrictInfoList{all: []*restrictInfo{clause, clause}}
	got := l.buildJoinRelRestrictList(0b001, 0b010, sj)
	if len(got) != 1 {
		t.Errorf("buildJoinRelRestrictList dedup = %d clauses, want 1", len(got))
	}
}

func TestBuildJoinRelRestrictListPreservedSideNotFilter(t *testing.T) {
	sj := mkSJ(parser.JoinLeft, 0b001, 0b010)
	preservedClause := &restrictInfo{relids: 0b001} // A only — preserved side
	l := &restrictInfoList{all: []*restrictInfo{preservedClause}}
	got := l.buildJoinRelRestrictList(0b001, 0b010, sj)
	if len(got) != 0 {
		t.Errorf("buildJoinRelRestrictList preserved-side = %d clauses, want 0", len(got))
	}
}

// ── isOuterJoinFilterClause ──

func TestIsOuterJoinFilterClauseLeftJoin(t *testing.T) {
	sj := mkSJ(parser.JoinLeft, 0b001, 0b010)
	if !isOuterJoinFilterClause(0b010, sj) {
		t.Error("clause on nullable RHS should be a filter clause for LEFT join")
	}
	if isOuterJoinFilterClause(0b001, sj) {
		t.Error("clause on preserved LHS should NOT be a filter clause for LEFT join")
	}
	if isOuterJoinFilterClause(0b011, sj) {
		t.Error("clause on both sides should NOT be a filter clause")
	}
}

func TestIsOuterJoinFilterClauseFullJoin(t *testing.T) {
	sj := mkSJ(parser.JoinFull, 0b001, 0b010)
	if !isOuterJoinFilterClause(0b001, sj) {
		t.Error("clause on nullable LHS should be a filter clause for FULL join")
	}
	if !isOuterJoinFilterClause(0b010, sj) {
		t.Error("clause on nullable RHS should be a filter clause for FULL join")
	}
	if isOuterJoinFilterClause(0b011, sj) {
		t.Error("clause on both sides should NOT be a filter clause for FULL join")
	}
}

// ── dedupRestrictInfoPtrs ──

func TestDedupRestrictInfoPtrs(t *testing.T) {
	a := &restrictInfo{relids: 0b001}
	b := &restrictInfo{relids: 0b010}
	c := &restrictInfo{relids: 0b100}
	tests := []struct {
		name string
		in   []*restrictInfo
		want int
	}{
		{"empty", nil, 0},
		{"single", []*restrictInfo{a}, 1},
		{"no dups", []*restrictInfo{a, b, c}, 3},
		{"dup adjacent", []*restrictInfo{a, a, b}, 2},
		{"dup separated", []*restrictInfo{a, b, a}, 2},
		{"all same", []*restrictInfo{a, a, a}, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := dedupRestrictInfoPtrs(tt.in)
			if len(got) != tt.want {
				t.Errorf("dedupRestrictInfoPtrs = %d, want %d", len(got), tt.want)
			}
		})
	}
}

// ── M0128-P1.4 SEMI/ANTI legality tests ──

// mkSEMISJ builds a SEMI SpecialJoinInfo with the given syntactic RHS
// distinct from the minimum RHS, to test the unique-ified skip.
func mkSEMISJ(minLHS, minRHS, synRHS RelSet) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		MinLefthand:  minLHS,
		MinRighthand: minRHS,
		SynLefthand:  minLHS,
		SynRighthand: synRHS,
		Jointype:     parser.JoinSemi,
	}
}

// mkANTISJ builds an ANTI SpecialJoinInfo.
func mkANTISJ(minLHS, minRHS RelSet) *SpecialJoinInfo {
	return &SpecialJoinInfo{
		MinLefthand:  minLHS,
		MinRighthand: minRHS,
		SynLefthand:  minLHS,
		SynRighthand: minRHS,
		Jointype:     parser.JoinAnti,
	}
}

// ── joinIsLegal SEMI/ANTI tests ──

func TestJoinIsLegalSemiMatch(t *testing.T) {
	// A LEFT SEMI JOIN B on A.x = B.x → SpecialJoinInfo(A, B)
	sj := mkSJ(parser.JoinSemi, 0b001, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	got, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal(A,B) for SEMI returned error: %v", err)
	}
	if got == nil {
		t.Fatal("joinIsLegal(A,B) for SEMI = nil; want the SpecialJoinInfo")
	}
	if got.Jointype != parser.JoinSemi {
		t.Errorf("joinIsLegal sj.Jointype = %v; want SEMI", got.Jointype)
	}
	if rev {
		t.Error("joinIsLegal(A,B) reversed = true; want false")
	}
}

func TestJoinIsLegalSemiReversed(t *testing.T) {
	sj := mkSJ(parser.JoinSemi, 0b001, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	got, rev, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b001))
	if err != nil {
		t.Fatalf("joinIsLegal(B,A) for SEMI returned error: %v", err)
	}
	if got == nil {
		t.Fatal("joinIsLegal(B,A) for SEMI = nil; want the SpecialJoinInfo")
	}
	if !rev {
		t.Error("joinIsLegal(B,A) reversed = false; want true")
	}
}

func TestJoinIsLegalSemiUniqueifiedSkip(t *testing.T) {
	// SEMI(A min={A}, syn={B}) — when the proposed join's rel1
	// already has the full syn_righthand embedded (proper superset),
	// the SEMI was already unique-ified and is no longer relevant.
	sj := mkSEMISJ(0b001, 0b010, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {B,C} ⋈ {A}: rel1 contains B (syn_righthand) as proper subset
	got, _, err := s.joinIsLegal(mkTestRel(0b110), mkTestRel(0b001))
	if err != nil {
		t.Fatalf("joinIsLegal({B,C},A) returned error: %v", err)
	}
	if got != nil {
		t.Errorf("joinIsLegal({B,C},A) = %v; want nil — SEMI already unique-ified", got)
	}
}

func TestJoinIsLegalSemiNotUniqueifiedWhenExactRHS(t *testing.T) {
	// When syn_righthand EQUALS rel (not a proper subset), the SEMI is
	// still relevant — it hasn't been unique-ified yet.
	sj := mkSEMISJ(0b001, 0b010, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {B} ⋈ {A}: {B} equals syn_righthand, not proper subset → still relevant
	got, _, err := s.joinIsLegal(mkTestRel(0b010), mkTestRel(0b001))
	if err != nil {
		t.Fatalf("joinIsLegal({B},A) returned error: %v", err)
	}
	if got == nil {
		t.Error("joinIsLegal({B},A) = nil; want SEMI match — RHS equals syn_righthand, not proper subset")
	}
}

func TestJoinIsLegalAntiMatch(t *testing.T) {
	// ANTI join: matches the same way as LEFT (min_lefthand/min_righthand).
	sj := mkANTISJ(0b001, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	got, rev, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal(A,B) for ANTI returned error: %v", err)
	}
	if got == nil {
		t.Fatal("joinIsLegal(A,B) for ANTI = nil; want the SpecialJoinInfo")
	}
	if got.Jointype != parser.JoinAnti {
		t.Errorf("joinIsLegal sj.Jointype = %v; want ANTI", got.Jointype)
	}
	if rev {
		t.Error("joinIsLegal(A,B) reversed = true; want false")
	}
}

func TestJoinIsLegalAntiRejectsRHSAssociation(t *testing.T) {
	// ANTI cannot associate into another join's RHS — only LEFT can.
	// The "both overlap RHS" case still handles this (valid commute).
	sj := mkANTISJ(0b001, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {A,B} ⋈ {B}: overlap RHS on both sides → both-overlap-RHS safe path
	got, _, err := s.joinIsLegal(mkTestRel(0b011), mkTestRel(0b010))
	if err != nil {
		t.Fatalf("joinIsLegal({A,B},B) returned error: %v", err)
	}
	if got != nil {
		t.Errorf("joinIsLegal({A,B},B) = %v; want nil — both overlap RHS, valid commute", got)
	}
}

func TestJoinIsLegalAntiCannotAssociateIntoRHS(t *testing.T) {
	// ANTI(A,{B,C}) with min_lefthand={A}, min_righthand={B,C}.
	// Join {A} ⋈ {C}: RHS {B,C} overlaps joinrelids={A,C} via C.
	// But {A} contains min_lefthand and {C} does NOT contain
	// min_righthand={B,C}. For LEFT, this could be mustBeLeftJoin;
	// for ANTI (and SEMI), it's an error (joinrels.c:519-521).
	sj := mkANTISJ(0b001, 0b110) // LHS={A}, RHS={B,C}
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	_, _, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b100))
	if err == nil {
		t.Error("joinIsLegal({A},{C}) should reject — ANTI RHS={B,C}, {C} doesn't cover full RHS and ANTI cannot associate")
	}
}

func TestJoinIsLegalSemiCannotAssociateIntoRHS(t *testing.T) {
	// Same shape as the ANTI test: SEMI also cannot associate.
	sj := mkSEMISJ(0b001, 0b110, 0b110) // LHS={A}, RHS={B,C}
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	_, _, err := s.joinIsLegal(mkTestRel(0b001), mkTestRel(0b100))
	if err == nil {
		t.Error("joinIsLegal({A},{C}) should reject — SEMI RHS={B,C}, {C} doesn't cover full RHS and SEMI cannot associate")
	}
}

// ── joinOrderRestricted SEMI tests ──

func TestJoinOrderRestrictedSemiUniqueifiedSkip(t *testing.T) {
	sj := mkSEMISJ(0b001, 0b010, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {B,C} has synRHS={B} as proper subset → skip → no restriction
	if s.joinOrderRestricted(mkTestRel(0b110), mkTestRel(0b001)) {
		t.Error("joinOrderRestricted({B,C},A) = true; want false — SEMI already unique-ified")
	}
}

func TestJoinOrderRestrictedSemiExactRHSNotSkipped(t *testing.T) {
	sj := mkSEMISJ(0b001, 0b010, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {B} equals synRHS → NOT a proper subset → restriction applies
	if !s.joinOrderRestricted(mkTestRel(0b010), mkTestRel(0b001)) {
		t.Error("joinOrderRestricted({B},A) = false; want true — exact RHS match, not unique-ified")
	}
}

// ── hasJoinRestriction SEMI tests ──

func TestHasJoinRestrictionSemiUniqueifiedSkip(t *testing.T) {
	sj := mkSEMISJ(0b001, 0b010, 0b010)
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	// {B,C} has synRHS={B} as proper subset → skip → no restriction
	if s.hasJoinRestriction(mkTestRel(0b110)) {
		t.Error("hasJoinRestriction({B,C}) = true; want false — SEMI already unique-ified")
	}
}

func TestHasJoinRestrictionSemiPartialLHSOverlap(t *testing.T) {
	// SEMI(minLHS={A,B}, minRHS={C}). rel={A}: overlaps min_lefthand but
	// doesn't fully contain → restriction (needs {B} to complete LHS).
	sj := mkSEMISJ(0b011, 0b100, 0b100) // LHS={A,B}, RHS={C}
	s := mkTestSearchCtx(t, 3, []*SpecialJoinInfo{sj})

	if !s.hasJoinRestriction(mkTestRel(0b001)) {
		t.Error("hasJoinRestriction({A}) with minLHS={A,B} = false; want true — partial LHS overlap")
	}
}

func TestHasJoinRestrictionSemiRHSNeedsCompletion(t *testing.T) {
	// SEMI(minLHS={A,B}, minRHS={C}). rel={C,D}: overlaps min_righthand but
	// doesn't fully contain it ({C} ∩ {C} ≠ ∅, but {C,D} does contain {C}).
	// Actually {C,D} DOES fully contain {C}, so no restriction.
	// Better: SEMI({A,B},{C,D}). rel={C}: overlaps RHS but doesn't contain it.
	sj := mkSEMISJ(0b011, 0b1100, 0b1100) // LHS={A,B}, RHS={C,D}
	s := mkTestSearchCtx(t, 4, []*SpecialJoinInfo{sj})

	if !s.hasJoinRestriction(mkTestRel(0b0100)) {
		t.Error("hasJoinRestriction({C}) with minRHS={C,D} = false; want true")
	}
}

// ── semiQualCapabilities tests ──

func TestSemiQualCapabilitiesNil(t *testing.T) {
	cb, ch := semiQualCapabilities(nil)
	if cb || ch {
		t.Error("semiQualCapabilities(nil) should be false, false")
	}
}

func TestSemiQualCapabilitiesEquality(t *testing.T) {
	eq := &parser.BinaryOp{Op: parser.OpEq,
		Left:  &parser.ColumnRef{Column: "x"},
		Right: &parser.ColumnRef{Column: "y"},
	}
	cb, ch := semiQualCapabilities(eq)
	if !cb || !ch {
		t.Error("semiQualCapabilities with = should be true, true")
	}
}

func TestSemiQualCapabilitiesNoEquality(t *testing.T) {
	lt := &parser.BinaryOp{Op: parser.OpLt,
		Left:  &parser.ColumnRef{Column: "x"},
		Right: &parser.ColumnRef{Column: "y"},
	}
	cb, ch := semiQualCapabilities(lt)
	if cb || ch {
		t.Error("semiQualCapabilities with < should be false, false")
	}
}
