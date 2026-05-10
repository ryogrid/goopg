package main

import (
	"reflect"
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

// TestPlanEqualStructuralIgnoresCost pins that structural
// mode strips `(rows=N)` annotations before comparing.
func TestPlanEqualStructuralIgnoresCost(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (rows=6000000)"
	b := "Sort\n  ->  SeqScan: lineitem (rows=5999999)"
	if !planEqual(a, b, "structural") {
		t.Errorf("plans differing only in (rows=N) should match structural")
	}
	c := "Sort\n  ->  IndexScan: lineitem (rows=6000000)"
	if planEqual(a, c, "structural") {
		t.Errorf("plans differing in node type should NOT match structural")
	}
}

// TestPlanEqualSemanticCostTolerance pins the ±10 %
// tolerance for cost values.
func TestPlanEqualSemanticCostTolerance(t *testing.T) {
	a := "Sort\n  ->  SeqScan: lineitem (rows=1000)"
	// +5 % cost (within 10 % tolerance)
	b := "Sort\n  ->  SeqScan: lineitem (rows=1050)"
	if !planEqual(a, b, "semantic-cost") {
		t.Errorf("cost +5%% should match semantic-cost")
	}
	// +20 % cost (beyond 10 % tolerance)
	c := "Sort\n  ->  SeqScan: lineitem (rows=1200)"
	if planEqual(a, c, "semantic-cost") {
		t.Errorf("cost +20%% should NOT match semantic-cost")
	}
}

// TestPlanEqualStructuralMultiline pins that structural
// mode handles multi-line plan trees.
func TestPlanEqualStructuralMultiline(t *testing.T) {
	a := `Sort
  ->  Aggregate (rows=5)
    ->  MultiHashJoin (rows=12345)
      ->  SeqScan: orders
      ->  SeqScan: lineitem (rows=6000000)`
	// Cost varies, structure same.
	b := `Sort
  ->  Aggregate (rows=4)
    ->  MultiHashJoin (rows=12500)
      ->  SeqScan: orders
      ->  SeqScan: lineitem (rows=5950000)`
	if !planEqual(a, b, "structural") {
		t.Errorf("multi-line plans with cost diff should match structural")
	}
}

// TestRowsRegexpExtractsCosts pins the cost extractor.
func TestRowsRegexpExtractsCosts(t *testing.T) {
	in := "Sort\n  ->  Aggregate (rows=5)\n    ->  SeqScan: lineitem (rows=6000000)"
	got := extractCosts(in)
	want := []int64{5, 6000000}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractCosts = %v, want %v", got, want)
	}
}

// TestRowsRegexpStripsAnnotations pins the structural-mode
// normalisation.
func TestRowsRegexpStripsAnnotations(t *testing.T) {
	in := "  ->  SeqScan: lineitem (rows=6000000)"
	got := rowsRegexp.ReplaceAllString(in, "")
	want := "  ->  SeqScan: lineitem"
	if got != want {
		t.Errorf("strip got %q, want %q", got, want)
	}
}
