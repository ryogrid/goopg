package main

import (
	"reflect"
	"strings"
	"testing"
)

// TestSelectQueriesAll pins the empty-spec → all 22 default.
func TestSelectQueriesAll(t *testing.T) {
	got := selectQueries("")
	if len(got) != 22 {
		t.Fatalf("len(selectQueries(\"\")) = %d, want 22", len(got))
	}
	for i, q := range got {
		if q != i+1 {
			t.Errorf("got[%d] = %d, want %d", i, q, i+1)
		}
	}
}

// TestSelectQueriesCSV pins comma-separated parsing.
func TestSelectQueriesCSV(t *testing.T) {
	got := selectQueries("1,3,5")
	want := []int{1, 3, 5}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectQueries(\"1,3,5\") = %v, want %v", got, want)
	}
}

// TestSelectQueriesRange pins range parsing + dedup + sort.
func TestSelectQueriesRange(t *testing.T) {
	got := selectQueries("5-9,3,7-10")
	want := []int{3, 5, 6, 7, 8, 9, 10}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("selectQueries(\"5-9,3,7-10\") = %v, want %v", got, want)
	}
}

// TestPlanEqualStrictText pins byte-for-byte mode.
func TestPlanEqualStrictText(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (rows=6000000)"
	b := "Sort\n  ->  SeqScan: lineitem (rows=6000000)"
	if !planEqual(a, b, "strict-text") {
		t.Errorf("identical strings should match strict-text")
	}
	c := "Sort\n  ->  SeqScan: lineitem (rows=6000001)"
	if planEqual(a, c, "strict-text") {
		t.Errorf("differing rows should NOT match strict-text")
	}
}

// TestPlanEqualStructuralIgnoresCost pins that structural mode strips the
// `(cost=A..B rows=N width=W)` annotation before comparing. review/260831-2
// CM-3: these cases used to be written against a bare `(rows=N)` suffix that
// no EXPLAIN ever prints, so they passed while stripping nothing.
func TestPlanEqualStructuralIgnoresCost(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	b := "Sort\n  ->  SeqScan: lineitem (cost=0.00..599999.90 rows=5999999 width=0)"
	if !planEqual(a, b, "structural") {
		t.Errorf("plans differing only in (rows=N) should match structural")
	}
	c := "Sort\n  ->  IndexScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	if planEqual(a, c, "structural") {
		t.Errorf("plans differing in node type should NOT match structural")
	}
}

// TestPlanEqualSemanticCostTolerance pins the ±10 %
// tolerance for cost values.
func TestPlanEqualSemanticCostTolerance(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (cost=0.00..100.00 rows=1000 width=0)"
	// +5 % cost (within 10 % tolerance)
	b := "Sort\n  ->  SeqScan: lineitem (cost=0.00..105.00 rows=1050 width=0)"
	if !planEqual(a, b, "semantic-cost") {
		t.Errorf("cost +5%% should match semantic-cost")
	}
	// +20 % cost (beyond 10 % tolerance)
	c := "Sort\n  ->  SeqScan: lineitem (cost=0.00..120.00 rows=1200 width=0)"
	if planEqual(a, c, "semantic-cost") {
		t.Errorf("cost +20%% should NOT match semantic-cost")
	}
}

// TestPlanEqualStructuralMultiline pins that structural
// mode handles multi-line plan trees.
func TestPlanEqualStructuralMultiline(t *testing.T) {
	a := `Sort
  ->  Aggregate (cost=0.00..0.50 rows=5 width=0)
    ->  MultiHashJoin (cost=0.00..1234.50 rows=12345 width=0)
      ->  SeqScan: orders
      ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)`
	// Cost varies, structure same.
	b := `Sort
  ->  Aggregate (cost=0.00..0.40 rows=4 width=0)
    ->  MultiHashJoin (cost=0.00..1250.00 rows=12500 width=0)
      ->  SeqScan: orders
      ->  SeqScan: lineitem (cost=0.00..595000.00 rows=5950000 width=0)`
	if !planEqual(a, b, "structural") {
		t.Errorf("multi-line plans with cost diff should match structural")
	}
}

// TestRowsRegexpExtractsCosts pins the cost extractor.
func TestRowsRegexpExtractsCosts(t *testing.T) {
	in := "Sort\n  ->  Aggregate (cost=0.00..0.50 rows=5 width=0)\n    ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	got := extractCosts(in)
	want := []int64{5, 6000000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractCosts = %v, want %v", got, want)
	}
}

