package executor

import (
	"encoding/json"
	"strings"
	"testing"
)

// M0125-0037(i) — set operations were opaque to EXPLAIN.
//
// Before this change `describePlan` had no `*planner.SetOp` case (so the
// node printed as the raw Go type name `*planner.SetOp`) and `planChildren`
// had none either (so the branches were never walked). TPC-DS Q5/Q18/Q67
// therefore rendered as four-line plans with the whole query body invisible,
// which is why M0125-0026's plan capture could not classify them at all.
//
// The expected spellings below were captured from PostgreSQL 18.3 on the
// TPC-DS reference cluster (port 65438), not derived from explain.c by
// reading alone:
//
//	UNION ALL  -> Append              (two Seq Scan children)
//	UNION      -> HashAggregate over Append
//	INTERSECT  -> HashSetOp Intersect (two Seq Scan children, no Append)
//	EXCEPT ALL -> HashSetOp Except All
//
// goopg fuses PG's SetOp+Append into one node, so the UNION-distinct case
// prints `HashSetOp Union` — see describePlan's comment and the deferral
// ledger row for why that spelling was chosen.

// setopExplainFixture creates two same-shaped tables and returns the
// EXPLAIN lines for sql.
func setopExplainLines(t *testing.T, sql string) []string {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)

	runSQL(t, ctx, "CREATE TABLE eso_a (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_b (id int, y int)")
	runSQL(t, ctx, "CREATE TABLE eso_c (id int, y int)")
	runSQL(t, ctx, "INSERT INTO eso_a VALUES (1, 1), (2, 2)")
	runSQL(t, ctx, "INSERT INTO eso_b VALUES (2, 2), (3, 3)")
	runSQL(t, ctx, "INSERT INTO eso_c VALUES (4, 4)")

	return runExplainRows(t, ctx, sql)
}

// TestExplainUnionAllRendersAppendWithBranches is the core regression: a
// UNION ALL must render as PG's `Append` and its branches must be visible.
func TestExplainUnionAllRendersAppendWithBranches(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b")

	joined := strings.Join(lines, "\n")
	if strings.Contains(joined, "optimizer.SetOp") {
		t.Fatalf("EXPLAIN still prints the raw Go type name:\n%s", joined)
	}
	if !strings.Contains(lines[0], "Append") {
		t.Fatalf("expected root line to be Append, got %q (all: %v)", lines[0], lines)
	}
	for _, tbl := range []string{"eso_a", "eso_b"} {
		if !strings.Contains(joined, "Seq Scan on "+tbl) {
			t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
		}
	}
}

// TestExplainUnionAllChainFlattensToOneAppend: goopg builds
// `a UNION ALL b UNION ALL c` as SetOp(SetOp(a,b),c), but PG plans one
// Append with three children. Without flattening, TPC-DS Q5's five-branch
// union would render five Append levels deep and no longer diff against
// PG's plan.
func TestExplainUnionAllChainFlattensToOneAppend(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b UNION ALL SELECT id FROM eso_c")

	appends := 0
	for _, l := range lines {
		if strings.Contains(l, "Append") {
			appends++
		}
	}
	if appends != 1 {
		t.Errorf("expected exactly 1 Append line for a 3-branch UNION ALL, got %d:\n%s",
			appends, strings.Join(lines, "\n"))
	}
	joined := strings.Join(lines, "\n")
	for _, tbl := range []string{"eso_a", "eso_b", "eso_c"} {
		if !strings.Contains(joined, "Seq Scan on "+tbl) {
			t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
		}
	}
}

