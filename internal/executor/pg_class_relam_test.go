package executor

import "testing"

// TestPgClassRelamFollowsRelkindHasTableAM pins pg_class.relam against
// RELKIND_HAS_TABLE_AM (pg_class.h, M0149-0001h1a16): a table, its TOAST
// table and a materialized view carry heap (2); a view, sequence and
// partitioned table carry InvalidOid. type_sanity checks both directions.
func TestPgClassRelamFollowsRelkindHasTableAM(t *testing.T) {
	for kind, want := range map[string]int64{"r": 2, "t": 2, "m": 2, "v": 0, "S": 0, "p": 0, "c": 0, "f": 0} {
		if got := relamFor(kind); got != want {
			t.Errorf("relamFor(%q) = %d, want %d", kind, got, want)
		}
	}
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()
	for _, ddl := range []string{
		"CREATE TABLE ra_t (a int, b text)",
		"CREATE VIEW ra_v AS SELECT a FROM ra_t",
		"CREATE TABLE ra_p (a int) PARTITION BY LIST (a)",
	} {
		if err := runDDL(t, ctx, ddl); err != nil {
			t.Fatalf("%s: %v", ddl, err)
		}
	}
	rows := runQueryRows(t, ctx,
		"SELECT relname, relkind, relam FROM pg_catalog.pg_class WHERE relname IN ('ra_t', 'ra_v', 'ra_p') ORDER BY relname")
	want := map[string]int64{"ra_t": 2, "ra_v": 0, "ra_p": 0}
	if len(rows) != len(want) {
		t.Fatalf("got %d pg_class rows, want %d", len(rows), len(want))
	}
	for _, r := range rows {
		if got := r[2].Int; got != want[r[0].StringValue()] {
			t.Errorf("%s (relkind %s) relam = %d, want %d", r[0].StringValue(), r[1].StringValue(), got, want[r[0].StringValue()])
		}
	}
	toast := runQueryRows(t, ctx,
		"SELECT c.relam FROM pg_catalog.pg_class c JOIN pg_catalog.pg_class t ON t.reltoastrelid = c.oid WHERE t.relname = 'ra_t'")
	if len(toast) != 1 {
		t.Fatalf("ra_t TOAST rows = %d, want 1", len(toast))
	}
	for _, r := range toast {
		if r[0].Int != 2 {
			t.Errorf("ra_t's TOAST relation relam = %d, want 2", r[0].Int)
		}
	}
}