// TestRowsRegexpStripsAnnotations pins the structural-mode
// normalisation.
func TestRowsRegexpStripsAnnotations(t *testing.T) {
	in := "  ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	got := rowsRegexp.ReplaceAllString(in, "")
	want := "  ->  SeqScan: lineitem"
	if got != want {
		t.Errorf("strip got %q, want %q", got, want)
	}
}

// TestPlanEqualCostsDetectsCostOnlyChange pins the A-05 cost-visible mode:
// a hashsize-style reprice that moves cost/rows/width without reshaping is a
// DIFFER under costs while structural stays MATCH. Fixture pair, not inline
// trivia: the two texts differ only inside the estimate annotations.
func TestPlanEqualCostsDetectsCostOnlyChange(t *testing.T) {
	a := `Sort
  ->  MultiHashJoin (cost=0.00..1234.50 rows=12345 width=64)
    ->  SeqScan: orders (cost=0.00..100.00 rows=1500 width=32)
    ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=32)`
	b := `Sort
  ->  MultiHashJoin (cost=0.00..1300.00 rows=13000 width=64)
    ->  SeqScan: orders (cost=0.00..100.00 rows=1500 width=32)
    ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=32)`
	if !planEqual(a, b, "structural") {
		t.Errorf("cost-only move should MATCH structural")
	}
	if planEqual(a, b, "costs") {
		t.Errorf("cost-only move should DIFFER under costs")
	}
	if !planEqual(a, a, "costs") {
		t.Errorf("identical plans should MATCH costs")
	}
}

// TestPlanEqualCostsIgnoresIndentation pins costs vs strict-text: per-line
// whitespace is not signal under costs, but it is under strict-text.
func TestPlanEqualCostsIgnoresIndentation(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	b := "Sort\n    ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	if !planEqual(a, b, "costs") {
		t.Errorf("indentation-only difference should MATCH costs")
	}
	if planEqual(a, b, "strict-text") {
		t.Errorf("indentation-only difference should DIFFER strict-text")
	}
}

// TestPlanEqualCostsDetectsShapeChange pins that costs is still a shape pin:
// a node-type move fails under every mode.
func TestPlanEqualCostsDetectsShapeChange(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	c := "Sort\n  ->  IndexScan: lineitem (cost=0.00..600000.00 rows=6000000 width=0)"
	for _, mode := range []string{"structural", "costs", "strict-text", "semantic-cost"} {
		if planEqual(a, c, mode) {
			t.Errorf("node-type move should DIFFER under %s", mode)
		}
	}
}

// TestReadSnapshotMissingFailsLoudly pins the A-05 non-skippable half at the
// binary level: a missing baseline is an error, never a silent pass. The
// Makefile gate relies on this — it must not (and no longer does) convert a
// missing baseline into SKIP-exit-0 itself.
func TestReadSnapshotMissingFailsLoudly(t *testing.T) {
	if _, err := readSnapshot("testdata/no-such-baseline.txt"); err == nil {
		t.Errorf("readSnapshot(missing) = nil error, want a loud failure")
	}
}

// TestPlanEqualUnknownModeFalse pins that an unknown mode never matches.
func TestPlanEqualUnknownModeFalse(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem"
	if planEqual(a, a, "no-such-mode") {
		t.Errorf("unknown mode should never match, even on identical input")
	}
}

// TestRowsRegexpMatchesRealExplainOutput is the review/260831-2 CM-3 guard:
// the annotation shapes actually produced by PG 18.3 and by goopg's EXPLAIN
// (internal/executor/operators_explain.go) must both be recognised, or
// structural mode degrades into strict-text without saying so.
func TestRowsRegexpMatchesRealExplainOutput(t *testing.T) {
	for _, line := range []string{
		"Seq Scan on lineitem  (cost=0.00..35.50 rows=2550 width=4)", // PG 18.3
		"SeqScan: lineitem  (cost=0.00..0.00 rows=2550 width=0)",     // goopg
	} {
		if got := extractCosts(line); len(got) != 1 || got[0] != 2550 {
			t.Errorf("extractCosts(%q) = %v, want [2550]", line, got)
		}
		stripped := rowsRegexp.ReplaceAllString(line, "")
		if strings.Contains(stripped, "rows=") {
			t.Errorf("annotation not stripped from %q: %q", line, stripped)
		}
	}
}
