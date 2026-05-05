package tpch_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// hammerdbIndexes mirrors the HammerDB TPC-H supplementary index set
// (the PK indexes are added separately via ALTER TABLE). Order
// matters for deterministic EXPLAIN output across runs.
//
// This list is the authoritative reference for M0054-0002 and the
// mirror is documented in `analysis/tpch-additional-indexes.md` and
// `analysis/tpch-hammerdb-run-011.md` §3.
var hammerdbIndexes = []string{
	`CREATE INDEX idx_nation_regionkey       ON nation   (n_regionkey)`,
	`CREATE INDEX idx_part_type              ON part     (p_type)`,
	`CREATE INDEX idx_part_size              ON part     (p_size)`,
	`CREATE INDEX idx_supplier_nationkey     ON supplier (s_nationkey)`,
	`CREATE INDEX idx_customer_nationkey     ON customer (c_nationkey)`,
	`CREATE INDEX idx_customer_mktsegment    ON customer (c_mktsegment)`,
	`CREATE INDEX idx_orders_custkey         ON orders   (o_custkey)`,
	`CREATE INDEX idx_orders_orderdate       ON orders   (o_orderdate)`,
	`CREATE INDEX idx_lineitem_orderkey      ON lineitem (l_orderkey)`,
	`CREATE INDEX idx_lineitem_partkey       ON lineitem (l_partkey)`,
	`CREATE INDEX idx_lineitem_suppkey       ON lineitem (l_suppkey)`,
	`CREATE INDEX idx_lineitem_shipdate      ON lineitem (l_shipdate)`,
	`CREATE INDEX idx_lineitem_commitdate    ON lineitem (l_commitdate)`,
	`CREATE INDEX idx_lineitem_receiptdate   ON lineitem (l_receiptdate)`,
	`CREATE INDEX idx_partsupp_partkey       ON partsupp (ps_partkey)`,
	`CREATE INDEX idx_partsupp_suppkey       ON partsupp (ps_suppkey)`,
}

// hammerdbPKs adds PRIMARY KEY constraints via ALTER TABLE. Composite
// PKs are intentionally included so M0053-0001's leading-column
// support is exercised by the audit.
var hammerdbPKs = []string{
	`ALTER TABLE region   ADD CONSTRAINT region_pk   PRIMARY KEY (r_regionkey)`,
	`ALTER TABLE nation   ADD CONSTRAINT nation_pk   PRIMARY KEY (n_nationkey)`,
	`ALTER TABLE supplier ADD CONSTRAINT supplier_pk PRIMARY KEY (s_suppkey)`,
	`ALTER TABLE customer ADD CONSTRAINT customer_pk PRIMARY KEY (c_custkey)`,
	`ALTER TABLE part     ADD CONSTRAINT part_pk     PRIMARY KEY (p_partkey)`,
	`ALTER TABLE partsupp ADD CONSTRAINT partsupp_pk PRIMARY KEY (ps_partkey, ps_suppkey)`,
	`ALTER TABLE orders   ADD CONSTRAINT orders_pk   PRIMARY KEY (o_orderkey)`,
	`ALTER TABLE lineitem ADD CONSTRAINT lineitem_pk PRIMARY KEY (l_linenumber, l_orderkey)`,
}

