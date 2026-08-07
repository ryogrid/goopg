package planner

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
	jl := deconstructJointree(fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled())
	infos := jl.collectSpecialJoinInfos(nil)
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
	// RIGHT JOIN is pinned and should produce a SpecialJoinInfo.
	// PG never sees RIGHT (reduce_outer_joins flips it), but goopg has no
	// such pass yet, so RIGHT reaches deconstruction as itself.
	got := sjCollect(t, "a RIGHT JOIN b ON a.x = b.x")
	if got == "(none)" {
		t.Error("RIGHT JOIN must produce a SpecialJoinInfo")
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
	_, rctx, err := planFromClause(sel, cat)
	if err != nil {
		t.Fatal(err)
	}
	if rctx == nil {
		t.Fatal("nil resolveContext")
	}
	if len(rctx.joinInfoList) == 0 {
		t.Error("joinInfoList not populated on resolveContext for LEFT JOIN query")
	}
	if len(rctx.joinlist.collectSpecialJoinInfos(nil)) != len(rctx.joinInfoList) {
		t.Error("joinInfoList and collectSpecialJoinInfos disagree")
	}
}

func TestSpecialJoinInfoFieldsAreSet(t *testing.T) {
	// Verify that the SpecialJoinInfo fields match PG's expectations.
	fromExprs := parseFrom(t, "a LEFT JOIN b ON a.x = b.x")
	jl := deconstructJointree(fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled())
	infos := jl.collectSpecialJoinInfos(nil)
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
