package executor

import (
	"testing"
)

// TestDefaultPartitionRouting is a targeted regression test for DEFAULT partition routing.
// Reproduces the failing subselect.sql pattern:
//
//	create temp table exists_tbl (c1 int, c2 int, c3 int) partition by list (c1);
//	create temp table exists_tbl_null partition of exists_tbl for values in (null);
//	create temp table exists_tbl_def partition of exists_tbl default;
//	insert into exists_tbl select x, x/2, x+1 from generate_series(0,10) x;
//	select * from exists_tbl t1
//	  where (exists(select 1 from exists_tbl t2 where t1.c1 = t2.c2) or c3 < 0);
//
// Root cause: the parser only accepted "FOR VALUES DEFAULT"; the bare "DEFAULT"
// short form (CREATE TABLE child PARTITION OF parent DEFAULT) produced a syntax
// error, so the default partition child was never created and all rows were
// written to the parent rather than routed anywhere useful.
// Fix: accept bare DEFAULT directly after PARTITION OF parent (before trying
// to consume FOR VALUES).
func TestDefaultPartitionRouting(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()

	for _, s := range []string{
		"CREATE TABLE exists_tbl (c1 int, c2 int, c3 int) PARTITION BY LIST (c1)",
		"CREATE TABLE exists_tbl_null PARTITION OF exists_tbl FOR VALUES IN (null)",
		"CREATE TABLE exists_tbl_def PARTITION OF exists_tbl DEFAULT",
		"INSERT INTO exists_tbl SELECT x, x/2, x+1 FROM generate_series(0,10) x",
	} {
		if err := runDDL(t, ctx, s); err != nil {
			t.Fatalf("runDDL(%q): %v", s, err)
		}
	}

	rows := runQuery(t, ctx,
		"SELECT * FROM exists_tbl t1 WHERE (EXISTS(SELECT 1 FROM exists_tbl t2 WHERE t1.c1 = t2.c2) OR c3 < 0)")
	if len(rows) != 6 {
		t.Fatalf("expected 6 rows (c1=0..5), got %d", len(rows))
	}
}
