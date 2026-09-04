package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
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

// TestExplainQualifiesUpperFilter: a qual that survives ON a join node is an
// upper qual (show_upper_qual), so its column references carry the alias.
//
// M0127-P5.9 pinned this to the legacy enumerator and P5.9-o unpins it, which
// is worth spelling out because the pin was never "the new plan is wrong".
//
// The original fixture's `a.st = 'x'` names ONE relation, so the PG-shaped
// search pushes it down to the scan — what upstream does. Once it is a SCAN
// qual, upstream's `show_scan_qual` (explain.c:2540) deparses it with
// `useprefix = (IsA(SubqueryScan) || es->verbose)`, i.e. UNQUALIFIED, so
// asserting a prefix on the searched arm's `Filter: (st = 'x')` would have
// asserted the opposite of PostgreSQL. The repair — a conjunct spanning both
// relations, which no scan can absorb — was unavailable only because goopg
// printed no `Join Filter:` line for a join's residual.
//
// P5.9-o prints it, so the fixture is now that cross-relation shape and the
// test runs on the DEFAULT enumerator. `Join Filter` is emitted through the
// same `es->rtable_size > 1` rule as `Hash Cond`, which is exactly the
// prefixing decision under test here. PG 18.3 prints
// `Join Filter: (a.st < b.st)` for this query.
func TestExplainQualifiesUpperFilter(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN SELECT a.id FROM eq_r a, eq_r b WHERE a.id = b.id AND a.st < b.st")

	got := findLine(lines, "Join Filter:")
	if got == "" {
		t.Fatalf("no Join Filter line in plan:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.Contains(got, "a.st") || !strings.Contains(got, "b.st") {
		t.Errorf("upper qual left a column unqualified: %q\nplan:\n%s",
			got, strings.Join(lines, "\n"))
	}
}

// TestExplainQualifiesCorrelatedOuterRef is the Q30/Q81 shape reduced to a
// fixture: a correlated subquery whose scan qual compares the inner
// relation's column against the same-named column of an outer one. PG prints
// `(ctr1.ctr_state = ctr_state)` — outer side qualified, inner side bare —
// and that asymmetry is the whole diagnostic value, so the test pins both
// halves.
//
// M0125-0041 note on the aggregate spelling: this fixture used `max(c2.id)`
// until the scalar pull-up learned to clone a CTE reference, at which point
// Q30's own spelling stopped producing a SubPlan at all — it decorrelates
// into a GROUP BY + hash join, and the correlated Filter line under test
// ceases to exist. The rule being pinned here belongs to the SubPlan path,
// so the fixture pins it with `count(*)`: canUnnestSubquery rejects the Star
// spelling permanently (count returns 0 over an empty group, so the
// INNER-join rewrite would be wrong), which keeps this a correlated SubPlan
// no matter how far decorrelation coverage grows.
func TestExplainQualifiesCorrelatedOuterRef(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN WITH c AS (SELECT id, st FROM eq_r) "+
			"SELECT c1.id FROM c c1 "+
			"WHERE c1.id > (SELECT count(*) FROM c c2 WHERE c2.st = c1.st)")

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
// :65438): TPC-DS Q30 and Q81 render this line byte-identically to PG's
// `Filter: (ctr1.ctr_state = ctr_state)` whenever they take the SubPlan path.
//
// M0125-0041: as in TestExplainQualifiesCorrelatedOuterRef, the aggregate is
// `count(*)` rather than Q30's `max`/`avg` so the shape stays a SubPlan — the
// pull-up now decorrelates the CTE-referencing scalar sublink, and a
// decorrelated plan has no correlated Filter line to qualify. The wall this
// test documents (a CTE body ending in an aggregate hands both sides
// SourceTableIdx 0, so resolveInAncestor is the only way to name the outer
// side) is unchanged by that.
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
			"WHERE c1.ctot > (SELECT count(*) FROM c c2 WHERE c2.cst = c1.cst)")

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

// M0128-P5.1 — when a relation is scanned twice without an alias (e.g. a
// subquery scanning the same table the outer query also scans), the second
// scan's node label must carry a `_1` suffix so the two lines are
// distinguishable. PG does this via select_rtable_names_for_explain
// (ruleutils.c).
func TestExplainNodeLabelDisambiguatesRepeatedTable(t *testing.T) {
	lines := qualifyExplainLines(t,
		"EXPLAIN (COSTS OFF) SELECT * FROM eq_r WHERE id IN (SELECT id FROM eq_r)")

	// Count Seq Scan lines that mention eq_r — there should be at
	// least two, and one should carry the disambiguated suffix.
	hasBare := false
	hasDisambiguated := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "->  ") {
			trimmed = trimmed[4:]
		}
		if strings.HasPrefix(trimmed, "Seq Scan on eq_r_1") {
			hasDisambiguated = true
		} else if strings.HasPrefix(trimmed, "Seq Scan on eq_r") {
			hasBare = true
		}
	}
	if !hasBare {
		t.Errorf("expected one Seq Scan on eq_r (bare) line\nplan:\n%s",
			strings.Join(lines, "\n"))
	}
	if !hasDisambiguated {
		t.Errorf("expected one Seq Scan on eq_r_1 (disambiguated) line\nplan:\n%s",
			strings.Join(lines, "\n"))
	}
}

