package planner

// M0125-0002 commit 6 pins for the leaf-local PAIR —
// conjunctIsLocalEligible (producer) and localizeExprToLeaf
// (consumer), stated per kind.
//
// Why one commit and one pin file: partitionConjunctsForJoinPlanning
// MOVES an eligible conjunct out of joinConjuncts into
// locals.byBinding, and attachRelationLocalFilters is the only thing
// that puts it back into the plan. So the producer's admission is a
// promise the consumer can rebase it. Both were incomplete in the SAME
// fail-open direction:
//
//   - conjunctIsLocalEligible enumerated 9 of 32 kinds with no default,
//     so a conjunct whose SubqueryExpr / ExistsExpr / OuterColumnRef sat
//     under an unenumerated container produced zero callbacks below that
//     container and returned a VACUOUS true;
//   - localizeExprToLeaf enumerated 7 of 32 and ended in a pass-through
//     commented "Constants … no ColumnRef; pass through" — true of the
//     seven it knew, false of the other twenty-five. An IsNullExpr or
//     CastExpr wrapping a ColumnRef was returned UNCHANGED, i.e. handed
//     to a leaf Filter still carrying FROM-cumulative indices. With
//     binding.offset > 0 that reads the WRONG COLUMN at execution time.
//
// The two holes overlap almost exactly, which is why the defect stayed
// latent: the producer usually declined what the consumer could not
// rebase. "Usually" is the whole problem — the sets were never equal,
// and after commit 4 completed tableForCol they drifted further, since
// a conjunct like `t.a IS NULL` newly attributes to a binding and so
// newly reaches this pair.
//
// Three behaviours are pinned:
//
//   - newly DECLINED conjuncts (the producer's fail-open closed): a
//     subquery or outer ref under any container the old switch skipped;
//   - newly REBASED containers (the consumer's pass-through closed);
//   - the pair invariant itself, over all 32 Expr kinds: whatever the
//     producer admits, the consumer rebases without aborting.
//
// Scope policy is scopeVeto on both sides. A decline here costs an
// optimisation (the conjunct stays in the join residual and is
// evaluated above the join), never a wrong answer — so unlike commits
// 3/4 the producer does NOT panic on an unenumerated type. The
// consumer's panic is a different claim: by the time it runs, the
// producer has already accepted the conjunct, so an abort means the
// pair has diverged and the predicate would otherwise be silently
// dropped or left un-rebased.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// planned returns a subquery-bearing node with a real inner Plan.
func plannedSubq() *SubqueryExpr {
	return &SubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}}
}

// ---------------------------------------------------------------------
// Producer: kinds the old 9-arm switch fell through, so a disqualifying
// node underneath them was never seen. Each subtest FAILED against the
// old switch (the conjunct read as eligible and was pushed to a leaf).
// One case per container — covering IsNullExpr proves nothing about
// CollateExpr, which is exactly how the hole survived.
// ---------------------------------------------------------------------

func TestConjunctEligible_DisqualifierUnderNewlyWalkedContainer(t *testing.T) {
	type tc struct {
		name string
		wrap func(Expr) Expr
	}
	wrappers := []tc{
		{"IsNullExpr", func(e Expr) Expr { return &IsNullExpr{Operand: e} }},
		{"IsBoolExpr", func(e Expr) Expr { return &IsBoolExpr{Operand: e} }},
		{"CollateExpr", func(e Expr) Expr { return &CollateExpr{Operand: e} }},
		{"CastExpr", func(e Expr) Expr { return &CastExpr{Operand: e} }},
		{"RowExpr", func(e Expr) Expr { return &RowExpr{Elems: []Expr{&IntegerConst{Value: 1}, e}} }},
		{"IsDistinctFromExpr", func(e Expr) Expr {
			return &IsDistinctFromExpr{Left: &IntegerConst{Value: 1}, Right: e}
		}},
		{"CaseExpr/Else", func(e Expr) Expr {
			return &CaseExpr{
				Whens: []CaseWhen{{When: &BooleanConst{Value: true}, Then: &IntegerConst{Value: 1}}},
				Else:  e,
			}
		}},
	}
	// Every disqualifier the old top-level arms knew, now reachable
	// through a container.
	disq := map[string]func() Expr{
		"SubqueryExpr":   func() Expr { return plannedSubq() },
		"ExistsExpr":     func() Expr { return &ExistsExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}} },
		"OuterColumnRef": func() Expr { return &OuterColumnRef{} },
		"InExpr+Plan": func() Expr {
			return &InExpr{Operand: &ColumnRef{Index: 0, Name: "a"},
				Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}}
		},
	}

	for _, w := range wrappers {
		for dname, mk := range disq {
			t.Run(w.name+"/"+dname, func(t *testing.T) {
				e := &BinaryOp{Op: parser.OpEq,
					Left:  w.wrap(mk()),
					Right: &IntegerConst{Value: 1}}
				if conjunctIsLocalEligible(e) {
					t.Errorf("%s wrapping a %s must be INELIGIBLE — the conjunct "+
						"carries an execution-time dependency the leaf attachment "+
						"cannot honour (design 01 §3.1.3)", w.name, dname)
				}
			})
		}
	}
}

