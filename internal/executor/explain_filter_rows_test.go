package executor

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// actualRowsOf returns the `rows=N` from the `(actual ...)` suffix of the
// first plan line whose text contains `needle`, plus that line's
// "Rows Removed by Filter" if the following detail lines carry one.
func actualRowsOf(t *testing.T, lines []string, needle string) (rows float64, removed float64, found bool) {
	t.Helper()
	reRows := regexp.MustCompile(`actual (?:time=[\d.]+\.\.[\d.]+ )?rows=([\d.]+)`)
	reRem := regexp.MustCompile(`Rows Removed by Filter: ([\d.]+)`)
	for i, ln := range lines {
		if !strings.Contains(ln, needle) {
			continue
		}
		m := reRows.FindStringSubmatch(ln)
		if m == nil {
			continue
		}
		rows, _ = strconv.ParseFloat(m[1], 64)
		found = true
		for j := i + 1; j < len(lines); j++ {
			if rm := reRem.FindStringSubmatch(lines[j]); rm != nil {
				removed, _ = strconv.ParseFloat(rm[1], 64)
				break
			}
			// stop at the next plan node line
			if strings.Contains(lines[j], "->") {
				break
			}
		}
		return
	}
	return 0, 0, false
}

// TestExplainAnalyzeCollapsedFilterRowsArePostFilter pins the invariant the
// renderer must hold: a node that prints "Rows Removed by Filter: R" must
// report the rows it actually PRODUCED, not the rows its child produced.
//
// PG has no such split — `rows` is instrument->ntuples/nloops counting only
// returned tuples (explain.c:1835, execProcnode.c:487) and nfiltered1 lives on
// the same node (execScan.h:245).
//
// The HashAggregate/HAVING shape is used deliberately: it needs no
// plan-forcing GUC (`SET enable_indexscan = off` does not reliably change the
// plan on this build), and it demonstrates that the defect was never
// scan-specific. Before the fix this printed rows=3 with Rows Removed=2 where
// the true answer is 1.
func TestExplainAnalyzeCollapsedFilterRowsArePostFilter(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE agg_t (g int, v int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "agg_t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	// groups 1,2,3 with 5, 2 and 1 rows respectively.
	for g, n := range map[int64]int{1: 5, 2: 2, 3: 1} {
		for i := 0; i < n; i++ {
			if err := writeHeapRow(ctx, rel, tbl.Columns,
				Row{{Kind: KindInt, Int: g}, {Kind: KindInt, Int: int64(i)}}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// HAVING count(*) > 3 keeps exactly ONE group (g=1, 5 rows) and rejects two.
	lines := runExplainRows(t, ctx,
		"EXPLAIN ANALYZE SELECT g, count(*) FROM agg_t GROUP BY g HAVING count(*) > 3")
	joined := strings.Join(lines, "\n")

	rows, removed, found := actualRowsOf(t, lines, "Aggregate")
	if !found {
		t.Fatalf("no aggregate node with an (actual ...) suffix:\n%s", joined)
	}
	if removed == 0 {
		t.Fatalf("expected a Rows Removed by Filter on the aggregate:\n%s", joined)
	}
	// The invariant: what the node reports producing must be what it produced.
	if removed != 2 {
		t.Errorf("aggregate reports Rows Removed=%.0f; 2 of 3 groups fail the "+
			"HAVING, so it must be 2 (an earlier revision double-counted the "+
			"collapsed filter's rejects and printed 4)\n%s", removed, joined)
	}
	if rows != 1 {
		t.Errorf("aggregate reports rows=%.0f with Rows Removed=%.0f; the query "+
			"returns 1 group, so rows must be 1 (pre-filter row count leaked "+
			"into the collapsed line)\n%s", rows, removed, joined)
	}
	t.Logf("collapsed aggregate: rows=%.0f removed=%.0f (true answer 1)", rows, removed)
}

// TestExplainAnalyzeSeqScanFilterStillReports guards the fallback that the
// first attempt at this fix broke: E-17 cut 2 absorbed the seq-scan qual into
// seqScanOp and deleted its filterOp while KEEPING the *optimizer.Filter plan
// node. Substituting the filter's stats unconditionally therefore dropped the
// entire "(actual ...)" suffix and the Rows Removed line for every seq scan.
func TestExplainAnalyzeSeqScanFilterStillReports(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	if err := runDDL(t, ctx, "CREATE TABLE sq_t (id int)"); err != nil {
		t.Fatal(err)
	}
	tbl, _ := ctx.Catalog.LookupTable(parser.ObjectName{Name: "sq_t"})
	rel := ctx.Catalog.RelFileNode(tbl)
	for n := int64(1); n <= 5; n++ {
		if err := writeHeapRow(ctx, rel, tbl.Columns, Row{{Kind: KindInt, Int: n}}); err != nil {
			t.Fatal(err)
		}
	}
	lines := runExplainRows(t, ctx, "EXPLAIN ANALYZE SELECT * FROM sq_t WHERE id > 3")
	joined := strings.Join(lines, "\n")
	rows, removed, found := actualRowsOf(t, lines, "Seq Scan")
	if !found {
		t.Fatalf("seq scan lost its (actual ...) suffix:\n%s", joined)
	}
	if !strings.Contains(joined, "Rows Removed by Filter:") {
		t.Fatalf("seq scan lost its Rows Removed line:\n%s", joined)
	}
	if rows != 2 || removed != 3 {
		t.Errorf("seq scan: rows=%.0f removed=%.0f, want 2 and 3\n%s", rows, removed, joined)
	}
	_ = fmt.Sprint()
}
