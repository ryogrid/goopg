package executor

import (
	"strings"
	"testing"
)

// TestExplainUpdateShowsScanChild verifies planChildren walks an
// UPDATE plan's target-table scan (previously EXPLAIN silently
// dropped it — real PG always shows the underlying Seq/Index Scan
// under "Update on ..."). Regression for the operators_explain.go
// planChildren gap (M0122 backlog).
func TestExplainUpdateShowsScanChild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE eud_t (id int primary key, y int)")
	runSQL(t, ctx, "INSERT INTO eud_t VALUES (1, 2), (2, 3)")

	lines := runExplainRows(t, ctx, "EXPLAIN UPDATE eud_t SET y = 1 WHERE y = 2")
	if !strings.Contains(lines[0], "Update on") {
		t.Fatalf("expected root line to be Update on ..., got %q", lines[0])
	}
	found := false
	for _, l := range lines[1:] {
		if strings.Contains(l, "Scan on") && strings.Contains(l, "eud_t") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a child scan-on-eud_t line, got lines: %v", lines)
	}
}

// TestExplainDeleteShowsScanChild is the DELETE sibling of
// TestExplainUpdateShowsScanChild.
func TestExplainDeleteShowsScanChild(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE eud_d (id int primary key, y int)")
	runSQL(t, ctx, "INSERT INTO eud_d VALUES (1, 2), (2, 3)")

	lines := runExplainRows(t, ctx, "EXPLAIN DELETE FROM eud_d WHERE y = 2")
	if !strings.Contains(lines[0], "Delete on") {
		t.Fatalf("expected root line to be Delete on ..., got %q", lines[0])
	}
	found := false
	for _, l := range lines[1:] {
		if strings.Contains(l, "Scan on") && strings.Contains(l, "eud_d") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a child scan-on-eud_d line, got lines: %v", lines)
	}
}

// TestExplainUpdateFromShowsFromScanChildren covers UPDATE ...
// FROM: the FROM-table scans (p.FromScans) must also be walked,
// not just the target-table Child.
func TestExplainUpdateFromShowsFromScanChildren(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE eud_target (id int primary key, y int)")
	runSQL(t, ctx, "CREATE TABLE eud_src (id int primary key, y int)")
	runSQL(t, ctx, "INSERT INTO eud_target VALUES (1, 0)")
	runSQL(t, ctx, "INSERT INTO eud_src VALUES (1, 9)")

	lines := runExplainRows(t, ctx, "EXPLAIN UPDATE eud_target SET y = eud_src.y FROM eud_src WHERE eud_target.id = eud_src.id")
	sawTarget, sawSrc := false, false
	for _, l := range lines[1:] {
		if strings.Contains(l, "Scan on") && strings.Contains(l, "eud_target") {
			sawTarget = true
		}
		if strings.Contains(l, "Scan on") && strings.Contains(l, "eud_src") {
			sawSrc = true
		}
	}
	if !sawTarget || !sawSrc {
		t.Fatalf("expected scans on both eud_target and eud_src, got lines: %v", lines)
	}
}
