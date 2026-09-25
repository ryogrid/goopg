package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// Regression tests for the two ways `tryBuildNLI` used to lose
// information carried by the nodes it replaces. Both surfaced as
// silent wrong results on TPC-H Q7 (486,357 rows where PostgreSQL
// returns 4):
//
//   - the inner IndexScan was built without the FROM-clause Alias, so
//     a self-joined relation (`FROM nation n1, nation n2`) could no
//     longer be disambiguated downstream and `n1.*` bound to a
//     neighbouring relation's slots;
//   - any conjunct of `j.Predicate` that the index probe does not
//     enforce was dropped. The old code assumed pushdown had already
//     separated every non-key conjunct, which does not hold for a
//     predicate spanning two relations — pushdown cannot place it on
//     either scan, so it stays on the join.
//
// Q7's nation pair
// `(n1.n_name='FRANCE' AND n2.n_name='GERMANY') OR (…GERMANY…FRANCE…)`
// is exactly the second shape.

// nliResidualFixture builds `lineitem` (no index) + `part` (indexed on
// p_partkey), the canonical outer/inner pair used by the NLI tests.
func nliResidualFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	part, err := cat.CreateTable(parser.ObjectName{Name: "part"}, []catalog.Column{
		{Name: "p_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "p_size", Type: catalog.Type{Name: "int4"}},
		{Name: "p_name", Type: catalog.Type{Name: "varchar", Args: []int64{55}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "part_pk"}, part,
		[]string{"p_partkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateTable(parser.ObjectName{Name: "lineitem"}, []catalog.Column{
		{Name: "l_partkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "l_quantity", Type: catalog.Type{Name: "int4"}},
	}); err != nil {
		t.Fatal(err)
	}
	return cat
}

// nliSelfJoinFixture builds a single `nation` table with a PK index,
// so a query can join it to itself under two aliases — the shape whose
// duplicate column names defeat Name-only rebinding.
func nliSelfJoinFixture(t *testing.T) catalog.Catalog {
	t.Helper()
	cat := catalog.NewInMemory()
	nation, err := cat.CreateTable(parser.ObjectName{Name: "nation"}, []catalog.Column{
		{Name: "n_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "n_regionkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "n_name", Type: catalog.Type{Name: "varchar", Args: []int64{25}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := cat.CreateIndex(parser.ObjectName{Name: "nation_pk"}, nation,
		[]string{"n_nationkey"}, true, "btree", true); err != nil {
		t.Fatal(err)
	}
	return cat
}

// planNLI plans sql against cat and returns the first NLI, failing the
// test when the rewrite did not fire (every caller below depends on it).
func planNLI(t *testing.T, cat catalog.Catalog, sql string) *NestedLoopIndexJoin {
	t.Helper()
	node, err := Plan(parseOne(t, sql), cat)
	if err != nil {
		t.Fatalf("Plan(%s): %v", sql, err)
	}
	nli := firstNLI(node)
	if nli == nil {
		t.Fatalf("expected NLI for %s; tree: %s", sql, describePlanTree(node))
	}
	return nli
}

// assertResidualIndicesResolve checks that every ColumnRef in the NLI's
// residual Predicate points at a slot of `outer ++ inner` that actually
// holds the column it names. This is the assertion that catches a
// residual which survived the rewrite but kept stale indices: such a
// predicate is silently evaluated against the wrong columns, which
// filters every row out rather than raising an error.
func assertResidualIndicesResolve(t *testing.T, nli *NestedLoopIndexJoin) {
	t.Helper()
	joined := make(Schema, 0, len(nli.Outer.Output())+len(nliIn(nli.Inner).Output()))
	joined = append(joined, nli.Outer.Output()...)
	joined = append(joined, nliIn(nli.Inner).Output()...)
	visitColumnRefs(nli.Predicate, func(e Expr) {
		cr, ok := e.(*ColumnRef)
		if !ok || cr.Name == "" {
			return
		}
		if cr.Index < 0 || cr.Index >= len(joined) {
			t.Fatalf("residual ColumnRef %q has Index %d, out of range for the %d-column joined schema",
				cr.Name, cr.Index, len(joined))
		}
		got := joined[cr.Index]
		if got.Name != cr.Name {
			t.Fatalf("residual ColumnRef %q resolves to slot %d, which holds %q — stale index",
				cr.Name, cr.Index, got.Name)
		}
		if cr.SourceTableIdx != 0 && got.SourceTableIdx != cr.SourceTableIdx {
			t.Fatalf("residual ColumnRef %q (source %d) resolves to slot %d, whose source is %d — bound to the wrong relation",
				cr.Name, cr.SourceTableIdx, cr.Index, got.SourceTableIdx)
		}
	})
}

// TestNLIConsumedByProbe covers the helper directly, including the case
// that motivated pointer-identity matching: a second equality binding
// the SAME inner column to a DIFFERENT outer expression is not enforced
// by the probe and must survive as a residual. (Bushy DP's edge
// selection can leave that shape behind — only one edge wins.)
func TestNLIConsumedByProbe(t *testing.T) {
	idx := &catalog.Index{Name: "part_pk", Columns: []string{"p_partkey"}}
	probeKey := &ColumnRef{Index: 0, Name: "l_partkey"}
	otherKey := &ColumnRef{Index: 1, Name: "l_otherkey"}
	innerCol := func() *ColumnRef { return &ColumnRef{Index: 2, Name: "p_partkey"} }
	eq := func(l, r Expr) *BinaryOp { return &BinaryOp{Op: parser.OpEq, Left: l, Right: r} }

	cases := []struct {
		name string
		c    Expr
		want bool
	}{
		{"probe conjunct, inner on left", eq(innerCol(), probeKey), true},
		{"probe conjunct, inner on right", eq(probeKey, innerCol()), true},
		{"same inner column, different outer expr", eq(innerCol(), otherKey), false},
		{"non-equality on the index column", &BinaryOp{Op: parser.OpGt, Left: innerCol(), Right: probeKey}, false},
		{"equality on an unindexed column", eq(&ColumnRef{Index: 3, Name: "p_size"}, probeKey), false},
		{"not a binary op", probeKey, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := nliConsumedByProbe(tc.c, idx, []Expr{probeKey}); got != tc.want {
				t.Fatalf("nliConsumedByProbe = %v, want %v", got, tc.want)
			}
		})
	}
	if nliConsumedByProbe(eq(innerCol(), probeKey), nil, []Expr{probeKey}) {
		t.Fatal("a nil index must never report a conjunct as probe-enforced")
	}
}

// TestNLISemiAntiAcceptResidual pins the S6 (D6.2) reversal of the
// earlier Semi/Anti carve-out: a residual-bearing semi/anti join now
// becomes an NLI, carrying the residual on Predicate. Although semi/anti
// EMIT the outer schema only, the executor evaluates the residual through
// virtualOut, whose column mapping spans outer ++ inner regardless of the
// emit schema — so an inner-referencing residual is evaluable. The
// resolved indices must land in range of outer ++ inner and name the
// right columns.
func TestNLISemiAntiAcceptResidual(t *testing.T) {
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
	// D6.3a: semi/anti NLI now requires stats. 10 000 suppliers probing
	// the 25-row nation PK (match set 1) is the canonical selective shape.
	supplier.Stats = &catalog.TableStats{RowCount: 10000, Columns: []catalog.ColumnStats{
		{NDistinct: 10000}, {NDistinct: 25}, {NDistinct: 10000},
	}}
	nation.Stats = &catalog.TableStats{RowCount: 25, Columns: []catalog.ColumnStats{
		{NDistinct: 25}, {NDistinct: 25},
	}}

	outer := &SeqScan{Table: supplier, schema: tableSchemaWithSource(supplier, 1)}
	inner := &SeqScan{Table: nation, schema: tableSchemaWithSource(nation, 2)}
	leftWidth := len(outer.Output())

	// The residual reaches into the inner side (n_name), which a Semi
	// join does not project.
	innerName := &ColumnRef{Index: leftWidth + 1, Name: "n_name", SourceTableIdx: 2}
	outerName := &ColumnRef{Index: 2, Name: "s_name", SourceTableIdx: 1}
	residual := &BinaryOp{Op: parser.OpGt, Left: innerName, Right: outerName}

	for _, jt := range []struct {
		name string
		typ  JoinType
	}{{"semi", JoinTypeSemi}, {"anti", JoinTypeAnti}} {
		t.Run(jt.name, func(t *testing.T) {
			innerName.Index, outerName.Index = leftWidth+1, 2
			j := &Join{
				Type:      jt.typ,
				Algo:      JoinAlgoHash,
				Left:      outer,
				Right:     inner,
				LeftKey:   &ColumnRef{Index: 1, Name: "s_nationkey", SourceTableIdx: 1},
				RightKey:  &ColumnRef{Index: leftWidth, Name: "n_nationkey", SourceTableIdx: 2},
				Predicate: residual,
				schema:    append(Schema(nil), outer.Output()...),
			}
			nli, ok := tryBuildNLI(j, cat)
			if !ok {
				t.Fatalf("%s join with an inner-side residual must now become an NLI (S6/D6.2)", jt.name)
			}
			if nli.Type != jt.typ {
				t.Fatalf("join type not preserved: got %v want %v", nli.Type, jt.typ)
			}
			if nli.Predicate == nil {
				t.Fatalf("%s NLI lost its residual predicate", jt.name)
			}
			// Emit schema stays outer-only; the residual's indices must
			// resolve within outer ++ inner.
			if got, want := len(nli.Output()), len(outer.Output()); got != want {
				t.Fatalf("semi/anti NLI schema width = %d, want outer-only %d", got, want)
			}
			assertResidualIndicesResolve(t, nli)
		})
	}
}

// TestNLISemiDeclinesUnresolvableResidual keeps the compute-before-
// write-back property observable: a residual naming a column that exists
// on neither side must decline the rewrite AND leave the shared
// predicate's indices untouched.
func TestNLISemiDeclinesUnresolvableResidual(t *testing.T) {
	cat := catalog.NewInMemory()
	supplier, err := cat.CreateTable(parser.ObjectName{Name: "supplier"}, []catalog.Column{
		{Name: "s_suppkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
		{Name: "s_nationkey", Type: catalog.Type{Name: "int4"}, NotNull: true},
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
	// D6.3a: semi/anti NLI now requires stats. 10 000 suppliers probing
	// the 25-row nation PK (match set 1) is the canonical selective shape.
	supplier.Stats = &catalog.TableStats{RowCount: 10000, Columns: []catalog.ColumnStats{
		{NDistinct: 10000}, {NDistinct: 25}, {NDistinct: 10000},
	}}
	nation.Stats = &catalog.TableStats{RowCount: 25, Columns: []catalog.ColumnStats{
		{NDistinct: 25}, {NDistinct: 25},
	}}
	outer := &SeqScan{Table: supplier, schema: tableSchemaWithSource(supplier, 1)}
	inner := &SeqScan{Table: nation, schema: tableSchemaWithSource(nation, 2)}
	leftWidth := len(outer.Output())
	ghost := &ColumnRef{Index: leftWidth + 1, Name: "no_such_column", SourceTableIdx: 0}
	j := &Join{
		Type:      JoinTypeSemi,
		Algo:      JoinAlgoHash,
		Left:      outer,
		Right:     inner,
		LeftKey:   &ColumnRef{Index: 1, Name: "s_nationkey", SourceTableIdx: 1},
		RightKey:  &ColumnRef{Index: leftWidth, Name: "n_nationkey", SourceTableIdx: 2},
		Predicate: &BinaryOp{Op: parser.OpGt, Left: ghost, Right: &IntegerConst{Value: 0}},
		schema:    append(Schema(nil), outer.Output()...),
	}
	if nli, ok := tryBuildNLI(j, cat); ok {
		t.Fatalf("unresolvable residual must decline the rewrite; got %#v", nli)
	}
	if ghost.Index != leftWidth+1 {
		t.Fatalf("declined rewrite rebound the shared predicate: got %d want %d", ghost.Index, leftWidth+1)
	}
}
