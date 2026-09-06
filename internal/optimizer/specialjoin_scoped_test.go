package optimizer

// C-01 (P3-01) — SpecialJoinInfo population through the name → leaf scope.
//
// These tests pin the safety contract in
// docs/design/planner-c01-sji-population/DESIGN.md: min sets only SHRINK
// from syn on fully-resolved evidence; ANY uncertainty falls back to syn
// (never an underestimate); LhsStrict defaults to false.

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// scopedTestCatalog builds tables a/b/c each holding column x.
func scopedTestCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	for _, name := range []string{"a", "b", "c"} {
		cols := []catalog.Column{{Name: "x", Type: catalog.Type{Name: "int8"}}}
		if _, err := cat.CreateTable(parser.ObjectName{Name: name}, cols); err != nil {
			t.Fatalf("CreateTable(%s): %v", name, err)
		}
	}
	return cat
}

// scopedCollect deconstructs with the C-01 scope and renders all entries.
func scopedCollect(t *testing.T, from string, cat catalog.Catalog) string {
	t.Helper()
	fromExprs := parseFrom(t, from)
	sc := newSjiScope(fromExprs, cat)
	// C-04a: from the deconstruction, not the joinlist walk — see sjCollect.
	_, infos := deconstructJointreeScopedSJI(fromExprs, defaultCollapseLimits(), pgShapedCollapseEnabled(), sc)
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

func TestScopedLeftChainShrinksMin(t *testing.T) {
	// Second join: synL={0,1} but the qual mentions only {1,2}, so PG's
	// min_lefthand = clause ∩ synL = {1} — a genuine shrink.
	got := scopedCollect(t,
		"a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x",
		scopedTestCatalog(t))
	want := "LEFT synL={0} synR={1} minL={0} minR={1} strict; " +
		"LEFT synL={0,1} synR={2} minL={1} minR={2} strict"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScopedNilScopeKeepsLegacy(t *testing.T) {
	// Without a scope there is no resolver: min = syn (legacy behaviour,
	// unchanged by C-01).
	got := sjCollect(t, "a LEFT JOIN b ON a.x = b.x LEFT JOIN c ON b.x = c.x")
	want := "LEFT synL={0} synR={1} minL={0} minR={1}; " +
		"LEFT synL={0,1} synR={2} minL={0,1} minR={2}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScopedUnknownQualifierFallsBack(t *testing.T) {
	// Qualifier z matches no leaf: syn fallback, LhsStrict stays false.
	got := scopedCollect(t, "a LEFT JOIN b ON z.x = b.x", scopedTestCatalog(t))
	want := "LEFT synL={0} synR={1} minL={0} minR={1}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScopedUnqualifiedWithTablelessLeafFallsBack(t *testing.T) {
	// Unqualified ref with a subquery leaf in scope is inconclusive
	// (the subquery might hold the column): syn fallback.
	got := scopedCollect(t,
		"a LEFT JOIN (SELECT 1 AS x) s ON x = 1",
		scopedTestCatalog(t))
	want := "LEFT synL={0} synR={1} minL={0} minR={1}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScopedUnqualifiedResolvesWithCatalog(t *testing.T) {
	// Unqualified x, all leaves catalog-backed, exactly one holder (b):
	// resolves to leaf 1, so minR = {1} as with the qualified spelling.
	got := scopedCollect(t,
		"a LEFT JOIN b ON a.x = x",
		scopedTestCatalog(t))
	if !strings.Contains(got, "minR={1}") {
		t.Errorf("unqualified single-holder ref must resolve (minR={1}), got %s", got)
	}
}

func TestScopedFullKeepsSyn(t *testing.T) {
	// PG's make_outerjoininfo early-return: FULL min = syn by definition.
	got := scopedCollect(t,
		"a FULL JOIN b ON a.x = b.x",
		scopedTestCatalog(t))
	want := "FULL synL={0} synR={1} minL={0} minR={1}"
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestScopedSemiAntiFlags(t *testing.T) {
	qual := &parser.BinaryOp{Op: parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.ColumnRef{Table: "b", Column: "x"}}
	left := joinlist{leafItem(0)}
	right := joinlist{leafItem(1)}

	semi := makeSpecialJoinInfoScoped(parser.JoinSemi, left, right, qual, nil, 0, nil)
	if !semi.SemiCanBtree || !semi.SemiCanHash {
		t.Errorf("SEMI with equality qual: got btree=%v hash=%v, want true/true",
			semi.SemiCanBtree, semi.SemiCanHash)
	}
	anti := makeSpecialJoinInfoScoped(parser.JoinAnti, left, right, qual, nil, 0, nil)
	if anti.SemiCanBtree || anti.SemiCanHash {
		t.Errorf("ANTI must match upstream (false/false): got btree=%v hash=%v",
			anti.SemiCanBtree, anti.SemiCanHash)
	}
}

func TestScopedFullBarrierExpandsMin(t *testing.T) {
	// Lower FULL join is an optimisation barrier (initsplan.c:1829): a
	// current LEFT whose synL overlaps it absorbs the FULL's rels into minL.
	cat := scopedTestCatalog(t)
	fromExprs := parseFrom(t, "a CROSS JOIN b CROSS JOIN c")
	sc := newSjiScope(fromExprs, cat)
	lowerFull := makeSpecialJoinInfoScoped(parser.JoinFull,
		joinlist{leafItem(0)}, joinlist{leafItem(1)}, nil, sc, 0, nil)
	qual := &parser.BinaryOp{Op: parser.OpEq,
		Left:  &parser.ColumnRef{Table: "a", Column: "x"},
		Right: &parser.ColumnRef{Table: "c", Column: "x"}}
	sj := makeSpecialJoinInfoScoped(parser.JoinLeft,
		joinlist{leafItem(0)}, joinlist{leafItem(2)}, qual, sc, 0, []*SpecialJoinInfo{lowerFull})
	if sj.MinLefthand != 0b011 {
		t.Errorf("FULL barrier: minL = %b, want 011 (absorbed the FULL's rels)", sj.MinLefthand)
	}
	if sj.MinRighthand != 0b100 {
		t.Errorf("FULL barrier: minR = %b, want 100", sj.MinRighthand)
	}
}
