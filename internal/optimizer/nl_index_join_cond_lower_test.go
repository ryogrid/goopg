package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// R48 Half 2 pins: a SEMI/ANTI NLI residual referencing only the inner
// side is lowered onto the probe as IndexScan.Cond (PG's inner-`Filter:`
// placement — distribute_restrictinfo_to_rels MOVEs the qual), while the
// join-level Predicate keeps whatever remains. The move runs BEFORE
// indexOnlyNLIInner, so a mixed residual provably stays an IndexScan
// (the IOS check sees Cond set and declines); the reverse order would
// drop the qual silently.

// condLowerFixture mirrors TestNLISemiAntiAcceptResidual's shape:
// 10 000 suppliers probing the 25-row nation PK (match set 1), the
// canonical selective semi/anti NLI shape (D6.3a: stats required).
func condLowerFixture(t *testing.T) (catalog.Catalog, *SeqScan, *SeqScan) {
	t.Helper()
	cat := catalog.NewInMemory()
	supplier, err := cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "s_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	nation, err := cat.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "n_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "nation_pk"}, nation,
		[]string{"n_nationkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	supplier.Stats = &catalog.TableStats{RowCount: 10000, Columns: []catalog.ColumnStats{
		{NDistinct: 10000}, {NDistinct: 25}, {NDistinct: 10000},
	}}
	nation.Stats = &catalog.TableStats{RowCount: 25, Columns: []catalog.ColumnStats{
		{NDistinct: 25}, {NDistinct: 25},
	}}
	outer := &SeqScan{Table: supplier, schema: tableSchemaWithSource(supplier, 1)}
	inner := &SeqScan{Table: nation, schema: tableSchemaWithSource(nation, 2)}
	return cat, outer, inner
}

// condLowerJoin builds the synthetic semi/anti/LEFT *Join every pin
// below feeds tryBuildNLI directly: equi keys on the nation PK plus an
// arbitrary residual Predicate.
func condLowerJoin(outer, inner *SeqScan, typ JoinType, residual Expr) *Join {
	leftWidth := len(outer.Output())
	return &Join{
		Type:      typ,
		Algo:      JoinAlgoHash,
		Left:      outer,
		Right:     inner,
		LeftKey:   &ColumnRef{Index: 1, Name: "s_nationkey", SourceTableIdx: 1},
		RightKey:  &ColumnRef{Index: leftWidth, Name: "n_nationkey", SourceTableIdx: 2},
		Predicate: residual,
		schema:    append(Schema(nil), outer.Output()...),
	}
}

// assertCondLeafLocal checks the Cond contract from plan.go's IndexScan
// doc: every ColumnRef addresses the scan's OWN output (leaf-local), by
// name as well as by range. A Cond that survived the -outerWidth shift
// with stale indices would silently filter on the wrong column.
func assertCondLeafLocal(t *testing.T, cond Expr, inner *IndexScan) {
	t.Helper()
	if cond == nil {
		t.Fatal("expected Cond to be set")
	}
	out := inner.Output()
	visitColumnRefs(cond, func(e Expr) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		if cr.Index < 0 || cr.Index >= len(out) {
			t.Fatalf("Cond ColumnRef %q has Index %d, out of range for the %d-column inner output",
				cr.Name, cr.Index, len(out))
		}
		if out[cr.Index].Name != cr.Name {
			t.Fatalf("Cond ColumnRef %q resolves to slot %d, which holds %q — not leaf-local",
				cr.Name, cr.Index, out[cr.Index].Name)
		}
	})
}

// refNames collects the ColumnRef names under e.
func refNames(e Expr) map[string]bool {
	names := map[string]bool{}
	visitColumnRefs(e, func(x Expr) {
		if cr, ok := x.(*ColumnRef); ok {
			names[cr.Name] = true
		}
	})
	return names
}

