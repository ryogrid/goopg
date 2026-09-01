package executor

import "testing"

// TestPgOptionsToTableLateral pins review/260831-2 EO2-6:
// pg_options_to_table was the one FROM-clause SRF missing from BOTH lateral
// mechanisms — the planner's nodeReferencesOuter had no *PgOptionsToTable
// case (so the wrapping Join was never marked Lateral and no outer row was
// ever bound) and the operator implemented no BindLateralOuter. An explicit
// `LATERAL pg_options_to_table(t.opts)` therefore failed with
// "XX000: column ref opts/1 on nil slot". PG 18.3 returns (1,a,1) (1,b,2)
// (2,c,3) for the fixture below.
func TestPgOptionsToTableLateral(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	if err := runDDL(t, ctx, `CREATE TABLE zopts (id int, opts text[])`); err != nil {
		t.Fatalf("CREATE TABLE: %v", err)
	}
	runSQL(t, ctx, `INSERT INTO zopts VALUES (1, ARRAY['a=1','b=2']), (2, ARRAY['c=3'])`)

	rows, err := runSQLCtxErr(t, ctx,
		`SELECT z.id, t.option_name, t.option_value FROM zopts z, LATERAL pg_options_to_table(z.opts) t ORDER BY 1, 2`)
	if err != nil {
		t.Fatalf("LATERAL pg_options_to_table: %v", err)
	}
	want := [][3]string{{"1", "a", "1"}, {"1", "b", "2"}, {"2", "c", "3"}}
	if len(rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(rows), len(want))
	}
	for i, w := range want {
		got := [3]string{rows[i][0].Format(), rows[i][1].Format(), rows[i][2].Format()}
		if got != w {
			t.Errorf("row %d = %v, want %v", i, got, w)
		}
	}

	// The correlated-subquery spelling (pg_dump's own usage, which rides
	// ctx.OuterRows rather than the bound slot) must keep working.
	rows, err = runSQLCtxErr(t, ctx,
		`SELECT z.id, (SELECT count(*) FROM pg_options_to_table(z.opts)) FROM zopts z ORDER BY 1`)
	if err != nil {
		t.Fatalf("correlated pg_options_to_table: %v", err)
	}
	if len(rows) != 2 || rows[0][1].Format() != "2" || rows[1][1].Format() != "1" {
		t.Errorf("correlated counts = %v, want 2 then 1", rows)
	}
}