// The three subquery-bearing kinds the old switch never named at all.
// They are declined whether or not Plan is set: exprChildSlots emits the
// slotInnerPlan slot only when Plan != nil, so scopeVeto alone would
// admit the unplanned form, and the old code declined SubqueryExpr /
// ExistsExpr unconditionally.
func TestConjunctEligible_ArrayAndMultiAssignSubqueriesDeclined(t *testing.T) {
	plan := &SeqScan{Table: &catalog.Table{Name: "x"}}
	row := &MultiAssignSubqRow{Plan: plan}
	cases := map[string]Expr{
		"ArraySubqueryExpr":         &ArraySubqueryExpr{Plan: plan},
		"ArraySubqueryExpr/no plan": &ArraySubqueryExpr{},
		"MultiAssignSubqRow":        row,
		"MultiAssignSubqRow/no plan": &MultiAssignSubqRow{},
		"MultiAssignSubqElem":       &MultiAssignSubqElem{Row: row},
		"MultiAssignSubqElem/no row": &MultiAssignSubqElem{},
		"SubqueryExpr/no plan":      &SubqueryExpr{},
		"ExistsExpr/no plan":        &ExistsExpr{},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if conjunctIsLocalEligible(e) {
				t.Errorf("%s must be INELIGIBLE for leaf-local attachment", name)
			}
		})
	}
}

// Fail CLOSED, not open, and not a panic: an Expr type exprChildSlots
// has never been taught declines the conjunct rather than admitting it
// on a vacuous true.
func TestConjunctEligible_UnenumeratedTypeDeclines(t *testing.T) {
	cases := map[string]Expr{
		"root": &unknownExpr{},
		"nested": &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "a"},
			Right: &unknownExpr{}},
	}
	for name, e := range cases {
		t.Run(name, func(t *testing.T) {
			if conjunctIsLocalEligible(e) {
				t.Error("an unenumerated Expr type must make the conjunct INELIGIBLE " +
					"(fail closed); the old switch had no default and admitted it")
			}
		})
	}
}

// ---------------------------------------------------------------------
// Producer: the old declines and the old admissions both survive. The
// conversion is monotone — it may only REMOVE admissions — so anything
// eligible before must still be eligible.
// ---------------------------------------------------------------------

