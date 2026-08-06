package planner

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
)

// M0125-0040 ("C6"): the grouping-sets expansion used to give every generated
// UNION ALL branch its own copy of FROM+WHERE, so an N-set construct scanned
// the source N times (measured: 5 full catalog_sales scans for TPC-DS Q18,
// 9 full store_sales scans for Q67). shareGroupingSetsSource hoists the source
// into one synthetic materialized CTE that all branches read.
//
// These tests pin the two halves that matter and are cheap to state
// structurally: the rewrite APPLIES on the shapes it was built for, and it
// DECLINES — leaving the statement byte-for-byte as before — on every shape
// whose safety has not been established. The answers themselves are pinned by
// the executor's grouping-sets compat tests, which now run through this path.

func gsShareCatalog(t *testing.T) catalog.Catalog {
	t.Helper()
	c := catalog.NewInMemory()
	if _, err := c.CreateTable(parser.ObjectName{Name: "gs_sales"}, []catalog.Column{
		{Name: "item_sk", Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		{Name: "date_sk", Type: catalog.Type{Name: "int8"}, Ordinal: 1},
		{Name: "qty", Type: catalog.Type{Name: "int8"}, Ordinal: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "gs_item"}, []catalog.Column{
		{Name: "i_item_sk", Type: catalog.Type{Name: "int8"}, Ordinal: 0},
		{Name: "i_class", Type: catalog.Type{Name: "text"}, Ordinal: 1},
		{Name: "i_brand", Type: catalog.Type{Name: "text"}, Ordinal: 2},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateTable(parser.ObjectName{Name: "gs_other"}, []catalog.Column{
		{Name: "o_key", Type: catalog.Type{Name: "int8"}, Ordinal: 0},
	}); err != nil {
		t.Fatal(err)
	}
	return c
}

// gsPlan parses and plans sql, failing the test on either error.
func gsPlan(t *testing.T, cat catalog.Catalog, sql string) Node {
	t.Helper()
	stmts, err := parser.Parse(sql)
	if err != nil {
		t.Fatalf("parse %q: %v", sql, err)
	}
	n, err := Plan(stmts[0], cat)
	if err != nil {
		t.Fatalf("plan %q: %v", sql, err)
	}
	return n
}

// gsCollectCTEScans returns every CTEScan in the plan whose name is one of
// this rewrite's synthetic sources.
func gsCollectCTEScans(n Node) []*CTEScan {
	var out []*CTEScan
	if s, ok := n.(*CTEScan); ok && strings.HasPrefix(strings.ToLower(s.Name), "__gs_src_") {
		out = append(out, s)
	}
	for _, c := range planChildren(n) {
		out = append(out, gsCollectCTEScans(c)...)
	}
	return out
}

// gsCountSeqScansOf counts SeqScan nodes over the named table.
func gsCountSeqScansOf(n Node, table string) int {
	count := 0
	if s, ok := n.(*SeqScan); ok && s.Table != nil && strings.EqualFold(s.Table.Name, table) {
		count++
	}
	for _, c := range planChildren(n) {
		count += gsCountSeqScansOf(c, table)
	}
	return count
}

// TestGSShareSourceAppliesToJoinedRollup is the shape M0125-0040 was filed
// for: a multi-table comma-FROM with a WHERE, rolled up over columns from
// both tables. All four generated branches must read the same synthetic
// source, which is what makes the executor's CTERowCache replay the join
// instead of re-running it (operators_cte_dml.go, cteScanOp.Open).
//
// The assertion is on CTEScan.DeclKey, the cache key itself, and not merely on
// the name — M0125-0050 narrowed that key from the CTE name to the declaration
// site, and the branches do NOT share a *plannedCTE: planSelect re-enters on
// the head operand of the set-op chain it just built, so the one synthetic
// `__gs_src_N` declaration is planned twice. Sharing therefore rests on both
// passes deriving the same key from the same declaration, which is precisely
// what this pins. Keying by declSeq or by body pointer compiles, passes a
// name-only assertion, and silently doubles the join.
func TestGSShareSourceAppliesToJoinedRollup(t *testing.T) {
	cat := gsShareCatalog(t)
	plan := gsPlan(t, cat, `
		SELECT i_class, i_brand, sum(qty)
		FROM gs_sales, gs_item
		WHERE item_sk = i_item_sk AND date_sk > 10
		GROUP BY ROLLUP(i_class, i_brand)`)

	scans := gsCollectCTEScans(plan)
	// ROLLUP(a,b) is three sets, so three branches, each with one reference.
	if len(scans) != 3 {
		t.Fatalf("synthetic-source references = %d, want 3 (one per generated branch)", len(scans))
	}
	key := scans[0].DeclKey()
	for _, s := range scans[1:] {
		if s.DeclKey() != key {
			t.Fatalf("branches read different sources: DeclKey %q vs %q — they would not share the cache, so the join would run once per branch",
				key, s.DeclKey())
		}
	}
}

// TestGSShareSourceDeclinesOnCorrelatedSubquery is the correctness guard the
// whole rewrite rests on. A CTE body cannot be correlated, so a grouping-sets
// SELECT whose WHERE references an outer relation must keep the per-branch
// expansion. The outer reference is invisible to the rewrite except by the
// fact that it resolves to no FROM-clause table — which is exactly what the
// resolver checks.
func TestGSShareSourceDeclinesOnCorrelatedSubquery(t *testing.T) {
	cat := gsShareCatalog(t)
	plan := gsPlan(t, cat, `
		SELECT o_key FROM gs_other o
		WHERE EXISTS (
			SELECT i_class, sum(qty)
			FROM gs_sales, gs_item
			WHERE item_sk = i_item_sk AND date_sk = o.o_key
			GROUP BY ROLLUP(i_class, i_brand))`)

	if scans := gsCollectCTEScans(plan); len(scans) != 0 {
		t.Fatalf("rewrote a correlated grouping-sets subquery into a CTE (%d refs) — the outer reference would not resolve", len(scans))
	}

	// The plan walk cannot reach inside a SubPlan, so state the decision
	// directly as well: the correlation lives ONLY in WHERE, which the
	// rewrite moves into the CTE body without rewriting it. That is the one
	// place an outer reference can hide from the target-list resolver, and
	// the verify-only walk over WHERE is what catches it.
	stmts, err := parser.Parse(`
		SELECT i_class, sum(qty) FROM gs_sales, gs_item
		WHERE item_sk = i_item_sk AND date_sk = o.o_key
		GROUP BY ROLLUP(i_class, i_brand)`)
	if err != nil {
		t.Fatal(err)
	}
	inner := stmts[0].(*parser.SelectStmt)
	if shareGroupingSetsSource(inner, inner.GroupingSets, cat) {
		t.Fatal("hoisted a WHERE carrying an outer reference into a CTE body — a CTE body cannot be correlated")
	}
	if inner.With != nil || len(inner.From) != 2 || inner.Where == nil {
		t.Fatalf("declined but still mutated the statement: With=%v From=%d Where=%v", inner.With, len(inner.From), inner.Where != nil)
	}
}

// TestGSShareSourceDeclinesOnUnsupportedShapes pins the fail-closed contract
// for the rest of the guard chain. Each of these still plans and still
// answers correctly — it just keeps today's expansion.
func TestGSShareSourceDeclinesOnUnsupportedShapes(t *testing.T) {
	cat := gsShareCatalog(t)
	cases := []struct {
		name string
		sql  string
	}{
		{
			// Explicit JOIN syntax lands in FromExprs, not the flattened
			// From list the resolver walks.
			name: "explicit join",
			sql: `SELECT i_class, sum(qty) FROM gs_sales JOIN gs_item ON item_sk = i_item_sk
			      GROUP BY ROLLUP(i_class, i_brand)`,
		},
		{
			// A sublink in the target list brings its own FROM scope, in
			// which an unqualified name may not mean what the resolver
			// would decide.
			name: "sublink in target list",
			sql: `SELECT i_class, (SELECT max(o_key) FROM gs_other), sum(qty)
			      FROM gs_sales, gs_item WHERE item_sk = i_item_sk
			      GROUP BY ROLLUP(i_class, i_brand)`,
		},
		{
			// A single generated set is a plain GROUP BY: one branch, one
			// scan, nothing to share.
			name: "single set",
			sql:  `SELECT i_class, sum(qty) FROM gs_sales, gs_item WHERE item_sk = i_item_sk GROUP BY GROUPING SETS ((i_class))`,
		},
		{
			// A derived table in FROM has no catalog entry to resolve
			// unqualified names against.
			name: "derived table in from",
			sql: `SELECT i_class, sum(qty) FROM (SELECT * FROM gs_sales) v, gs_item
			      WHERE v.item_sk = i_item_sk GROUP BY ROLLUP(i_class, i_brand)`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := gsPlan(t, cat, tc.sql)
			if scans := gsCollectCTEScans(plan); len(scans) != 0 {
				t.Fatalf("rewrite applied (%d synthetic-source refs) on a shape it must decline", len(scans))
			}
		})
	}
}

// TestGSShareSourceOffRestoresOldPlan pins the reopen path: with the knob off
// the plan is the pre-M0125-0040 one, so an operator who hits a problem on a
// benchmark cluster can get the old planner back without a rebuild.
func TestGSShareSourceOffRestoresOldPlan(t *testing.T) {
	cat := gsShareCatalog(t)
	sql := `SELECT i_class, i_brand, sum(qty) FROM gs_sales, gs_item
	        WHERE item_sk = i_item_sk GROUP BY ROLLUP(i_class, i_brand)`

	prev := gsShareSource.Load()
	gsShareSource.Store(false)
	defer gsShareSource.Store(prev)

	plan := gsPlan(t, cat, sql)
	if scans := gsCollectCTEScans(plan); len(scans) != 0 {
		t.Fatalf("knob off but the rewrite still applied (%d refs)", len(scans))
	}
	if got := gsCountSeqScansOf(plan, "gs_sales"); got != 3 {
		t.Fatalf("gs_sales scans = %d, want 3 (the per-branch expansion)", got)
	}
}

// TestParseGSShareSource pins the off-words and the deliberate
// unparseable-means-default direction (the M0125-0005 convention: a typo may
// never silently hand an operator a planner production does not run).
func TestParseGSShareSource(t *testing.T) {
	for _, v := range []string{"0", "off", "false", "no", "OFF", " no "} {
		if parseGSShareSource(v) {
			t.Fatalf("parseGSShareSource(%q) = true, want false", v)
		}
	}
	for _, v := range []string{"", "1", "on", "true", "yes", "banana"} {
		if !parseGSShareSource(v) {
			t.Fatalf("parseGSShareSource(%q) = false, want true (the shipped default)", v)
		}
	}
}
