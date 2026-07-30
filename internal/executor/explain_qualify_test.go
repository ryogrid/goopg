package executor

import (
	"strings"
	"testing"
)

// M0125-0039 — EXPLAIN printed column references unqualified, so a real
// correlation between two relations rendered as a self-comparison.
//
// The reading a triage loop needs is exactly the one the old output
// destroyed. TPC-DS Q30 printed `Filter: (ctr_state = ctr_state)` where
// PostgreSQL 18.3 prints `Filter: (ctr1.ctr_state = ctr_state)`; nothing in
// goopg's line distinguished "two columns of a self-join" from "a predicate
// that can never be true". Same for Q64's
// `(cd_marital_status <> cd_marital_status)`, Q72's
// `(d_week_seq = d_week_seq)` and Q31's repeated `d_qoy`.
//
// The rule reproduced here is upstream's, not an invention. explain.c splits
// the decision by node kind: show_scan_qual deparses a scan's Filter with
// varprefix=false, while show_upper_qual and show_sort_group_keys use
// `es->rtable_size > 1`. Separately, ruleutils.c's get_parameter forces
// prefixing while deparsing a Param's expansion — goopg's OuterColumnRef —
// which is why Q30's outer side is qualified even though its scan qual is
// not.

// qualifyExplainFixture creates one table and returns a Context; every case
// below self-joins it, since a self-join is the only shape where the bare
// column name is genuinely ambiguous.
func qualifyExplainLines(t *testing.T, sql string) []string {
	t.Helper()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)

	runSQL(t, ctx, "CREATE TABLE eq_r (id int, st text)")
	runSQL(t, ctx, "INSERT INTO eq_r VALUES (1, 'a'), (2, 'b')")

	return runExplainRows(t, ctx, sql)
}

// findLine returns the first plan line containing want, or "".
func findLine(lines []string, want string) string {
	for _, l := range lines {
		if strings.Contains(l, want) {
			return strings.TrimSpace(l)
		}
	}
	return ""
}

// TestExplainQualifiesUpperQualOnSelfJoin is the core regression: a
// predicate on a join node names both sides' relations, so two distinct
// columns no longer render as one.
func TestExplainQualifiesUpperQualOnSelfJoin(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT a.id FROM eq_r a, eq_r b WHERE a.id = b.id ORDER BY a.st, b.st")

	got := findLine(lines, "Sort Key:")
	if got == "" {
		t.Fatalf("no Sort Key line in plan:\n%s", strings.Join(lines, "\n"))
	}
	if got != "Sort Key: a.st, b.st" {
		t.Errorf("sort key not qualified per relation:\n got %q\nwant %q\nplan:\n%s",
			got, "Sort Key: a.st, b.st", strings.Join(lines, "\n"))
	}
}

// TestExplainQualifiesUpperFilter: a Filter that survives above a join is an
// upper qual (show_upper_qual), so its column references carry the alias.
func TestExplainQualifiesUpperFilter(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT a.id FROM eq_r a, eq_r b WHERE a.id = b.id AND a.st = 'x'")

	got := findLine(lines, "Filter:")
	if got == "" {
		t.Fatalf("no Filter line in plan:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(got, "a.st") {
		t.Errorf("upper Filter left its column unqualified: %q\nplan:\n%s",
			got, strings.Join(lines, "\n"))
	}
}

// TestExplainQualifiesCorrelatedOuterRef is the Q30/Q81 shape reduced to a
// fixture: a correlated subquery whose scan qual compares the inner
// relation's column against the same-named column of an outer one. PG prints
// `(ctr1.ctr_state = ctr_state)` — outer side qualified, inner side bare —
// and that asymmetry is the whole diagnostic value, so the test pins both
// halves.
func TestExplainQualifiesCorrelatedOuterRef(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN WITH c AS (SELECT id, st FROM eq_r) "+
			"SELECT c1.id FROM c c1 "+
			"WHERE c1.id > (SELECT max(c2.id) FROM c c2 WHERE c2.st = c1.st)")

	got := findLine(lines, "st = ")
	if got == "" {
		t.Fatalf("no correlated filter line in plan:\n%s", strings.Join(lines, "\n"))
	}
	if got == "Filter: (st = st)" {
		t.Fatalf("correlated filter still renders as a self-comparison: %q\nplan:\n%s",
			got, strings.Join(lines, "\n"))
	}
	if got != "Filter: (st = c1.st)" {
		t.Errorf("correlated filter shape diverged:\n got %q\nwant %q\nplan:\n%s",
			got, "Filter: (st = c1.st)", strings.Join(lines, "\n"))
	}
}