// P0-04 — an index scan over an aliased relation must carry the alias in its
// node header, as PG's select_rtable_names does. The SeqScan arm always did;
// the index arms dropped it, so a self-join's second scan rendered a bare
// second `on customer` where PG prints `on customer c2` (live case: NLI
// probe over customer c2). Hermetic: hand-built nodes through the same
// header arms the walker uses, so no planner shape is assumed.
func TestExplainIndexScanHeaderShowsAlias(t *testing.T) {
	tbl := &catalog.Table{Name: "customer", Schema: "public"}
	idx := &catalog.Index{Name: "customer_pk"}
	n := &optimizer.IndexScan{Table: tbl, Index: idx, Alias: "c2"}
	nm := newExplainNames(n)

	if got := describePlan(n, nm); got != "Index Scan using customer_pk on customer c2" {
		t.Errorf("plain header = %q, want %q", got, "Index Scan using customer_pk on customer c2")
	}
	if got := describePlanVerbose(n, true, nm); got != "Index Scan using customer_pk on public.customer c2" {
		t.Errorf("verbose header = %q, want %q", got, "Index Scan using customer_pk on public.customer c2")
	}

	// No alias: rendering unchanged (bare table, schema rules as before).
	bare := &optimizer.IndexScan{Table: tbl, Index: idx}
	bm := newExplainNames(bare)
	if got := describePlan(bare, bm); got != "Index Scan using customer_pk on customer" {
		t.Errorf("bare plain header = %q, want %q", got, "Index Scan using customer_pk on customer")
	}
	if got := describePlanVerbose(bare, true, bm); got != "Index Scan using customer_pk on public.customer" {
		t.Errorf("bare verbose header = %q, want %q", got, "Index Scan using customer_pk on public.customer")
	}

	// Alias identical to the table name: no redundant suffix (SeqScan rule).
	self := &optimizer.IndexScan{Table: tbl, Index: idx, Alias: "customer"}
	sm := newExplainNames(self)
	if got := describePlan(self, sm); got != "Index Scan using customer_pk on customer" {
		t.Errorf("self-aliased header = %q, want %q", got, "Index Scan using customer_pk on customer")
	}
}

// A-01(i) — same alias rule for the index-only-scan header. The planner
// stamps IndexOnlyScan.Alias at every IndexScan/SeqScan→IOS promotion
// site (mirroring IndexScan.Alias, M0062-0002); the two min/max-agg
// synthesis sites leave it empty (fresh inner scan, no alias in scope).
// Hermetic like its IndexScan twin: hand-built nodes, no planner shape.
func TestExplainIndexOnlyScanHeaderShowsAlias(t *testing.T) {
	tbl := &catalog.Table{Name: "customer", Schema: "public"}
	idx := &catalog.Index{Name: "customer_pk"}
	n := &optimizer.IndexOnlyScan{Table: tbl, Index: idx, Alias: "c2"}
	nm := newExplainNames(n)

	if got := describePlan(n, nm); got != "Index Only Scan using customer_pk on customer c2" {
		t.Errorf("plain header = %q, want %q", got, "Index Only Scan using customer_pk on customer c2")
	}
	if got := describePlanVerbose(n, true, nm); got != "Index Only Scan using customer_pk on public.customer c2" {
		t.Errorf("verbose header = %q, want %q", got, "Index Only Scan using customer_pk on public.customer c2")
	}

	// No alias: rendering unchanged (bare table, schema rules as before).
	bare := &optimizer.IndexOnlyScan{Table: tbl, Index: idx}
	bm := newExplainNames(bare)
	if got := describePlan(bare, bm); got != "Index Only Scan using customer_pk on customer" {
		t.Errorf("bare plain header = %q, want %q", got, "Index Only Scan using customer_pk on customer")
	}
	if got := describePlanVerbose(bare, true, bm); got != "Index Only Scan using customer_pk on public.customer" {
		t.Errorf("bare verbose header = %q, want %q", got, "Index Only Scan using customer_pk on public.customer")
	}

	// Alias identical to the table name: no redundant suffix (SeqScan rule).
	self := &optimizer.IndexOnlyScan{Table: tbl, Index: idx, Alias: "customer"}
	sm := newExplainNames(self)
	if got := describePlan(self, sm); got != "Index Only Scan using customer_pk on customer" {
		t.Errorf("self-aliased header = %q, want %q", got, "Index Only Scan using customer_pk on customer")
	}
}