// TestNLISemiAntiLowersInnerOnlyResidualToCond: a residual referencing
// only the inner side moves to the probe; the join-level Predicate goes
// nil. The Q4 shape is the inner-only single-conjunct case.
func TestNLISemiAntiLowersInnerOnlyResidualToCond(t *testing.T) {
	for _, tc := range []struct {
		name string
		typ  JoinType
	}{{"semi", JoinTypeSemi}, {"anti", JoinTypeAnti}} {
		t.Run(tc.name, func(t *testing.T) {
			typ := tc.typ
			cat, outer, inner := condLowerFixture(t)
			leftWidth := len(outer.Output())
			residual := &BinaryOp{Op: parser.OpGt,
				Left:  &ColumnRef{Index: leftWidth + 1, Name: "n_name", SourceTableIdx: 2},
				Right: &ColumnRef{Index: 2, Name: "s_name", SourceTableIdx: 1},
			}
			// Rewrite as inner-only: compare the inner column to a
			// constant instead of an outer column.
			residual.Right = &IntegerConst{Value: 0}
			_ = leftWidth
			nli, ok := tryBuildNLI(condLowerJoin(outer, inner, typ, residual), cat)
			if !ok {
				t.Fatal("semi/anti join with an inner-only residual must become an NLI")
			}
			if nli.Predicate != nil {
				t.Fatalf("inner-only residual should leave no join Predicate; got %#v", nli.Predicate)
			}
			probe := nliIn(nli.Inner)
			if probe == nil {
				t.Fatalf("inner-only residual must stay an IndexScan (IOS declines on inner-reading residuals); inner is %T",
					nli.Inner)
			}
			assertCondLeafLocal(t, probe.Cond, probe)
			if names := refNames(probe.Cond); !names["n_name"] || len(names) != 1 {
				t.Fatalf("Cond should reference only n_name; refs=%v", names)
			}
		})
	}
}

// TestNLISemiKeepsOuterReferencingResidual: an outer-only conjunct stays
// on the join — it cannot evaluate per inner row. (The IOS promotion may
// fire on this shape; that pre-existing decision is not this round's.)
func TestNLISemiKeepsOuterReferencingResidual(t *testing.T) {
	cat, outer, inner := condLowerFixture(t)
	residual := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: 0, Name: "s_suppkey", SourceTableIdx: 1},
		Right: &IntegerConst{Value: 0},
	}
	nli, ok := tryBuildNLI(condLowerJoin(outer, inner, JoinTypeSemi, residual), cat)
	if !ok {
		t.Fatal("expected the NLI rewrite to fire")
	}
	if nli.Predicate == nil {
		t.Fatal("outer-only residual must stay on the join Predicate")
	}
	if names := refNames(nli.Predicate); !names["s_suppkey"] {
		t.Fatalf("join Predicate lost the outer conjunct; refs=%v", names)
	}
	switch in := nli.Inner.(type) {
	case *IndexScan:
		if in.Cond != nil {
			t.Fatalf("outer-only conjunct must not become probe Cond; got %#v", in.Cond)
		}
	case *IndexOnlyScan:
		// Pre-existing promotion on outer-only residuals; carries no Cond.
	default:
		t.Fatalf("unexpected inner kind %T", nli.Inner)
	}
}

// TestNLISemiMixedResidualReducesAndDeclinesIOS is the F2 order pin: a
// mixed residual splits (inner-only to Cond, outer-only stays), and the
// inner provably stays an *IndexScan — indexOnlyNLIInner saw Cond set
// and declined. Checking IOS first and setting Cond after would have
// promoted here and dropped the qual silently.
func TestNLISemiMixedResidualReducesAndDeclinesIOS(t *testing.T) {
	cat, outer, inner := condLowerFixture(t)
	leftWidth := len(outer.Output())
	innerOnly := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: leftWidth + 1, Name: "n_name", SourceTableIdx: 2},
		Right: &IntegerConst{Value: 0},
	}
	outerOnly := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: 0, Name: "s_suppkey", SourceTableIdx: 1},
		Right: &IntegerConst{Value: 0},
	}
	residual := &BinaryOp{Op: parser.OpAnd, Left: innerOnly, Right: outerOnly}
	nli, ok := tryBuildNLI(condLowerJoin(outer, inner, JoinTypeSemi, residual), cat)
	if !ok {
		t.Fatal("expected the NLI rewrite to fire")
	}
	probe := nliIn(nli.Inner)
	if probe == nil {
		t.Fatalf("mixed residual must stay an IndexScan (F2: IOS declines on Cond); inner is %T", nli.Inner)
	}
	assertCondLeafLocal(t, probe.Cond, probe)
	if names := refNames(probe.Cond); !names["n_name"] || names["s_suppkey"] {
		t.Fatalf("Cond should hold only the inner conjunct; refs=%v", names)
	}
	if nli.Predicate == nil {
		t.Fatal("outer conjunct must survive on the join Predicate")
	}
	if names := refNames(nli.Predicate); !names["s_suppkey"] || names["n_name"] {
		t.Fatalf("Predicate should hold only the outer conjunct; refs=%v", names)
	}
	assertResidualIndicesResolve(t, nli)
}

