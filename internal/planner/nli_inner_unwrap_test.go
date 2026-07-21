package planner

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// D6.3b: the INNER-join Filter{SeqScan}-inner NLI unwrap is re-enabled
// behind innerUnwrapCostAccepts, resolving the csq-S2/S3 deferral-ledger
// row. The canonical must-decline is the Q9 shape that regressed 115 s →
// DNF when the unwrap shipped unconditionally: a ~6 M-row outer probing a
// 200 K-row part table with a `%green%` LIKE evaluated per probe.

// innerUnwrapFixture builds an outer/inner pair with an index on the
// inner join key and the given stats. ND on the inner key column controls
// the match set.
func innerUnwrapFixture(t *testing.T, outerRows, innerRows, innerKeyND int64) (Node, *SeqScan, *catalog.Index) {
	t.Helper()
	c := catalog.NewInMemory()
	outerTbl, err := c.CreateTable(parser.ObjectName{Name: "outer_u"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	innerTbl, err := c.CreateTable(parser.ObjectName{Name: "inner_u"}, []catalog.Column{
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
		{Name: "i_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	idx, err := c.CreateIndex(parser.ObjectName{Name: "inner_u_key_idx"}, innerTbl,
		[]string{"i_key"}, false, "btree", false)
	if err != nil {
		t.Fatal(err)
	}
	if outerRows > 0 {
		outerTbl.Stats = &catalog.TableStats{RowCount: outerRows,
			Columns: []catalog.ColumnStats{{NDistinct: outerRows}}}
	}
	if innerRows > 0 {
		innerTbl.Stats = &catalog.TableStats{RowCount: innerRows,
			Columns: []catalog.ColumnStats{{NDistinct: innerKeyND}, {NDistinct: innerRows}}}
	}
	outer := &SeqScan{Table: outerTbl, schema: Schema{{Name: "o_key"}}}
	inner := &SeqScan{Table: innerTbl, schema: Schema{{Name: "i_key"}, {Name: "i_name"}}}
	return outer, inner, idx
}

func likeConjunct() Expr {
	return &BinaryOp{Op: parser.OpLike,
		Left:  &ColumnRef{Name: "i_name", Index: 1},
		Right: &StringConst{Value: "%green%"}}
}

func plainConjunct() Expr {
	return &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Name: "i_key", Index: 0},
		Right: &IntegerConst{Value: 5}}
}

// TestInnerUnwrapCostTable pins the accept formula
// outerRows×(matchSet+residualMult) < innerRows+outerRows and the
// no-stats DECLINE default (the asymmetry with semi/anti's optimistic
// default is pinned separately below).
func TestInnerUnwrapCostTable(t *testing.T) {
	cases := []struct {
		name                             string
		outerRows, innerRows, innerKeyND int64
		mult                             int64
		want                             bool
	}{
		// Q9 shape: 6M outer × (matchSet 1 + LIKE surcharge 8) = 54M
		// > 200K + 6M → decline.
		{"q9-shape-declines", 6000000, 200000, 200000, 8, false},
		// Selective outer, cheap residual: 10K × (1+1) = 20K <
		// 200K + 10K → accept.
		{"selective-outer-accepts", 10000, 200000, 200000, 1, true},
		// Fat match set overwhelms even a small outer: 1000 × (600+1)
		// = 601K > 6K + 1K → decline.
		{"fat-matchset-declines", 1000, 6000, 10, 1, false},
		// No stats at all → decline (status quo hash; see the
		// asymmetry note on innerUnwrapCostAccepts).
		{"no-stats-declines", 0, 0, 0, 1, false},
		{"no-inner-stats-declines", 10000, 0, 0, 1, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outer, inner, idx := innerUnwrapFixture(t, tc.outerRows, tc.innerRows, tc.innerKeyND)
			got := innerUnwrapCostAccepts(outer, inner, idx, tc.mult)
			if got != tc.want {
				t.Fatalf("innerUnwrapCostAccepts = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestResidualCostMultiplier pins the surcharge classification.
func TestResidualCostMultiplier(t *testing.T) {
	if got := residualCostMultiplier([]Expr{plainConjunct()}); got != 1 {
		t.Fatalf("plain comparison multiplier = %d, want 1", got)
	}
	if got := residualCostMultiplier([]Expr{plainConjunct(), likeConjunct()}); got != 8 {
		t.Fatalf("LIKE multiplier = %d, want 8", got)
	}
	fc := &BinaryOp{Op: parser.OpGt,
		Left:  &FuncCall{Name: "length", Args: []Expr{&ColumnRef{Name: "i_name", Index: 1}}},
		Right: &IntegerConst{Value: 3}}
	if got := residualCostMultiplier([]Expr{fc}); got != 8 {
		t.Fatalf("FuncCall multiplier = %d, want 8", got)
	}
}

// TestNoStatsAsymmetry pins, side by side, the two documented no-stats
// defaults: semi/anti NLI accepts optimistically (R2-3 — rejecting would
// permanently disable a shape whose measured upside is 71×), while the
// INNER unwrap declines (R2-6 — declining keeps today's healthy hash
// behavior; a wrong accept is the Q9-DNF direction).
func TestNoStatsAsymmetry(t *testing.T) {
	outer, inner, idx := innerUnwrapFixture(t, 0, 0, 0)
	if !nliCostGateAccepts(JoinTypeSemi, outer, inner, idx) {
		t.Fatal("no-stats SEMI gate must accept optimistically (R2-3 rule)")
	}
	if !nliCostGateAccepts(JoinTypeAnti, outer, inner, idx) {
		t.Fatal("no-stats ANTI gate must accept optimistically (R2-3 rule)")
	}
	if innerUnwrapCostAccepts(outer, inner, idx, 1) {
		t.Fatal("no-stats INNER unwrap must decline (R2-6 rule)")
	}
}

// unwrapCatalog returns a catalog holding the fixture's index so
// tryBuildNLI can resolve it. innerUnwrapFixture already created the
// tables/index inside its own catalog; rebuild the same layout here.
func unwrapCatalogFor(t *testing.T, outerRows, innerRows, innerKeyND int64, conjunct Expr) (*Join, catalog.Catalog) {
	t.Helper()
	c := catalog.NewInMemory()
	outerTbl, err := c.CreateTable(parser.ObjectName{Name: "outer_u"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int4"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	innerTbl, err := c.CreateTable(parser.ObjectName{Name: "inner_u"}, []catalog.Column{
		{Name: "i_key", Type: catalog.Type{Name: "int4"}},
		{Name: "i_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateIndex(parser.ObjectName{Name: "inner_u_key_idx"}, innerTbl,
		[]string{"i_key"}, false, "btree", false); err != nil {
		t.Fatal(err)
	}
	if outerRows > 0 {
		outerTbl.Stats = &catalog.TableStats{RowCount: outerRows,
			Columns: []catalog.ColumnStats{{NDistinct: outerRows}}}
	}
	if innerRows > 0 {
		innerTbl.Stats = &catalog.TableStats{RowCount: innerRows,
			Columns: []catalog.ColumnStats{{NDistinct: innerKeyND}, {NDistinct: innerRows}}}
	}
	outer := &SeqScan{Table: outerTbl, schema: Schema{{Name: "o_key"}}}
	inner := &SeqScan{Table: innerTbl, schema: Schema{{Name: "i_key"}, {Name: "i_name"}}}
	j := &Join{
		Type: JoinTypeInner,
		Algo: JoinAlgoHash,
		Left: outer,
		Right: &Filter{
			Predicate: conjunct,
			Child:     inner,
		},
		LeftKey:  &ColumnRef{Name: "o_key", Index: 0},
		RightKey: &ColumnRef{Name: "i_key", Index: 1},
	}
	return j, c
}

// innerPlainConjunct references i_key (idx 0) in inner-relative coords.
func innerPlainConjunct() Expr {
	return &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Name: "i_key", Index: 0},
		Right: &IntegerConst{Value: 5}}
}

// innerLikeConjunct references i_name (idx 1) in inner-relative coords.
func innerLikeConjunct() Expr {
	return &BinaryOp{Op: parser.OpLike,
		Left:  &ColumnRef{Name: "i_name", Index: 1},
		Right: &StringConst{Value: "%green%"}}
}

// TestInnerUnwrapAccepts: a selective outer with a plain-comparison inner
// Filter unwraps to an NLI whose residual carries the hoisted conjunct
// with resolvable indices.
func TestInnerUnwrapAccepts(t *testing.T) {
	j, c := unwrapCatalogFor(t, 10000, 200000, 200000, innerPlainConjunct())
	nli, ok := tryBuildNLI(j, c)
	if !ok {
		t.Fatal("selective-outer INNER Filter-inner unwrap must accept")
	}
	found := false
	visitColumnRefs(nli.Predicate, func(e Expr) {
		if cr, okc := e.(*ColumnRef); okc && cr.Name == "i_key" && cr.Index >= 1 {
			found = true
		}
	})
	if !found {
		t.Fatalf("hoisted conjunct missing from NLI residual: %v", nli.Predicate)
	}
	assertResidualIndicesResolve(t, nli)
}

// TestInnerUnwrapQ9Declines: the Q9 shape (huge outer, LIKE residual)
// declines and restores the Filter inner untouched — hash keeps serving.
func TestInnerUnwrapQ9Declines(t *testing.T) {
	j, c := unwrapCatalogFor(t, 6000000, 200000, 200000, innerLikeConjunct())
	if _, ok := tryBuildNLI(j, c); ok {
		t.Fatal("Q9-shape INNER unwrap must decline")
	}
	if _, stillFilter := j.Right.(*Filter); !stillFilter {
		t.Fatal("declined unwrap must restore j.Right to the Filter")
	}
}

// TestInnerUnwrapNoStatsDeclines: without stats the unwrap declines
// (status quo hash), restoring the Filter inner.
func TestInnerUnwrapNoStatsDeclines(t *testing.T) {
	j, c := unwrapCatalogFor(t, 0, 0, 0, innerPlainConjunct())
	if _, ok := tryBuildNLI(j, c); ok {
		t.Fatal("no-stats INNER unwrap must decline")
	}
	if _, stillFilter := j.Right.(*Filter); !stillFilter {
		t.Fatal("declined unwrap must restore j.Right to the Filter")
	}
}

// TestLeftFilterInnerNeverUnwraps: LEFT joins with a Filter inner keep
// the hash path entirely (null-pad residual hazard, csq-S6 ledger row).
func TestLeftFilterInnerNeverUnwraps(t *testing.T) {
	outer, inner, _ := innerUnwrapFixture(t, 100, 100, 100)
	j := &Join{
		Type: JoinTypeLeft,
		Algo: JoinAlgoHash,
		Left: outer,
		Right: &Filter{
			Predicate: plainConjunct(),
			Child:     inner,
		},
		LeftKey:  &ColumnRef{Name: "o_key", Index: 0},
		RightKey: &ColumnRef{Name: "i_key", Index: 1},
	}
	c := catalog.NewInMemory()
	if _, ok := tryBuildNLI(j, c); ok {
		t.Fatal("LEFT join with a Filter inner must not build an NLI")
	}
	if _, stillFilter := j.Right.(*Filter); !stillFilter {
		t.Fatal("declined LEFT unwrap must leave j.Right untouched")
	}
}