func TestConjunctEligible_PreservedArms(t *testing.T) {
	col := func() *ColumnRef { return &ColumnRef{Index: 0, Name: "a"} }

	declined := map[string]Expr{
		"OuterColumnRef":        &OuterColumnRef{},
		"SubqueryExpr":          plannedSubq(),
		"ExistsExpr":            &ExistsExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}},
		"InExpr with Plan":      &InExpr{Operand: col(), Plan: &SeqScan{Table: &catalog.Table{Name: "x"}}},
		"BinaryOp/nested subq":  &BinaryOp{Op: parser.OpGt, Left: col(), Right: plannedSubq()},
		"FuncCall/nested outer": &FuncCall{Name: "abs", Args: []Expr{&OuterColumnRef{}}},
	}
	for name, e := range declined {
		t.Run("declined/"+name, func(t *testing.T) {
			if conjunctIsLocalEligible(e) {
				t.Errorf("%s must stay INELIGIBLE", name)
			}
		})
	}

	admitted := map[string]Expr{
		"a = 5":            &BinaryOp{Op: parser.OpEq, Left: col(), Right: &IntegerConst{Value: 5}},
		"-a":               &UnaryOp{Operand: col()},
		"abs(a)":           &FuncCall{Name: "abs", Args: []Expr{col()}},
		"extract(a)":       &ExtractExpr{Source: col()},
		"a IN (1, 2)":      &InExpr{Operand: col(), List: []Expr{&IntegerConst{Value: 1}, &IntegerConst{Value: 2}}},
		"CASE WHEN a…":     &CaseExpr{Whens: []CaseWhen{{When: col(), Then: &IntegerConst{Value: 1}}}},
		"a = $1":           &BinaryOp{Op: parser.OpEq, Left: col(), Right: &ParamRef{}},
		"a IS NULL":        &IsNullExpr{Operand: col()},
		"CAST(a AS int)":   &CastExpr{Operand: col()},
		"a IS DISTINCT…":   &IsDistinctFromExpr{Left: col(), Right: &IntegerConst{Value: 1}},
		"ROW(a, 1)":        &RowExpr{Elems: []Expr{col(), &IntegerConst{Value: 1}}},
		"a COLLATE \"C\"":  &CollateExpr{Operand: col()},
		"a IS TRUE":        &IsBoolExpr{Operand: col()},
	}
	for name, e := range admitted {
		t.Run("admitted/"+name, func(t *testing.T) {
			if !conjunctIsLocalEligible(e) {
				t.Errorf("%s must stay ELIGIBLE — the conversion may only remove "+
					"admissions that were unsafe, never decline ordinary predicates", name)
			}
		})
	}
}

// ---------------------------------------------------------------------
// Consumer: containers the old 7-arm switch returned UNCHANGED now have
// every ColumnRef under them rebased. Each subtest FAILED against the
// old switch with the cumulative index still in place — the silent
// wrong-column read this commit closes.
// ---------------------------------------------------------------------

func TestLocalizeExprToLeaf_NewlyRebasedContainers(t *testing.T) {
	const offset = 16
	binding := rangeBinding{offset: offset, sourceIdx: 3}
	mk := func() *ColumnRef { return makeColRefBinding("c", 18, 3) } // 18 - 16 = 2

	cases := map[string]struct {
		build func(Expr) Expr
		get   func(Expr) Expr
	}{
		"IsNullExpr": {
			func(c Expr) Expr { return &IsNullExpr{Operand: c} },
			func(e Expr) Expr { return e.(*IsNullExpr).Operand },
		},
		"IsBoolExpr": {
			func(c Expr) Expr { return &IsBoolExpr{Operand: c} },
			func(e Expr) Expr { return e.(*IsBoolExpr).Operand },
		},
		"CollateExpr": {
			func(c Expr) Expr { return &CollateExpr{Operand: c} },
			func(e Expr) Expr { return e.(*CollateExpr).Operand },
		},
		"CastExpr": {
			func(c Expr) Expr { return &CastExpr{Operand: c} },
			func(e Expr) Expr { return e.(*CastExpr).Operand },
		},
		"RowExpr": {
			func(c Expr) Expr { return &RowExpr{Elems: []Expr{&IntegerConst{Value: 1}, c}} },
			func(e Expr) Expr { return e.(*RowExpr).Elems[1] },
		},
		"IsDistinctFromExpr/Left": {
			func(c Expr) Expr { return &IsDistinctFromExpr{Left: c, Right: &IntegerConst{Value: 1}} },
			func(e Expr) Expr { return e.(*IsDistinctFromExpr).Left },
		},
		"IsDistinctFromExpr/Right": {
			func(c Expr) Expr { return &IsDistinctFromExpr{Left: &IntegerConst{Value: 1}, Right: c} },
			func(e Expr) Expr { return e.(*IsDistinctFromExpr).Right },
		},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			in := c.build(mk())
			out := localizeExprToLeaf(in, binding)
			got, ok := c.get(out).(*ColumnRef)
			if !ok {
				t.Fatalf("%s: expected a *ColumnRef under the container, got %T", name, c.get(out))
			}
			if got.Index != 2 {
				t.Errorf("%s: ColumnRef.Index = %d, want 2 (18 - offset 16) — an "+
					"un-rebased index reads the WRONG COLUMN from the leaf slot",
					name, got.Index)
			}
			// The caller's tree must be untouched: these predicates are
			// still referenced from the cumulative-space conjunct list.
			if orig, ok := c.get(in).(*ColumnRef); ok && orig.Index != 18 {
				t.Errorf("%s: input tree mutated — ColumnRef.Index is now %d, want 18",
					name, orig.Index)
			}
		})
	}
}