// TestExplainQualifiesOuterRefThroughAggregate is the real Q30 shape, and
// the reason the SourceTableIdx table alone is not enough. When the CTE body
// ends in an aggregate, its output columns carry SourceTableIdx 0 ("no
// identity assigned"), so BOTH sides of the correlation arrive with nothing
// to look up. Upstream hits the same wall and answers it by deparsing the
// Param against the ancestor plan node (push_ancestor_plan); goopg mirrors
// that with explainNames.resolveInAncestor.
//
// Verified against PostgreSQL 18.3 on the TPC-DS SF=0.5 cluster (:65437 vs
// :65438): TPC-DS Q30 and Q81 now render this line byte-identically to PG's
// `Filter: (ctr1.ctr_state = ctr_state)`.
func TestExplainQualifiesOuterRefThroughAggregate(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE eq_x (id int, st text)")
	runSQL(t, ctx, "CREATE TABLE eq_y (id int, amt int)")

	lines := runExplainRows(t, ctx,
		"EXPLAIN WITH c AS ("+
			"SELECT eq_x.st AS cst, sum(eq_y.amt) AS ctot FROM eq_x, eq_y "+
			"WHERE eq_x.id = eq_y.id GROUP BY eq_x.st) "+
			"SELECT c1.ctot FROM c c1 "+
			"WHERE c1.ctot > (SELECT max(c2.ctot) FROM c c2 WHERE c2.cst = c1.cst)")

	got := findLine(lines, "cst = ")
	if got == "Filter: (cst = cst)" {
		t.Fatalf("outer ref through an aggregate still renders as a self-comparison: %q\nplan:\n%s",
			got, strings.Join(lines, "\n"))
	}
	if got != "Filter: (cst = c1.cst)" {
		t.Errorf("correlated filter shape diverged:\n got %q\nwant %q\nplan:\n%s",
			got, "Filter: (cst = c1.cst)", strings.Join(lines, "\n"))
	}
}

// TestExplainLeavesSingleRelationQualsBare pins the other half of upstream's
// rule. A one-relation query has `es->rtable_size == 1`, so PG prints bare
// column names — and so must goopg, or every existing EXPLAIN expectation in
// the tree would shift for no diagnostic gain.
func TestExplainLeavesSingleRelationQualsBare(t *testing.T) {
	lines := qualifyExplainLines(t, "EXPLAIN SELECT id FROM eq_r WHERE st = 'a'")

	got := findLine(lines, "Filter:")
	if got != "Filter: (st = 'a')" {
		t.Errorf("single-relation filter should stay unqualified:\n got %q\nwant %q\nplan:\n%s",
			got, "Filter: (st = 'a')", strings.Join(lines, "\n"))
	}
}

// TestExplainLeavesScanQualBare pins the scan-vs-upper split: even in a
// multi-relation query, a qual attached to a scan node renders bare, because
// upstream's show_scan_qual passes varprefix=false.
func TestExplainLeavesScanQualBare(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT a.id FROM eq_r a WHERE a.st > 'a' AND a.id IN (SELECT b.id FROM eq_r b WHERE b.st < 'z')")

	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if !strings.HasPrefix(trimmed, "Filter:") {
			continue
		}
		// Only scan nodes carry a bare `st`/`id` filter here; if any of
		// them acquired a prefix, the scan/upper split is wrong.
		if strings.Contains(trimmed, "a.st >") || strings.Contains(trimmed, "b.st <") {
			t.Errorf("scan qual was qualified, upstream keeps it bare: %q\nplan:\n%s",
				trimmed, strings.Join(lines, "\n"))
		}
	}
}

// TestExplainDoesNotQualifyDerivedColumns guards the hazard the
// implementation probe found: planner.go restarts its SourceTableIdx counter
// at 1 for every query level, so a flattened subquery's binding can share an
// id with a base relation inside it. Qualifying blindly would print a
// confident, wrong relation name — worse than the bare name it replaced.
// The column-membership guard degrades those refs back to unqualified.
func TestExplainDoesNotQualifyDerivedColumns(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT t.s1 FROM (SELECT a.st AS s1, b.st AS s2 FROM eq_r a, eq_r b WHERE a.id = b.id) t "+
			"WHERE t.s1 <> t.s2")

	got := findLine(lines, "s1 <> s2")
	if got == "" {
		t.Fatalf("derived-column filter missing or wrongly qualified:\n%s", strings.Join(lines, "\n"))
	}
	if strings.Contains(got, "a.s2") || strings.Contains(got, "b.s1") {
		t.Errorf("derived column attributed to the wrong relation: %q", got)
	}
}

// TestExplainNamesDisambiguateRepeatedRelation: two unaliased scans of the
// same relation must not both print the bare table name, or the qualifier
// carries no information. Upstream's set_rtable_names leaves the first bare
// and suffixes the rest (`date_dim`, `date_dim_1`).
func TestExplainNamesDisambiguateRepeatedRelation(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT eq_r.id FROM eq_r, eq_r x WHERE eq_r.id = x.id ORDER BY eq_r.st, x.st")

	got := findLine(lines, "Sort Key:")
	if got != "Sort Key: eq_r.st, x.st" {
		t.Errorf("repeated relation not disambiguated:\n got %q\nwant %q\nplan:\n%s",
			got, "Sort Key: eq_r.st, x.st", strings.Join(lines, "\n"))
	}
}