// P0-04 — same alias rule for the bitmap heap scan header.
func TestExplainBitmapHeapScanHeaderShowsAlias(t *testing.T) {
	tbl := &catalog.Table{Name: "customer", Schema: "public"}
	n := &optimizer.BitmapHeapScan{Table: tbl, Alias: "c2"}
	nm := newExplainNames(n)

	if got := describePlan(n, nm); got != "Bitmap Heap Scan on customer c2" {
		t.Errorf("header = %q, want %q", got, "Bitmap Heap Scan on customer c2")
	}
}

// P0-04 — a self-join probe's Index Cond qualifies the outer key side:
// `(c_nationkey = c1.c_nationkey)`, not the self-comparison
// `(c_nationkey = c_nationkey)` PG would never print. The index-column side
// stays bare (catalog name, as PG prints it). Hermetic: the probe key is
// rendered straight through formatIndexCond with a two-binding name table.
func TestExplainIndexCondQualifiesOuterProbeKey(t *testing.T) {
	tbl := &catalog.Table{Name: "customer", Schema: "public"}
	idx := &catalog.Index{Name: "customer_nation_fkidx", Columns: []string{"c_nationkey"}}
	key := &optimizer.ColumnRef{Name: "c_nationkey", Index: 0, SourceTableIdx: 1}
	nm := &explainNames{
		bySource: map[int32]string{1: "c1", 2: "c2"},
		bySrc:    map[int16]int32{1: 1, 2: 2},
		cols: map[int32]map[string]bool{
			1: {"c_nationkey": true},
			2: {"c_nationkey": true},
		},
	}
	reg := &subPlanReg{rel: nm}
	scan := &optimizer.IndexScan{Table: tbl, Index: idx, Alias: "c2", Key: key}

	if got := formatIndexCond(scan, reg); got != "(c_nationkey = c1.c_nationkey)" {
		t.Errorf("index cond = %q, want %q", got, "(c_nationkey = c1.c_nationkey)")
	}

	// Single-relation scan: the key side stays bare, as before.
	solo := &explainNames{
		bySource: map[int32]string{1: "customer"},
		bySrc:    map[int16]int32{1: 1},
		cols:     map[int32]map[string]bool{1: {"c_nationkey": true}},
	}
	if got := formatIndexCond(scan, &subPlanReg{rel: solo}); got != "(c_nationkey = c_nationkey)" {
		t.Errorf("single-relation index cond = %q, want %q", got, "(c_nationkey = c_nationkey)")
	}
}

// P0-04e — FORMAT JSON emits the same TREE as the text walker: Project and
// Filter wrappers collapse in both, because PG has neither node. The
// collapsed predicate is preserved as the surviving node's "Filter"
// property (text: the `Filter:` detail line), not dropped with the wrapper.
func TestJSONCollapsesProjectFilterWrappersLikeText(t *testing.T) {
	tbl := parallelLabelTestTable(t, "t")
	scan := &optimizer.SeqScan{Table: tbl, EstRelRows: 10000}
	pred := &optimizer.BinaryOp{
		Op:    parser.OpEq,
		Left:  &optimizer.ColumnRef{Name: "a"},
		Right: &optimizer.IntegerConst{Value: 42},
	}
	proj := &optimizer.Project{
		Child:   &optimizer.Filter{Child: scan, Predicate: pred},
		Targets: []optimizer.Expr{&optimizer.ColumnRef{Name: "a"}},
	}

	// TEXT: no wrapper node lines, but the predicate survives as detail.
	text := renderPlain(t, proj)
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		trimmed = strings.TrimPrefix(trimmed, "->  ")
		if strings.HasPrefix(trimmed, "Projection") || strings.HasPrefix(trimmed, "Filter ") {
			t.Errorf("text walker emitted a wrapper node line %q:\n%s", line, text)
		}
	}
	if findLine(strings.Split(text, "\n"), "Filter: (a = 42)") == "" {
		t.Errorf("text walker dropped the collapsed predicate:\n%s", text)
	}

	// JSON: same tree — no wrapper Node Types anywhere, Filter preserved.
	var types []string
	var filters []string
	var walkJSON func(m map[string]any)
	walkJSON = func(m map[string]any) {
		if nt, ok := m["Node Type"].(string); ok {
			types = append(types, nt)
		}
		if f, ok := m["Filter"].(string); ok {
			filters = append(filters, f)
		}
		if plans, ok := m["Plans"].([]map[string]any); ok {
			for _, c := range plans {
				walkJSON(c)
			}
		}
	}
	obj := planToJSON(proj, parser.ExplainOptions{})
	walkJSON(obj)
	for _, nt := range types {
		if nt == "Projection" || nt == "Filter" {
			t.Errorf("JSON tree contains wrapper node type %q (types: %v)", nt, types)
		}
	}
	if len(types) != 1 || !strings.HasPrefix(types[0], "Seq Scan on t") {
		t.Errorf("JSON tree = %v, want a single Seq Scan node", types)
	}
	if len(filters) != 1 || filters[0] != "(a = 42)" {
		t.Errorf("JSON Filter properties = %v, want [(a = 42)]", filters)
	}
}