// TestExplainIntersectExceptRenderHashSetOp covers the commands PG spells
// with an explicit SetOp node. The `All` suffix is part of PG's label
// (explain.c SETOPCMD_INTERSECT_ALL / SETOPCMD_EXCEPT_ALL).
func TestExplainIntersectExceptRenderHashSetOp(t *testing.T) {
	cases := []struct {
		sql  string
		want string
	}{
		{"SELECT id FROM eso_a INTERSECT SELECT id FROM eso_b", "HashSetOp Intersect"},
		{"SELECT id FROM eso_a INTERSECT ALL SELECT id FROM eso_b", "HashSetOp Intersect All"},
		{"SELECT id FROM eso_a EXCEPT SELECT id FROM eso_b", "HashSetOp Except"},
		{"SELECT id FROM eso_a EXCEPT ALL SELECT id FROM eso_b", "HashSetOp Except All"},
		// goopg-only spelling: PG plans this as HashAggregate over Append.
		{"SELECT id FROM eso_a UNION SELECT id FROM eso_b", "HashSetOp Union"},
	}
	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			lines := setopExplainLines(t, "EXPLAIN "+tc.sql)
			if !strings.Contains(lines[0], tc.want) {
				t.Fatalf("expected root line to contain %q, got %q", tc.want, lines[0])
			}
			joined := strings.Join(lines, "\n")
			for _, tbl := range []string{"eso_a", "eso_b"} {
				if !strings.Contains(joined, "Seq Scan on "+tbl) {
					t.Errorf("branch scan on %s missing from plan:\n%s", tbl, joined)
				}
			}
		})
	}
}

// TestExplainSetOpChildrenIndentUnderParent guards the property that made
// Q5 unreadable: the branches must be rendered as CHILDREN (one "->" level
// deeper), not as siblings of the set-op line.
func TestExplainSetOpChildrenIndentUnderParent(t *testing.T) {
	lines := setopExplainLines(t,
		"EXPLAIN SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b")

	rootIndent := len(lines[0]) - len(strings.TrimLeft(lines[0], " "))
	for _, l := range lines[1:] {
		if !strings.Contains(l, "Seq Scan on eso_") {
			continue
		}
		ind := len(l) - len(strings.TrimLeft(l, " "))
		if ind <= rootIndent {
			t.Errorf("branch line is not indented under the Append root:\n%s",
				strings.Join(lines, "\n"))
		}
	}
}

// TestExplainSetOpJSONMatchesUpstreamProperties: unlike the TEXT format,
// upstream's JSON does not fold the command into the node name — it emits
// "Node Type": "SetOp" plus separate "Strategy" and "Command" properties
// (verified against PG 18.3). A UNION ALL has no SetOp node upstream at
// all and keeps the plain "Append".
func TestExplainSetOpJSONMatchesUpstreamProperties(t *testing.T) {
	decode := func(t *testing.T, lines []string) map[string]any {
		t.Helper()
		var doc []map[string]any
		if err := json.Unmarshal([]byte(strings.Join(lines, "\n")), &doc); err != nil {
			t.Fatalf("EXPLAIN (FORMAT JSON) is not valid JSON: %v\n%s", err, strings.Join(lines, "\n"))
		}
		if len(doc) != 1 {
			t.Fatalf("expected 1 JSON plan object, got %d", len(doc))
		}
		plan, ok := doc[0]["Plan"].(map[string]any)
		if !ok {
			t.Fatalf("no Plan object in %v", doc[0])
		}
		return plan
	}

	intersect := decode(t, setopExplainLines(t,
		"EXPLAIN (FORMAT JSON) SELECT id FROM eso_a INTERSECT ALL SELECT id FROM eso_b"))
	if got := intersect["Node Type"]; got != "SetOp" {
		t.Errorf(`Node Type = %v, want "SetOp"`, got)
	}
	if got := intersect["Strategy"]; got != "Hashed" {
		t.Errorf(`Strategy = %v, want "Hashed"`, got)
	}
	if got := intersect["Command"]; got != "Intersect All" {
		t.Errorf(`Command = %v, want "Intersect All"`, got)
	}
	if plans, ok := intersect["Plans"].([]any); !ok || len(plans) != 2 {
		t.Errorf("expected 2 child plans under the SetOp, got %v", intersect["Plans"])
	}

	unionAll := decode(t, setopExplainLines(t,
		"EXPLAIN (FORMAT JSON) SELECT id FROM eso_a UNION ALL SELECT id FROM eso_b"))
	if got := unionAll["Node Type"]; got != "Append" {
		t.Errorf(`Node Type = %v, want "Append"`, got)
	}
	if _, present := unionAll["Command"]; present {
		t.Errorf("Append must not carry a Command property: %v", unionAll)
	}
}
