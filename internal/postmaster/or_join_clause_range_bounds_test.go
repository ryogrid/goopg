package postmaster

import (
	"context"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestORJoinClauseArmPairsRangeBounds pins M0146-0241: an OR whose arms read
// two relations is a join clause, and PG prices each arm (an AND) through
// clauselist_selectivity_ext, which pairs the bounds of one variable into a
// range — `oa.x BETWEEN 100 AND 199` is hibound + lobound - 1, not the product
// of the two halves (clausesel.c addRangeClause). goopg multiplied them, so a
// two-arm OR read about twice PG's rows (TPC-DS Q13's customer_address join:
// 31665 against 16054). Each expected row count is PG 18.3's EXPLAIN on the
// same statements; every table is small enough that ANALYZE reads all of it.
func TestORJoinClauseArmPairsRangeBounds(t *testing.T) {
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
		"CREATE TABLE oa (a int, x int) WITH (autovacuum_enabled = false)",
		"CREATE TABLE ob (b int, s text) WITH (autovacuum_enabled = false)",
		"INSERT INTO oa SELECT g, g % 1000 FROM generate_series(1, 20000) g",
		"INSERT INTO ob SELECT g, (ARRAY['p','q','r','s'])[1 + g % 4] FROM generate_series(1, 20000) g",
		"ANALYZE oa",
		"ANALYZE ob",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	rowsRe := regexp.MustCompile(`rows=(\d+)`)
	for q, want := range map[string]string{
		"SELECT * FROM oa JOIN ob ON oa.a = ob.b WHERE (ob.s = 'p' AND oa.x BETWEEN 100 AND 199) OR (ob.s = 'q' AND oa.x BETWEEN 500 AND 549)": "743",
		"SELECT * FROM oa JOIN ob ON oa.a = ob.b WHERE (ob.s = 'p' AND oa.x >= 100 AND oa.x < 300) OR (ob.s IN ('q','r') AND oa.x > 900)":      "1940",
	} {
		var line string
		if err := db.QueryRowContext(ctx, "EXPLAIN "+q).Scan(&line); err != nil {
			t.Fatalf("EXPLAIN %s: %v", q, err)
		}
		m := rowsRe.FindStringSubmatch(line)
		if m == nil || m[1] != want {
			t.Errorf("EXPLAIN %s:\n got %s\nwant rows=%s (PG 18.3)", q, line, want)
		}
	}
}