// TestNLILeftKeepsResidual: LEFT never lowers — a moved qual would stop
// filtering null-extended rows (wrong rows, not a perf question).
func TestNLILeftKeepsResidual(t *testing.T) {
	cat, outer, inner := condLowerFixture(t)
	leftWidth := len(outer.Output())
	residual := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: leftWidth + 1, Name: "n_name", SourceTableIdx: 2},
		Right: &IntegerConst{Value: 0},
	}
	nli, ok := tryBuildNLI(condLowerJoin(outer, inner, JoinTypeLeft, residual), cat)
	if !ok {
		t.Skipf("LEFT NLI did not fire on this shape (cost gate); the gate, not the lowering, owns that decision")
	}
	if nli.Predicate == nil {
		t.Fatal("LEFT residual must stay on the join Predicate")
	}
	probe := nliIn(nli.Inner)
	if probe == nil {
		t.Fatalf("expected a plain IndexScan inner; got %T", nli.Inner)
	}
	if probe.Cond != nil {
		t.Fatalf("LEFT must never lower to Cond; got %#v", probe.Cond)
	}
}

// TestNLISemiKeepsOutOfScopeResidual: an OuterColumnRef conjunct
// (correlation into a scope above the join) stays on the Predicate while
// the inner-only conjunct beside it still moves — split precision, fail
// closed on the unclassifiable half.
func TestNLISemiKeepsOutOfScopeResidual(t *testing.T) {
	cat, outer, inner := condLowerFixture(t)
	leftWidth := len(outer.Output())
	innerOnly := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: leftWidth + 1, Name: "n_name", SourceTableIdx: 2},
		Right: &IntegerConst{Value: 0},
	}
	correlated := &BinaryOp{Op: parser.OpGt,
		Left:  &OuterColumnRef{Level: 1, Index: 0, Name: "x"},
		Right: &IntegerConst{Value: 0},
	}
	residual := &BinaryOp{Op: parser.OpAnd, Left: innerOnly, Right: correlated}
	nli, ok := tryBuildNLI(condLowerJoin(outer, inner, JoinTypeSemi, residual), cat)
	if !ok {
		t.Fatal("expected the NLI rewrite to fire")
	}
	if nli.Predicate == nil {
		t.Fatal("correlated conjunct must survive on the join Predicate")
	}
	probe := nliIn(nli.Inner)
	if probe == nil {
		t.Fatalf("expected a plain IndexScan inner; got %T", nli.Inner)
	}
	assertCondLeafLocal(t, probe.Cond, probe)
	if names := refNames(probe.Cond); !names["n_name"] || len(names) != 1 {
		t.Fatalf("Cond should hold only the inner conjunct; refs=%v", names)
	}
}

// TestNLISemiHoistedInnerFilterLowersToCond is the Q4 shape end to end
// through the legacy rewrite: the EXISTS inner qual arrives as
// Filter{SeqScan}, is hoisted into the residual set, then lowered to the
// probe — leaving a bare semi join with a filtered probe.
func TestNLISemiHoistedInnerFilterLowersToCond(t *testing.T) {
	cat, outer, innerScan := condLowerFixture(t)
	leftWidth := len(outer.Output())
	filtered := &Filter{
		Child: innerScan,
		Predicate: &BinaryOp{Op: parser.OpGt,
			Left:  &ColumnRef{Index: 1, Name: "n_name", SourceTableIdx: 2},
			Right: &IntegerConst{Value: 0},
		},
	}
	j := condLowerJoin(outer, nil, JoinTypeSemi, nil)
	j.Right = filtered
	_ = leftWidth
	nli, ok := tryBuildNLI(j, cat)
	if !ok {
		t.Fatal("semi join over a Filter{SeqScan} inner must become an NLI (S6 unwrap)")
	}
	if nli.Predicate != nil {
		t.Fatalf("hoisted inner-only qual should leave no join Predicate; got %#v", nli.Predicate)
	}
	probe := nliIn(nli.Inner)
	if probe == nil {
		t.Fatalf("expected a plain IndexScan inner; got %T", nli.Inner)
	}
	assertCondLeafLocal(t, probe.Cond, probe)
}

// TestLowerSemiResidualToCondDirect pins the helper's decline paths:
// a non-bare probe (Cond already set) and a nil residual pass through
// untouched.
func TestLowerSemiResidualToCondDirect(t *testing.T) {
	_, _, innerScan := condLowerFixture(t)
	probe := &IndexScan{Table: innerScan.Table,
		schema: innerScan.Output(), Cond: &BooleanConst{Value: true}}
	residual := &BinaryOp{Op: parser.OpGt,
		Left:  &ColumnRef{Index: 1, Name: "n_name"},
		Right: &IntegerConst{Value: 0},
	}
	if got := lowerSemiResidualToCond(residual, probe, 3); got != Expr(residual) {
		t.Fatalf("non-bare probe must decline the move; residual changed to %#v", got)
	}
	if probe.Cond == nil {
		t.Fatal("non-bare probe's existing Cond must be preserved")
	}
	if got := lowerSemiResidualToCond(nil, probe, 3); got != nil {
		t.Fatalf("nil residual must stay nil; got %#v", got)
	}
}
