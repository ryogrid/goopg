package postmaster

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestInequalityEstimateReadsActualIndexEndpoints pins M0146-0233's
// get_actual_variable_range, end to end through the hook initdb wires.
//
// ineq_histogram_selectivity (selfuncs.c) replaces a histogram end with the
// column's actual current minimum or maximum, read from a btree leading on the
// column, whenever its binary search reaches that end. ANALYZE ran when ep
// held ids 1..10000; another 10000 rows were then inserted. For `id > 15000`
// the stale histogram says nothing qualifies, so without the probe the
// estimate is clamped to a hundredth of a bin (0.0001 of the rows: 1 row).
// With the probe the last bin runs to the actual maximum 20000, and 15000 is
// half-way into it: PG 18.3 estimates a selectivity of 0.00495 (98 of the
// 19778 rows it scales reltuples to). goopg keeps reltuples at 10000 (no
// current-pages scaling, M0146-0237), so the same selectivity is 50 rows.
func TestInequalityEstimateReadsActualIndexEndpoints(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	for _, stmt := range []string{
		"CREATE TABLE ep (id int PRIMARY KEY, v int) WITH (autovacuum_enabled = false)",
		"INSERT INTO ep SELECT g, g % 7 FROM generate_series(1, 10000) g",
		"ANALYZE ep",
		"INSERT INTO ep SELECT g, g % 7 FROM generate_series(10001, 20000) g",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rowsRe := regexp.MustCompile(`rows=(\d+)`)
	estimate := func(q string) string {
		var line string
		if err := db.QueryRowContext(ctx, "EXPLAIN "+q).Scan(&line); err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		m := rowsRe.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("no row estimate in %q", line)
		}
		return m[1] + " (" + strings.TrimSpace(line) + ")"
	}
	if got := estimate("SELECT * FROM ep WHERE id > 15000"); !strings.HasPrefix(got, "50 ") {
		t.Fatalf("id > 15000 estimated %s; want 50 — the probed maximum 20000 puts 15000 half-way into the last bin (1 = the stale histogram's clamp)", got)
	}
	// Inside the histogram the probe is not consulted.
	if got := estimate("SELECT * FROM ep WHERE id > 9000"); !strings.HasPrefix(got, "1000 ") {
		t.Fatalf("id > 9000 estimated %s; want 1000", got)
	}
}
