package executor

import (
	"strings"
	"testing"
)

// TestExplainCostsOffSuppressesRowsSuffix pins the M0100-0005i
// behaviour: `EXPLAIN (COSTS OFF) ...` must not append `(rows=N)`
// to any node label.  PG's COSTS OFF mode renders bare node
// labels; the isolation specs depend on this format.
func TestExplainCostsOffSuppressesRowsSuffix(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM t WHERE data = 34")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "(rows=") {
		t.Errorf("COSTS OFF must suppress (rows=N) suffix; got:\n%s", joined)
	}
}

// TestExplainCostsOnAnalyzeIncludesActualRows pins the converse:
// `EXPLAIN (ANALYZE)` (Costs defaults to true) still includes
// the per-node `actual rows=N` instrumentation even when no
// `(rows=N)` planner-estimate appears (e.g. unanalysed tables).
// Otherwise TestExplainCostsOffSuppressesRowsSuffix could pass
// trivially by always suppressing every rows= token.
func TestExplainCostsOnAnalyzeIncludesActualRows(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE) SELECT * FROM t")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "rows=") {
		t.Errorf("EXPLAIN (ANALYZE) should include rows= in `actual` block; got:\n%s", joined)
	}
}

// TestExplainSuppressesProjectionWrapper pins that EXPLAIN does
// not surface a "Projection" plan node.  PG folds projection into
// the parent / scan label; goopg's planner inserts a Project node
// around select lists but EXPLAIN must hide it for upstream parity.
// Pre-M0100-0005i the output started with `Projection (rows=N)`.
func TestExplainSuppressesProjectionWrapper(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM t WHERE data = 34")
	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "Projection") {
		t.Errorf("EXPLAIN must not surface a Projection wrapper; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Seq Scan on t") {
		t.Errorf("EXPLAIN should still render the underlying SeqScan; got:\n%s", joined)
	}
}

// TestExplainEmitsFilterDetailUnderSeqScan pins the M0100-0005i
// behaviour: a Filter wrapper above a SeqScan must render its
// predicate as `Filter: (<pred>)` indented under the scan label,
// matching upstream PG's EXPLAIN TEXT format.
func TestExplainEmitsFilterDetailUnderSeqScan(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM t WHERE data = 34")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Filter: (data = 34)") {
		t.Errorf("expected `Filter: (data = 34)` detail line under SeqScan; got:\n%s", joined)
	}
	// The Filter wrapper itself should not appear as a node label.
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "Filter") && !strings.HasPrefix(strings.TrimSpace(line), "Filter:") {
			t.Errorf("Filter wrapper leaked as a node label: %q", line)
		}
	}
}

// TestExplainEmitsSortKeyDetail pins the M0100-0005i behaviour:
// a Sort node's keys must render as a `Sort Key: <expr_csv>` detail
// line, matching upstream PG's EXPLAIN format.
func TestExplainEmitsSortKeyDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM t ORDER BY id, data")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Sort Key: id, data") {
		t.Errorf("expected `Sort Key: id, data` detail line; got:\n%s", joined)
	}
}

// TestExplainEmitsIndexCondDetail pins the M0100-0005i behaviour:
// an IndexScan with a single-column equality key must render as
// `Index Cond: (<col> = <key>)` under the scan label.
func TestExplainEmitsIndexCondDetail(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, data int)"); err != nil {
		t.Fatal(err)
	}
	if err := runDDL(t, ctx, "CREATE INDEX t_data ON t(data)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) SELECT * FROM t WHERE data = 34")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Index Scan") {
		t.Skipf("planner did not pick IndexScan (need cost-tuned planner); got:\n%s", joined)
	}
	if !strings.Contains(joined, "Index Cond: (data = 34)") {
		t.Errorf("expected `Index Cond: (data = 34)` detail line; got:\n%s", joined)
	}
}
