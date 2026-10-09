package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/parser"
)

// TestVacuumAnalyzeCollectsColumnStatistics pins M0146-0009j against PG 18.3's
// vacuum(): with VACOPT_ANALYZE it runs analyze_rel on every target right
// after vacuum_rel, so `VACUUM ANALYZE t` leaves the same per-column
// statistics `ANALYZE t` does. goopg's VACUUM used to stop at the
// relation-size pass (reltuples/relpages), leaving pg_stats empty — which made
// every plan of the regress suite (test_setup.sql VACUUM ANALYZEs its tables)
// stats-less. A bare `VACUUM ANALYZE` walks every relation, catalogs included,
// and must still succeed.
func TestVacuumAnalyzeCollectsColumnStatistics(t *testing.T) {
	ctx, cat, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, q := range []string{
		"CREATE TABLE vas (id int, grp int)",
		"INSERT INTO vas SELECT g, g % 7 FROM generate_series(1, 700) g",
	} {
		runSQL(t, ctx, q)
	}
	runSQL(t, ctx, "VACUUM ANALYZE vas")
	tbl, ok := cat.LookupTable(parser.ObjectName{Name: "vas"})
	if !ok {
		t.Fatal("vas not found")
	}
	if tbl.Stats == nil || len(tbl.Stats.Columns) < 2 {
		t.Fatalf("VACUUM ANALYZE left no column statistics: %+v", tbl.Stats)
	}
	if got := tbl.Stats.Columns[0].NDistinct; got != 700 {
		t.Errorf("id NDistinct=%d want 700", got)
	}
	if got := tbl.Stats.Columns[1].NDistinct; got != 7 {
		t.Errorf("grp NDistinct=%d want 7", got)
	}
	runSQL(t, ctx, "VACUUM ANALYZE")
	runSQL(t, ctx, "VACUUM (ANALYZE) vas")
}
