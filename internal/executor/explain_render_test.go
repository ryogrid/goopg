package executor

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
)

// runExplainRows wraps the parser → planner → executor pipeline
// for an EXPLAIN-prefixed SQL string and returns the QUERY PLAN
// rows. Used by every M0018-0002 render test.
func runExplainRows(t *testing.T, ctx *Context, sql string) []string {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	plan, err := planner.Plan(stmts[0], ctx.Catalog)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	op, err := Build(plan)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := op.Open(ctx); err != nil {
		t.Fatalf("Open: %v", err)
	}
	rows, err := drainScan(op)
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	_ = op.Close()
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if len(r) > 0 && r[0].Kind == KindString {
			out = append(out, r[0].StringValue())
		}
	}
	return out
}

// TestExplainTextFormatUnchanged: the bare-EXPLAIN form
// produces the same multi-line indented TEXT output it produced
// before M0018-0002 — pre-existing tests / fixtures stay
// byte-for-byte stable.
func TestExplainTextFormatUnchanged(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT 1")
	if len(lines) == 0 {
		t.Fatal("EXPLAIN produced no rows")
	}
	// The first line should be the root-node label without the
	// `->` prefix (matches the pre-M0018 rendering invariant).
	if strings.HasPrefix(lines[0], "->") {
		t.Errorf("first line starts with '->' (root should have no arrow): %q", lines[0])
	}
}

// TestExplainFormatJSONProducesValidJSON: `EXPLAIN (FORMAT JSON)
// SELECT 1` returns a single row whose value parses as JSON and
// whose root array contains one object with `"Node Type"`.
func TestExplainFormatJSONProducesValidJSON(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON) SELECT 1")
	if len(lines) != 1 {
		t.Fatalf("FORMAT JSON produced %d rows, want 1", len(lines))
	}
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, lines[0])
	}
	if len(top) != 1 {
		t.Fatalf("root array has %d entries, want 1", len(top))
	}
	if _, ok := top[0]["Node Type"]; !ok {
		t.Errorf("root object missing 'Node Type' key: %+v", top[0])
	}
}

// TestExplainFormatJSONOmitsOutputWithoutVerbose: the JSON
// shape's Output array is opt-in via VERBOSE — without it the
// key shouldn't appear.
func TestExplainFormatJSONOmitsOutputWithoutVerbose(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON) SELECT 1")
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatal(err)
	}
	if _, ok := top[0]["Output"]; ok {
		t.Errorf("non-verbose JSON includes 'Output' key: %+v", top[0])
	}
}

// TestExplainFormatJSONHonoursVerbose: with both FORMAT JSON
// and VERBOSE, the JSON object includes the Output column-name
// array. Pin the cross-option interaction.
func TestExplainFormatJSONHonoursVerbose(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, val int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON, VERBOSE) SELECT id FROM t")
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatal(err)
	}
	out, ok := top[0]["Output"]
	if !ok {
		t.Fatalf("verbose JSON missing 'Output' key: %+v", top[0])
	}
	cols, ok := out.([]any)
	if !ok || len(cols) == 0 {
		t.Errorf("Output not a non-empty array: %+v", out)
	}
}

// TestExplainVerboseAddsOutputLine: `EXPLAIN VERBOSE` in TEXT
// format adds an `Output:` line under each node listing the
// projected columns. Pins the verbose-text shape.
func TestExplainVerboseAddsOutputLine(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int, val int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN VERBOSE SELECT id FROM t")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Output:") {
		t.Errorf("VERBOSE EXPLAIN missing 'Output:' line:\n%s", joined)
	}
	if !strings.Contains(joined, "id") {
		t.Errorf("VERBOSE EXPLAIN didn't list the 'id' column:\n%s", joined)
	}
}

func TestExplainIncludesWindowAggNodeText(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT row_number() OVER (ORDER BY id) FROM t")
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "WindowAgg") {
		t.Errorf("EXPLAIN TEXT missing WindowAgg node:\n%s", joined)
	}
}

func TestExplainIncludesWindowAggNodeJSON(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE t (id int)"); err != nil {
		t.Fatal(err)
	}

	lines := runExplainRows(t, ctx, "EXPLAIN (FORMAT JSON) SELECT row_number() OVER (ORDER BY id) FROM t")
	if len(lines) != 1 {
		t.Fatalf("FORMAT JSON produced %d rows, want 1", len(lines))
	}
	var top []map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &top); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, lines[0])
	}
	if len(top) != 1 {
		t.Fatalf("root array has %d entries, want 1", len(top))
	}
	if !jsonPlanHasNodeType(top[0], "WindowAgg") {
		t.Fatalf("EXPLAIN JSON missing WindowAgg node: %+v", top[0])
	}
}

func jsonPlanHasNodeType(node map[string]any, needle string) bool {
	if nodeType, ok := node["Node Type"].(string); ok && strings.HasPrefix(nodeType, needle) {
		return true
	}
	children, ok := node["Plans"].([]any)
	if !ok {
		return false
	}
	for _, child := range children {
		cm, ok := child.(map[string]any)
		if !ok {
			continue
		}
		if jsonPlanHasNodeType(cm, needle) {
			return true
		}
	}
	return false
}

// (Stage A's ANALYZE-rejection tests were replaced by the
// instrumentation tests below when M0018-0003 lifted the gate.
// `EXPLAIN ANALYZE` now executes the inner statement and reports
// per-node actual-rows / loops / timing.)
