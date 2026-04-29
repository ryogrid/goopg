package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestExplainAnalyzeTimingOffSuppressesTimeBracket: with
// `TIMING off`, TEXT output still reports rows/loops but omits
// the per-node `time=` bracket. Operators running ANALYZE for
// reproducible output (drift-free CI snapshots) rely on this.
func TestExplainAnalyzeTimingOffSuppressesTimeBracket(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, TIMING off) SELECT 1")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "rows=1") || !strings.Contains(joined, "loops=1") {
		t.Errorf("expected rows=1 / loops=1 in output:\n%s", joined)
	}
	if strings.Contains(joined, "time=") {
		t.Errorf("TIMING off should suppress 'time=' bracket:\n%s", joined)
	}
}

// TestExplainAnalyzeTimingOnByDefault: pins the default-true
// semantics. Without explicit `TIMING off`, the time= bracket
// must be present (otherwise the previous test's contract
// trivially passes for the wrong reason).
func TestExplainAnalyzeTimingOnByDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE) SELECT 1")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "actual time=") {
		t.Errorf("default ANALYZE should include 'actual time=':\n%s", joined)
	}
}

// TestExplainAnalyzeSummaryOffSuppressesFooter: with
// `SUMMARY off`, the trailing Planning Time / Execution Time
// rows are dropped.
func TestExplainAnalyzeSummaryOffSuppressesFooter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, SUMMARY off) SELECT 1")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Planning Time:") {
		t.Errorf("SUMMARY off should drop 'Planning Time:' footer:\n%s", joined)
	}
	if strings.Contains(joined, "Execution Time:") {
		t.Errorf("SUMMARY off should drop 'Execution Time:' footer:\n%s", joined)
	}
}

// TestExplainAnalyzeTimingOffJSONOmitsTimeKeys: per-node JSON
// loses Actual Total Time / Actual Startup Time when TIMING is
// off. The Actual Rows / Actual Loops keys remain.
func TestExplainAnalyzeTimingOffJSONOmitsTimeKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, TIMING off, FORMAT JSON) SELECT 1")
	if len(lines) != 1 {
		t.Fatalf("got %d rows, want 1", len(lines))
	}
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatal(err)
	}
	root := top[0]
	for _, must := range []string{"Actual Rows", "Actual Loops"} {
		if _, ok := root[must]; !ok {
			t.Errorf("expected %q present even with TIMING off: %+v", must, root)
		}
	}
	for _, mustNot := range []string{"Actual Total Time", "Actual Startup Time"} {
		if _, ok := root[mustNot]; ok {
			t.Errorf("TIMING off must omit %q: %+v", mustNot, root)
		}
	}
}

// TestExplainAnalyzeSummaryOffJSONOmitsTimingKeys: JSON's root
// loses Planning Time / Execution Time when SUMMARY is off,
// regardless of TIMING.
func TestExplainAnalyzeSummaryOffJSONOmitsTimingKeys(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, SUMMARY off, FORMAT JSON) SELECT 1")
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatal(err)
	}
	root := top[0]
	for _, mustNot := range []string{"Planning Time", "Execution Time"} {
		if _, ok := root[mustNot]; ok {
			t.Errorf("SUMMARY off must omit %q: %+v", mustNot, root)
		}
	}
}

// TestExplainJSONShapeStable: structural snapshot for a
// stable known query. Pins the JSON shape contract so future
// renderer changes (BUFFERS / SETTINGS additions) don't
// silently drop a required key. Uses a non-ANALYZE query so
// timing values can't drift across architectures.
func TestExplainJSONShapeStable(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON) SELECT 1")
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, lines[0])
	}
	if len(top) != 1 {
		t.Fatalf("root array has %d entries, want 1", len(top))
	}
	root := top[0]
	// Required key.
	if _, ok := root["Node Type"]; !ok {
		t.Error("missing required 'Node Type'")
	}
	// Gated keys must be absent without their option.
	for _, mustNot := range []string{
		"Output", "Actual Rows", "Actual Loops",
		"Actual Total Time", "Actual Startup Time",
		"Planning Time", "Execution Time",
	} {
		if _, ok := root[mustNot]; ok {
			t.Errorf("non-ANALYZE JSON should not include %q: %+v", mustNot, root)
		}
	}
}
