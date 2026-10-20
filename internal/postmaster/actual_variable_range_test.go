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
// half-way into it: PG 18.3 estimates 98 of the 19778 rows it scales
// reltuples to.
//
// The same workload pins M0146-0237's estimate_rel_size: reltuples (10000) and
// relpages (45) are stale, the heap has 89 pages, and PG scales the stored
// density to them — rel->tuples 19778, rel->pages 89 — so a plain scan costs
// 0.00..286.78, and a relative ndistinct resolves against 19778 (`id < 9500`
// is 18788 rows, not the 18787 an eq_selec over 10000 gives). Every line below
// is PG 18.3's EXPLAIN on the same statements.
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
	if got := estimate("SELECT * FROM ep WHERE id > 15000"); !strings.HasPrefix(got, "98 ") {
		t.Fatalf("id > 15000 estimated %s; want 98 — the probed maximum 20000 puts 15000 half-way into the last bin (1 = the stale histogram's clamp)", got)
	}
	// Inside the histogram the probe is not consulted.
	if got := estimate("SELECT * FROM ep WHERE id > 9000"); !strings.HasPrefix(got, "1978 ") {
		t.Fatalf("id > 9000 estimated %s; want 1978", got)
	}
	for q, want := range map[string]string{
		"SELECT * FROM ep":                 "Seq Scan on ep  (cost=0.00..286.78 rows=19778 width=8)",
		"SELECT * FROM ep WHERE id < 9500": "Seq Scan on ep  (cost=0.00..336.23 rows=18788 width=8)",
		"SELECT * FROM ep WHERE v > 3":     "Seq Scan on ep  (cost=0.00..336.23 rows=8475 width=8)",
	} {
		var line string
		if err := db.QueryRowContext(ctx, "EXPLAIN "+q).Scan(&line); err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		if line != want {
			t.Errorf("EXPLAIN %s:\n got %s\nwant %s (PG 18.3: estimate_rel_size scales the stale reltuples to the current 89 pages)", q, line, want)
		}
	}
}
