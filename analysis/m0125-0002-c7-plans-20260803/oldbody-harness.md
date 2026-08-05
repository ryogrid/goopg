# M0125-0002 commit 7 — "prove the pin fails first" harness (verbatim)

Deleted from the tree after the proof run; reproduced here so the
proof is auditable without a git archaeology step. Output:
`pin-proof.txt`.

```go
package planner

// TEMPORARY — M0125-0002 commit 7 "prove the pin fails first" harness.
// Deleted before the commit; reproduced verbatim in the analysis dir.
//
// The conversion changes visitColumnRefsByName's SIGNATURE (it now
// returns a totality flag), so the real pin file cannot be compiled
// against the pre-conversion body the way commits 3-6 did it. Instead the
// old 7-arm body and the old consumer shapes are reproduced here under
// _c7old names and the same tables are run against them, asserting that
// each pin's expectation is VIOLATED. Run with -v; every subtest below
// must report the old body's wrong answer.

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// ---- verbatim pre-conversion body (bushy.go @ 900990a2) ----------------

func visitColumnRefsByName_c7old(e Expr, fn func(string)) {
	if e == nil {
		return
	}
	switch x := e.(type) {
	case *ColumnRef:
		if x.Name != "" {
			fn(x.Name)
		}
	case *BinaryOp:
		visitColumnRefsByName_c7old(x.Left, fn)
		visitColumnRefsByName_c7old(x.Right, fn)
	case *UnaryOp:
		visitColumnRefsByName_c7old(x.Operand, fn)
	case *FuncCall:
		for _, a := range x.Args {
			visitColumnRefsByName_c7old(a, fn)
		}
	case *ExtractExpr:
		visitColumnRefsByName_c7old(x.Source, fn)
	case *CaseExpr:
		if x.Operand != nil {
			visitColumnRefsByName_c7old(x.Operand, fn)
		}
		for _, w := range x.Whens {
			visitColumnRefsByName_c7old(w.When, fn)
			visitColumnRefsByName_c7old(w.Then, fn)
		}
		if x.Else != nil {
			visitColumnRefsByName_c7old(x.Else, fn)
		}
	case *InExpr:
		visitColumnRefsByName_c7old(x.Operand, fn)
	}
}

func extraInScans_c7old(c Expr, scans []Node) bool {
	allMatched := true
	visitColumnRefsByName_c7old(c, func(name string) {
		found := false
		for _, s := range scans {
			ss, ok := s.(*SeqScan)
			if !ok {
				continue
			}
			for _, col := range ss.Output() {
				if col.Name == name {
					found = true
					break
				}
			}
			if found {
				break
			}
		}
		if !found {
			allMatched = false
		}
	})
	return allMatched
}

func allColumnRefNamesInScope_c7old(c Expr, j *Join) bool {
	names := map[string]bool{}
	collectScanOutputNames(j.Left, names)
	collectScanOutputNames(j.Right, names)
	allIn := true
	visitColumnRefsByName_c7old(c, func(name string) {
		if !names[name] {
			allIn = false
		}
	})
	return allIn
}

func byNameSaw_c7old(e Expr, want string) bool {
	saw := false
	visitColumnRefsByName_c7old(e, func(n string) {
		if n == want {
			saw = true
		}
	})
	return saw
}

// ---- group 1: the newly-collected kinds were NOT collected ------------

func TestC7Old_NewlyCollectedKindsWereSkipped(t *testing.T) {
	col := func() Expr { return &ColumnRef{Index: 1, Name: "want_me"} }
	plan := &SeqScan{Table: &catalog.Table{Name: "x"}}

	cases := []struct {
		name string
		expr Expr
	}{
		{"IsNullExpr", &IsNullExpr{Operand: col()}},
		{"IsBoolExpr", &IsBoolExpr{Operand: col()}},
		{"CollateExpr", &CollateExpr{Operand: col()}},
		{"CastExpr", &CastExpr{Operand: col()}},
		{"IsDistinctFromExpr/Left", &IsDistinctFromExpr{Left: col(), Right: &IntegerConst{}}},
		{"IsDistinctFromExpr/Right", &IsDistinctFromExpr{Left: &IntegerConst{}, Right: col()}},
		{"RowExpr", &RowExpr{Elems: []Expr{col()}}},
		{"InExpr/List", &InExpr{Operand: &IntegerConst{}, List: []Expr{col()}}},
		{"InExpr/Args", &InExpr{Operand: &IntegerConst{}, Plan: plan, Args: []Expr{col()}}},
		{"ExistsExpr/Args", &ExistsExpr{Plan: plan, Args: []Expr{col()}}},
		{"SubqueryExpr/Args", &SubqueryExpr{Plan: plan, Args: []Expr{col()}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if byNameSaw_c7old(c.expr, "want_me") {
				t.Fatalf("PIN NOT PROVED: old body already collected %s", c.name)
			}
			t.Logf("old body skipped %s (pin would fail) — OK", c.name)
		})
	}
}

// ---- group 3: the consumer inversion --------------------------------

func TestC7Old_ExtraInScansAdmittedOpaqueConjuncts(t *testing.T) {
	scans := []Node{byNameScan("t", "a", "b")}
	cases := []struct {
		name string
		expr Expr
	}{
		{"SubqueryExpr", &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "a"},
			Right: &SubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "z"}}}}},
		{"ExistsExpr", &ExistsExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "z"}}}},
		{"ArraySubqueryExpr", &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "a"},
			Right: &ArraySubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "z"}}}}},
		{"OuterColumnRef", &BinaryOp{Op: parser.OpEq,
			Left:  &ColumnRef{Index: 0, Name: "a"},
			Right: &OuterColumnRef{Level: 1, Index: 2, Name: "a"}}},
		{"CTIDExpr", &BinaryOp{Op: parser.OpEq,
			Left: &ColumnRef{Index: 0, Name: "a"}, Right: &CTIDExpr{}}},
		{"unnamed ColumnRef", &BinaryOp{Op: parser.OpEq,
			Left: &ColumnRef{Index: 0, Name: "a"}, Right: &ColumnRef{Index: 9}}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if !extraInScans_c7old(c.expr, scans) {
				t.Fatalf("PIN NOT PROVED: old extraInScans already rejected %s", c.name)
			}
			t.Logf("old extraInScans ADMITTED %s into MHJ.Filters — pin would fail", c.name)
		})
	}
}

func TestC7Old_AllColumnRefNamesInScopeCertifiedOpaque(t *testing.T) {
	j := &Join{Left: byNameScan("t", "a"), Right: byNameScan("u", "c")}
	opaque := &BinaryOp{Op: parser.OpEq,
		Left:  &ColumnRef{Index: 0, Name: "a"},
		Right: &ArraySubqueryExpr{Plan: &SeqScan{Table: &catalog.Table{Name: "z"}}}}
	if !allColumnRefNamesInScope_c7old(opaque, j) {
		t.Fatal("PIN NOT PROVED: old allColumnRefNamesInScope already rejected it")
	}
	t.Log("old allColumnRefNamesInScope CERTIFIED an unlooked-at subquery — pin would fail")
}
```