// TestTPCHIndexUtilisationBaseline (M0054-0002) loads the TPC-H schema
// + sample data + HammerDB-equivalent indexes, runs ANALYZE, then
// captures `EXPLAIN (FORMAT JSON)` for each Q1..Q22. The aggregated
// per-query plan-shape summary is written to
// `analysis/tpch-explain-baseline.md` so M0054-0003 has a concrete
// gap list to attack.
//
// The test asserts only that EXPLAIN succeeded for every query — the
// report content itself is the audit artifact; downstream M0054-0003
// sub-tasks compare against it manually.
func TestTPCHIndexUtilisationBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cluster-backed TPC-H index audit in short mode")
	}
	repoRoot := repoRoot(t)
	base := t.TempDir()
	c, err := cluster.New("tpch-index-audit", cluster.Options{
		RepoRoot:     repoRoot,
		DataDir:      filepath.Join(base, "data"),
		StartupWait:  30 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()

	// Schema + data + indexes + ANALYZE.
	for _, ddl := range tpch.DDL() {
		if _, err := c.Query(ctx, ddl); err != nil {
			t.Fatalf("DDL %q: %v", firstWords(ddl, 6), err)
		}
	}
	for _, ins := range tpch.SampleInserts() {
		if _, err := c.Query(ctx, ins); err != nil {
			t.Fatalf("INSERT %q: %v", firstWords(ins, 4), err)
		}
	}
	for _, pk := range hammerdbPKs {
		if _, err := c.Query(ctx, pk); err != nil {
			t.Logf("PK %q: %v (continuing — some PKs require additional planner support)", firstWords(pk, 6), err)
		}
	}
	for _, idx := range hammerdbIndexes {
		if _, err := c.Query(ctx, idx); err != nil {
			t.Logf("INDEX %q: %v (continuing — index will simply not appear in plans)", firstWords(idx, 6), err)
		}
	}
	for _, tbl := range []string{"region", "nation", "supplier", "customer", "part", "partsupp", "orders", "lineitem"} {
		if _, err := c.Query(ctx, "ANALYZE "+tbl); err != nil {
			t.Logf("ANALYZE %s: %v (continuing)", tbl, err)
		}
	}

	// Capture per-query EXPLAIN JSON.
	queries := tpch.Queries()
	results := make([]qResult, 0, 22)
	// captureExplain runs EXPLAIN (FORMAT JSON) on a single SELECT
	// and appends the result under the given label.
	captureExplain := func(num int, label, sql string) {
		rows, err := c.Query(ctx, "EXPLAIN (FORMAT JSON) "+sql)
		if err != nil {
			results = append(results, qResult{num: num, label: label, ok: false, err: truncate(err.Error(), 200)})
			return
		}
		if len(rows) == 0 || len(rows[0]) == 0 {
			results = append(results, qResult{num: num, label: label, ok: false, err: "EXPLAIN returned no rows"})
			return
		}
		shape, perr := parseExplainJSON(rows[0][0])
		if perr != nil {
			results = append(results, qResult{num: num, label: label, ok: false, err: "parse: " + perr.Error()})
			return
		}
		results = append(results, qResult{num: num, label: label, ok: true, summary: shape})
	}

	for q := 1; q <= 22; q++ {
		sql := queries[q]
		label := fmt.Sprintf("Q%d", q)
		// Q15 special case (M0054-0003a): the `Queries()[15]` slot is
		// the CREATE OR REPLACE VIEW sub-statement of HammerDB's Q15.
		// EXPLAIN cannot wrap it directly, so we instead:
		//   1. execute the CREATE VIEW so revenue0 exists,
		//   2. EXPLAIN the VIEW body as Q15a (the lineitem range scan),
		//   3. EXPLAIN the main SELECT as Q15b (supplier+revenue0 join),
		//   4. drop the view as cleanup.
		// This recovers the index-usage signal that the previous
		// "non-SELECT slot" skip was hiding.
		if q == 15 {
			if _, err := c.Query(ctx, sql); err != nil {
				results = append(results, qResult{num: 15, label: "Q15a", ok: false, err: "CREATE VIEW failed: " + truncate(err.Error(), 160)})
				results = append(results, qResult{num: 15, label: "Q15b", ok: false, err: "CREATE VIEW failed (skipped)"})
				continue
			}
			captureExplain(15, "Q15a", tpch.Q15ViewBody())
			captureExplain(15, "Q15b", tpch.Q15MainSelect())
			if _, err := c.Query(ctx, "DROP VIEW revenue0"); err != nil {
				t.Logf("DROP VIEW revenue0: %v (continuing — Q15 cleanup is best-effort)", err)
			}
			continue
		}
		// Other CREATE/DROP slots — defensive guard, not currently hit
		// because no other query starts with CREATE/DROP.
		lower := strings.ToLower(strings.TrimSpace(sql))
		if strings.HasPrefix(lower, "create ") || strings.HasPrefix(lower, "drop ") {
			results = append(results, qResult{num: q, label: label, ok: false, err: "non-SELECT slot — EXPLAIN not applicable"})
			continue
		}
		captureExplain(q, label, sql)
	}

	// Assertion: every SELECT slot must EXPLAIN. A failure here means
	// the planner choked on a query the executor previously ran (M0053
	// tests cover executor success); we do NOT assert plan shape here
	// — the report is the artifact downstream M0054-0003 sub-tasks
	// consume.
	for _, r := range results {
		if r.ok {
			continue
		}
		if strings.Contains(r.err, "non-SELECT slot") {
			continue
		}
		t.Errorf("%s EXPLAIN failed: %s", r.label, r.err)
	}

	// Render the report.
	reportPath := filepath.Join(repoRoot, "analysis", "tpch-explain-baseline.md")
	if err := writeExplainBaselineReport(reportPath, results); err != nil {
		t.Errorf("write report: %v", err)
	}
	t.Logf("M0054-0002 baseline report: %s", reportPath)
}

// qResult records the EXPLAIN outcome for one TPC-H query slot.
// The label is the rendered query identifier (e.g. "Q1", "Q15a",
// "Q15b") and `num` is the parent query number used as a sort key
// (Q15a/Q15b both have num=15).
type qResult struct {
	num     int
	label   string
	ok      bool
	err     string
	summary planShape
}

// planShape is the per-query summary extracted from EXPLAIN (FORMAT JSON).
type planShape struct {
	rootNodeType string
	scans        []scanEntry // ordered by visitation
}

type scanEntry struct {
	NodeType  string // "Seq Scan" / "Index Scan" / "Index Only Scan"
	Table     string
	IndexName string // empty when not an Index Scan
}

// parseExplainJSON extracts the plan-shape summary from an EXPLAIN
// (FORMAT JSON) output string.
func parseExplainJSON(jsonStr string) (planShape, error) {
	var top []map[string]interface{}
	if err := json.Unmarshal([]byte(jsonStr), &top); err != nil {
		return planShape{}, err
	}
	if len(top) == 0 {
		return planShape{}, fmt.Errorf("empty EXPLAIN array")
	}
	root, _ := top[0]["Plan"].(map[string]interface{})
	if root == nil {
		// goopg's EXPLAIN currently returns the plan node directly at
		// the top level rather than wrapping it under "Plan" — handle
		// both shapes.
		root = top[0]
	}
	shape := planShape{}
	if nt, ok := root["Node Type"].(string); ok {
		shape.rootNodeType = nt
	}
	walkExplainNode(root, &shape.scans)
	return shape, nil
}

// walkExplainNode visits every node in the plan tree, recording any
// Seq Scan / Index Scan / Index Only Scan it encounters. goopg's
// EXPLAIN JSON renders `Node Type` as a single descriptive string
// like `"Seq Scan on lineitem"` or
// `"Index Scan using lineitem_pk on lineitem"` (see
// `internal/executor/operators_explain.go::describePlan`), so this
// walker parses that string rather than reading separate
// `Relation Name` / `Index Name` fields.
func walkExplainNode(node map[string]interface{}, out *[]scanEntry) {
	if node == nil {
		return
	}
	if nt, ok := node["Node Type"].(string); ok {
		if e, hit := classifyNodeType(nt); hit {
			*out = append(*out, e)
		}
	}
	if children, ok := node["Plans"].([]interface{}); ok {
		for _, ch := range children {
			if m, ok := ch.(map[string]interface{}); ok {
				walkExplainNode(m, out)
			}
		}
	}
}

// classifyNodeType pattern-matches goopg's `Node Type` strings.
func classifyNodeType(label string) (scanEntry, bool) {
	const seqPrefix = "Seq Scan on "
	if strings.HasPrefix(label, seqPrefix) {
		rest := label[len(seqPrefix):]
		// Trailing " (stats)" decoration is harmless metadata.
		rest = strings.TrimSuffix(rest, " (stats)")
		return scanEntry{NodeType: "Seq Scan", Table: rest}, true
	}
	const idxPrefix = "Index Scan using "
	if strings.HasPrefix(label, idxPrefix) {
		rest := label[len(idxPrefix):]
		// "<idx> on <table>"
		if onIdx := strings.Index(rest, " on "); onIdx > 0 {
			return scanEntry{NodeType: "Index Scan", IndexName: rest[:onIdx], Table: rest[onIdx+4:]}, true
		}
		return scanEntry{NodeType: "Index Scan", IndexName: rest}, true
	}
	const ioPrefix = "Index Only Scan using "
	if strings.HasPrefix(label, ioPrefix) {
		rest := label[len(ioPrefix):]
		if onIdx := strings.Index(rest, " on "); onIdx > 0 {
			return scanEntry{NodeType: "Index Only Scan", IndexName: rest[:onIdx], Table: rest[onIdx+4:]}, true
		}
		return scanEntry{NodeType: "Index Only Scan", IndexName: rest}, true
	}
	return scanEntry{}, false
}

// writeExplainBaselineReport renders the M0054-0002 markdown summary
// from the per-query results list.
func writeExplainBaselineReport(path string, results []qResult) error {
	var b strings.Builder
	fmt.Fprintln(&b, "# TPC-H EXPLAIN Baseline (M0054-0002)")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "Generated by `TestTPCHIndexUtilisationBaseline` against the synthetic\n")
	fmt.Fprintf(&b, "TPC-H sample-data fixture (`internal/testutil/tpch/SampleInserts`).\n")
	fmt.Fprintln(&b, "Each query was run through `EXPLAIN (FORMAT JSON)`; this file lists,")
	fmt.Fprintln(&b, "per query, every Seq Scan / Index Scan / Index Only Scan in the plan tree.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Per-query plan shapes")
	fmt.Fprintln(&b)
	for _, r := range results {
		label := r.label
		if label == "" {
			label = fmt.Sprintf("Q%d", r.num)
		}
		fmt.Fprintf(&b, "### %s\n\n", label)
		if !r.ok {
			fmt.Fprintf(&b, "EXPLAIN unavailable: `%s`\n\n", r.err)
			continue
		}
		fmt.Fprintf(&b, "Root node: `%s`\n\n", r.summary.rootNodeType)
		if len(r.summary.scans) == 0 {
			fmt.Fprintln(&b, "No scan nodes — plan likely uses Values / no underlying tables.")
			fmt.Fprintln(&b)
			continue
		}
		fmt.Fprintln(&b, "| # | Node Type | Table | Index |")
		fmt.Fprintln(&b, "|---|-----------|-------|-------|")
		for i, s := range r.summary.scans {
			idx := s.IndexName
			if idx == "" {
				idx = "—"
			}
			fmt.Fprintf(&b, "| %d | %s | %s | %s |\n", i+1, s.NodeType, s.Table, idx)
		}
		fmt.Fprintln(&b)
	}

	// Aggregate gap section: top SeqScan-on-indexed-table cases.
	gaps := computeIndexGaps(results)
	fmt.Fprintln(&b, "## Aggregate gaps")
	fmt.Fprintln(&b)
	if len(gaps) == 0 {
		fmt.Fprintln(&b, "No SeqScan entries on tables with documented HammerDB indexes —")
		fmt.Fprintln(&b, "every queryable index is at least sometimes used. Note: the")
		fmt.Fprintln(&b, "synthetic SampleInserts dataset is small enough that the planner")
		fmt.Fprintln(&b, "may legitimately prefer SeqScan over IndexScan for some queries;")
		fmt.Fprintln(&b, "this report bakes in the *current* plan shape, not a target shape.")
		fmt.Fprintln(&b)
	} else {
		fmt.Fprintln(&b, "Tables with HammerDB-equivalent indexes that still appear under")
		fmt.Fprintln(&b, "`Seq Scan` in one or more Q1..Q22 plans. Each row is a candidate")
		fmt.Fprintln(&b, "for an M0054-0003 sub-task investigation.")
		fmt.Fprintln(&b)
		fmt.Fprintln(&b, "| Table | Seq Scan queries | Index Scan queries |")
		fmt.Fprintln(&b, "|-------|------------------|--------------------|")
		for _, g := range gaps {
			fmt.Fprintf(&b, "| %s | %s | %s |\n", g.table, joinLabels(g.seqLabels), joinLabels(g.idxLabels))
		}
		fmt.Fprintln(&b)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

type tableGap struct {
	table      string
	seqLabels  []string
	idxLabels  []string
}

// computeIndexGaps groups scan entries by table and reports tables
// where SeqScan happened in some queries even though IndexScan was
// possible (or did happen) in others. Labels are query identifiers
// like "Q1" / "Q15a" so multi-statement slots (M0054-0003a) surface
// the correct sub-entry.
func computeIndexGaps(results []qResult) []tableGap {
	type entry struct {
		seq []string
		idx []string
	}
	per := map[string]*entry{}
	for _, r := range results {
		if !r.ok {
			continue
		}
		label := r.label
		if label == "" {
			label = fmt.Sprintf("Q%d", r.num)
		}
		for _, s := range r.summary.scans {
			if s.Table == "" {
				continue
			}
			e, ok := per[s.Table]
			if !ok {
				e = &entry{}
				per[s.Table] = e
			}
			switch s.NodeType {
			case "Seq Scan":
				e.seq = append(e.seq, label)
			case "Index Scan", "Index Only Scan":
				e.idx = append(e.idx, label)
			}
		}
	}
	out := make([]tableGap, 0, len(per))
	for tbl, e := range per {
		if len(e.seq) == 0 {
			continue
		}
		out = append(out, tableGap{table: tbl, seqLabels: dedupSortedStrings(e.seq), idxLabels: dedupSortedStrings(e.idx)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].table < out[j].table })
	return out
}

func dedupSortedStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	sorted := append([]string(nil), in...)
	sort.Strings(sorted)
	out := []string{sorted[0]}
	for _, v := range sorted[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func joinLabels(in []string) string {
	if len(in) == 0 {
		return "—"
	}
	return strings.Join(in, ", ")
}
