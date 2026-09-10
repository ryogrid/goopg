package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
)

// R48 Half-1 pins: a trivially-true *Filter wrapper is unnest scaffolding
// (unnest.go pushConjunctsBelowSemiAnti keeps the wrapper "so downstream
// code that recurses through Filter still finds the join") — PG has no such
// node, so the renderer must pass through it. Every pin renders through BOTH
// walkers (renderPlain + renderAnalyze): the file's own doctrine says a test
// pinning one proves nothing about the other, and the ANALYZE walker carries
// a twin Filter arm.

// trueWrapped renders n through both walkers with a BooleanConst{true}
// Filter wrapper above it.
func trueWrapped(t *testing.T, n optimizer.Node) (plain, analyze string) {
	t.Helper()
	w := &optimizer.Filter{
		Child:     n,
		Predicate: &optimizer.BooleanConst{Value: true},
	}
	return renderPlain(t, w), renderAnalyze(t, w)
}

// TestTrueFilterWrapperSkippedAboveScan: Filter{true} above a bare scan
// renders byte-identical to the bare scan — no `Filter: (true)` line, and
// the host line re-prices from the wrapper carrier to the child carrier.
func TestTrueFilterWrapperSkippedAboveScan(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	scan := &optimizer.SeqScan{Table: tbl, EstRelRows: 10000}
	barePlain, bareAnalyze := renderPlain(t, scan), renderAnalyze(t, scan)
	wrappedPlain, wrappedAnalyze := trueWrapped(t, scan)
	if wrappedPlain != barePlain {
		t.Errorf("plain walker: true-wrapper render differs from bare scan\nwrapped:\n%s\nbare:\n%s",
			wrappedPlain, barePlain)
	}
	if wrappedAnalyze != bareAnalyze {
		t.Errorf("analyze walker: true-wrapper render differs from bare scan\nwrapped:\n%s\nbare:\n%s",
			wrappedAnalyze, bareAnalyze)
	}
	if strings.Contains(wrappedPlain, "(true)") || strings.Contains(wrappedAnalyze, "(true)") {
		t.Errorf("true-wrapper still renders a (true) line\nplain:\n%s\nanalyze:\n%s",
			wrappedPlain, wrappedAnalyze)
	}
}

// TestTrueFilterWrapperSkippedAboveJoin: the R48 Q4 shape — a true-wrapper
// above a semi join renders the join with no stray line.
func TestTrueFilterWrapperSkippedAboveJoin(t *testing.T) {
	outer := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "o"), EstRelRows: 10000}
	inner := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "i"), EstRelRows: 10000}
	join := &optimizer.Join{Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop, Left: outer, Right: inner}
	barePlain, bareAnalyze := renderPlain(t, join), renderAnalyze(t, join)
	wrappedPlain, wrappedAnalyze := trueWrapped(t, join)
	if wrappedPlain != barePlain {
		t.Errorf("plain walker: true-wrapper above join differs from bare join\nwrapped:\n%s\nbare:\n%s",
			wrappedPlain, barePlain)
	}
	if wrappedAnalyze != bareAnalyze {
		t.Errorf("analyze walker: true-wrapper above join differs from bare join\nwrapped:\n%s\nbare:\n%s",
			wrappedAnalyze, bareAnalyze)
	}
}

// TestTrueFilterBetweenRealFilterAndJoin: a real outer filter above a
// true-wrapper still renders its own `Filter:` line — the pass-through
// carries the INCOMING attached filter unchanged.
func TestTrueFilterBetweenRealFilterAndJoin(t *testing.T) {
	outer := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "o"), EstRelRows: 10000}
	inner := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "i"), EstRelRows: 10000}
	join := &optimizer.Join{Type: optimizer.JoinTypeSemi, Algo: optimizer.JoinAlgoNestedLoop, Left: outer, Right: inner}
	real := &optimizer.Filter{
		Child: &optimizer.Filter{
			Child:     join,
			Predicate: &optimizer.BooleanConst{Value: true},
		},
		Predicate: &optimizer.BinaryOp{
			Op:    parser.OpEq,
			Left:  &optimizer.ColumnRef{Name: "a"},
			Right: &optimizer.IntegerConst{Value: 42},
		},
	}
	for name, out := range map[string]string{"plain": renderPlain(t, real), "analyze": renderAnalyze(t, real)} {
		if !strings.Contains(out, "Filter:") {
			t.Errorf("%s walker: real outer filter above true-wrapper lost its Filter: line\n%s", name, out)
		}
		if strings.Contains(out, "(true)") {
			t.Errorf("%s walker: true-wrapper still renders a (true) line\n%s", name, out)
		}
	}
}

// TestNilPredicateFilterUntouched: a nil predicate is NOT a trivially-true
// constant — the skip must not fire, and rendering must not panic.
func TestNilPredicateFilterUntouched(t *testing.T) {
	scan := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "t"), EstRelRows: 10000}
	nilf := &optimizer.Filter{Child: scan}
	for name, out := range map[string]string{"plain": renderPlain(t, nilf), "analyze": renderAnalyze(t, nilf)} {
		if !strings.Contains(out, "Seq Scan") {
			t.Errorf("%s walker: nil-predicate filter lost the scan line\n%s", name, out)
		}
	}
}

// TestFalseFilterNotSkipped: the skip fires ONLY on Value:true — a false
// constant is a real (selective) qual and must still render.
func TestFalseFilterNotSkipped(t *testing.T) {
	scan := &optimizer.SeqScan{Table: parallelLabelTestTable(t, "t"), EstRelRows: 10000}
	bare := renderPlain(t, scan)
	falsef := &optimizer.Filter{Child: scan, Predicate: &optimizer.BooleanConst{Value: false}}
	out := renderPlain(t, falsef)
	if !strings.Contains(out, "Filter:") {
		t.Errorf("false predicate was skipped like a true one — no Filter: line\n%s", out)
	}
	if out == bare {
		t.Errorf("false predicate renders byte-identical to the bare scan — the qual was dropped\n%s", out)
	}
}