// A container nested inside another container: the old switch stopped at
// the first unenumerated node, so a BinaryOp UNDER an IsNullExpr was
// unreachable even though BinaryOp was enumerated.
func TestLocalizeExprToLeaf_RebasesUnderNestedContainers(t *testing.T) {
	binding := rangeBinding{offset: 5}
	inner := &BinaryOp{Op: parser.OpAdd,
		Left:  makeColRefBinding("a", 8, 1),
		Right: makeColRefBinding("b", 9, 1)}
	out := localizeExprToLeaf(&IsNullExpr{Operand: inner}, binding)
	got, ok := out.(*IsNullExpr)
	if !ok {
		t.Fatalf("expected *IsNullExpr, got %T", out)
	}
	bin, ok := got.Operand.(*BinaryOp)
	if !ok {
		t.Fatalf("expected *BinaryOp under IS NULL, got %T", got.Operand)
	}
	if l := bin.Left.(*ColumnRef); l.Index != 3 {
		t.Errorf("Left.Index = %d, want 3", l.Index)
	}
	if r := bin.Right.(*ColumnRef); r.Index != 4 {
		t.Errorf("Right.Index = %d, want 4", r.Index)
	}
}

// Leaves that are NOT ColumnRefs keep their values through the clone.
// cloneExprRefs shallow-copies every node including leaves (the old
// switch shared them), so this pins value preservation, not identity.
func TestLocalizeExprToLeaf_NonColumnLeavesPreserved(t *testing.T) {
	binding := rangeBinding{offset: 7}
	out := localizeExprToLeaf(&StringConst{Value: "ASIA"}, binding)
	s, ok := out.(*StringConst)
	if !ok {
		t.Fatalf("expected *StringConst, got %T", out)
	}
	if s.Value != "ASIA" {
		t.Errorf("StringConst.Value = %q, want \"ASIA\"", s.Value)
	}
}

// ---------------------------------------------------------------------
// The pair invariant, over every Expr kind exprChildSlots knows.
//
// This is the pin that makes the two functions one commit: whatever the
// producer admits, the consumer must rebase without aborting. It is
// stated as a property over a per-kind sample table rather than as prose,
// so a future Expr type cannot re-open the gap silently — the
// exhaustiveness gate forces the new kind into exprChildSlots, and this
// table forces a decision about it here.
// ---------------------------------------------------------------------

