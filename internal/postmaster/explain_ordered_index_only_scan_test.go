package postmaster

import (
	"strings"
	"testing"
)

// TestExplainOrderedIndexOnlyScan pins M0146-0188 against PG 18.3.
//
// build_index_paths (indxpath.c) builds one path per index with the index's
// useful pathkeys and makes it index-only when check_index_only allows, so a
// full index scan that delivers the ORDER BY is an Index Only Scan. goopg filed
// its full-index index-only path without pathkeys; the ordered plain Index Scan
// (addOneOrderedIndexPath) then won every ordering contest — TPC-DS Q77's
// web_page_pkey scans driving its nested loops printed as Index Scans.
//
// PG 18.3 prints `Limit -> Index Only Scan using io_t_pkey on io_t`.
func TestExplainOrderedIndexOnlyScan(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	for _, q := range []string{
		"CREATE TABLE io_t (k int PRIMARY KEY, pad text)",
		"INSERT INTO io_t SELECT g, repeat('x', 50) FROM generate_series(1, 20000) g",
		"VACUUM ANALYZE io_t",
		"SET max_parallel_workers_per_gather = 0",
	} {
		if _, e := allRowsQuery(t, conn, q); e != "" {
			t.Fatalf("%s: %s", q, e)
		}
	}
	got, e := allRowsQuery(t, conn, "EXPLAIN (COSTS OFF) SELECT k FROM io_t ORDER BY k LIMIT 10")
	if e != "" {
		t.Fatal(e)
	}
	plan := strings.Join(got, "\n")
	if !strings.Contains(plan, "->  Index Only Scan using io_t_pkey on io_t") || strings.Contains(plan, "Sort") {
		t.Errorf("PG 18.3 plans Limit -> Index Only Scan using io_t_pkey:\n%s", plan)
	}
	rows, e := allRowsQuery(t, conn, "SELECT k FROM io_t ORDER BY k LIMIT 3")
	if e != "" {
		t.Fatal(e)
	}
	if strings.Join(rows, ",") != "1,2,3" {
		t.Errorf("ORDER BY k LIMIT 3 = %v, want 1,2,3", rows)
	}
}