func TestLeafLocalPairAgreesOnEveryExprKind(t *testing.T) {
	const offset = 4
	binding := rangeBinding{offset: offset}
	cum := 10 // → leaf-local 6
	col := func() *ColumnRef { return &ColumnRef{Index: cum, Name: "a"} }
	plan := &SeqScan{Table: &catalog.Table{Name: "x"}}
	row := &MultiAssignSubqRow{Plan: plan}

	// wantEligible records the DECISION per kind, not a derived value:
	// the six scope-opening kinds and OuterColumnRef are declined; every
	// other kind is a pushable predicate fragment.
	type kind struct {
		expr         Expr
		wantEligible bool
	}
	kinds := map[string]kind{
		// 15 childless leaves.
		"IntegerConst":     {&IntegerConst{Value: 1}, true},
		"StringConst":      {&StringConst{Value: "s"}, true},
		"NumericConst":     {&NumericConst{}, true},
		"TypedStringLit":   {&TypedStringLit{}, true},
		"IntervalLit":      {&IntervalLit{}, true},
		"NullConst":        {&NullConst{}, true},
		"BooleanConst":     {&BooleanConst{Value: true}, true},
		"ColumnRef":        {col(), true},
		"OuterColumnRef":   {&OuterColumnRef{}, false},
		"ParamRef":         {&ParamRef{}, true},
		"ExecParamRef":     {&ExecParamRef{}, true},
		"TableOidExpr":     {&TableOidExpr{}, true},
		"CTIDExpr":         {&CTIDExpr{}, true},
		"MergeActionExpr":  {&MergeActionExpr{}, true},
		"MergeWholeRowRef": {&MergeWholeRowRef{}, true},

		// 11 same-scope containers.
		"ExtractExpr":        {&ExtractExpr{Source: col()}, true},
		"IsNullExpr":         {&IsNullExpr{Operand: col()}, true},
		"IsBoolExpr":         {&IsBoolExpr{Operand: col()}, true},
		"CollateExpr":        {&CollateExpr{Operand: col()}, true},
		"CastExpr":           {&CastExpr{Operand: col()}, true},
		"UnaryOp":            {&UnaryOp{Operand: col()}, true},
		"BinaryOp":           {&BinaryOp{Op: parser.OpEq, Left: col(), Right: &IntegerConst{Value: 1}}, true},
		"IsDistinctFromExpr": {&IsDistinctFromExpr{Left: col(), Right: &IntegerConst{Value: 1}}, true},
		"FuncCall":           {&FuncCall{Name: "abs", Args: []Expr{col()}}, true},
		"RowExpr":            {&RowExpr{Elems: []Expr{col()}}, true},
		"CaseExpr":           {&CaseExpr{Whens: []CaseWhen{{When: col(), Then: &IntegerConst{Value: 1}}}}, true},

		// 6 scope-opening kinds. InExpr appears twice because its two
		// forms take opposite decisions and only one of them is a scope
		// crossing.
		"InExpr/list":         {&InExpr{Operand: col(), List: []Expr{&IntegerConst{Value: 1}}}, true},
		"InExpr/plan":         {&InExpr{Operand: col(), Plan: plan}, false},
		"ExistsExpr":          {&ExistsExpr{Plan: plan}, false},
		"SubqueryExpr":        {&SubqueryExpr{Plan: plan}, false},
		"ArraySubqueryExpr":   {&ArraySubqueryExpr{Plan: plan}, false},
		"MultiAssignSubqRow":  {row, false},
		"MultiAssignSubqElem": {&MultiAssignSubqElem{Row: row}, false},
	}

	for name, k := range kinds {
		t.Run(name, func(t *testing.T) {
			got := conjunctIsLocalEligible(k.expr)
			if got != k.wantEligible {
				t.Fatalf("conjunctIsLocalEligible(%s) = %v, want %v", name, got, k.wantEligible)
			}
			if !got {
				return
			}
			// The promise: an admitted conjunct rebases without the
			// consumer's abort panic, and every ColumnRef under it moves.
			out := localizeExprToLeaf(k.expr, binding)
			if out == nil {
				t.Fatalf("localizeExprToLeaf(%s) returned nil", name)
			}
			var seen int
			visitColumnRefsForTable(out, func(idx int) {
				seen++
				if idx != cum-offset {
					t.Errorf("%s: ColumnRef.Index = %d after rebase, want %d",
						name, idx, cum-offset)
				}
			})
			// Sanity: the table must actually contain a reference where
			// one is structurally possible, or the check above is vacuous.
			if seen == 0 && name != "OuterColumnRef" {
				switch k.expr.(type) {
				case *IntegerConst, *StringConst, *NumericConst, *TypedStringLit,
					*IntervalLit, *NullConst, *BooleanConst, *ParamRef,
					*ExecParamRef, *TableOidExpr, *CTIDExpr, *MergeActionExpr,
					*MergeWholeRowRef:
					// genuinely reference-free kinds
				default:
					t.Errorf("%s: sample carries no ColumnRef — the rebase check is vacuous", name)
				}
			}
		})
	}
}
