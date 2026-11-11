package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestNullTestIndexQual pins M0148-0001c3a slices 1 and 2: a `col IS [NOT] NULL`
// conjunct on an index's leading column is an index qual (PG's
// match_clause_to_indexcol NullTest arm; btree amsearchnulls), executed as
// SK_SEARCHNULL / SK_SEARCHNOTNULL bounds at the column's NULL group. Every
// indexed answer must equal the same query through a non-indexable twin
// (`(a + 0) IS NULL`), on ASC, NULLS FIRST, DESC and composite indexes;
// EXPLAIN prints PG's `Index Cond`. Without the NULL-keyed-entries capability
// IS NULL cannot use the index (the rows are not in it) and stays a Filter.
func TestNullTestIndexQual(t *testing.T) {
	catalog.SetNullKeyedIndexEntries(true)
	defer catalog.SetNullKeyedIndexEntries(false)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE ntq (a int, b int, c int)")
	runSQL(t, ctx, "INSERT INTO ntq SELECT CASE WHEN g % 10 = 0 THEN NULL ELSE g END, g % 7, g FROM generate_series(1, 5000) g")
	runSQL(t, ctx, "CREATE INDEX ntq_a ON ntq (a)")
	runSQL(t, ctx, "ANALYZE ntq")

	plan := func(q string) string { return strings.Join(runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+q), "\n") }
	same := func(name, indexed, twin string) {
		t.Helper()
		got, want := sortedRowStrings(t, ctx, indexed), sortedRowStrings(t, ctx, twin)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("%s: indexed %d rows, twin %d rows\nplan:\n%s", name, len(got), len(want), plan(indexed))
		}
	}
	for _, tc := range []struct{ q, cond string }{
		{"SELECT * FROM ntq WHERE a IS NULL", "Index Cond: (a IS NULL)"},
		{"SELECT * FROM ntq WHERE a IS NOT NULL AND a < 5", "Index Cond: ((a IS NOT NULL) AND (a < 5))"},
	} {
		if p := plan(tc.q); !strings.Contains(p, tc.cond) || strings.Contains(p, "Filter") {
			t.Errorf("%s: plan\n%s\nwant %q and no Filter", tc.q, p, tc.cond)
		}
	}
	cases := []struct{ name, indexed, twin string }{
		{"is-null", "SELECT * FROM ntq WHERE a IS NULL", "SELECT * FROM ntq WHERE (a + 0) IS NULL"},
		{"not-null-range", "SELECT * FROM ntq WHERE a IS NOT NULL AND a < 5", "SELECT * FROM ntq WHERE (a + 0) IS NOT NULL AND a + 0 < 5"},
		{"not-null-open-low", "SELECT * FROM ntq WHERE a IS NOT NULL AND a > 4990", "SELECT * FROM ntq WHERE (a + 0) IS NOT NULL AND a + 0 > 4990"},
		{"null-and-range-empty", "SELECT * FROM ntq WHERE a IS NULL AND a < 5", "SELECT * FROM ntq WHERE (a + 0) IS NULL AND a + 0 < 5"},
	}
	for _, c := range cases {
		same(c.name, c.indexed, c.twin)
	}

	// NULLS FIRST and DESC put the NULL group at the other end of the index.
	for _, ddl := range []string{"CREATE INDEX ntq_af ON ntq (a NULLS FIRST)", "CREATE INDEX ntq_ad ON ntq (a DESC)"} {
		runSQL(t, ctx, "DROP INDEX IF EXISTS ntq_a")
		runSQL(t, ctx, "DROP INDEX IF EXISTS ntq_af")
		runSQL(t, ctx, ddl)
		for _, c := range cases {
			same(ddl+": "+c.name, c.indexed, c.twin)
		}
		if p := plan("SELECT * FROM ntq WHERE a IS NULL"); !strings.Contains(p, "Index Cond: (a IS NULL)") {
			t.Errorf("%s: IS NULL plan\n%s", ddl, p)
		}
	}

	// Composite index: IS NULL on the leading column takes every trailing key.
	runSQL(t, ctx, "DROP INDEX IF EXISTS ntq_ad")
	runSQL(t, ctx, "CREATE INDEX ntq_ab ON ntq (a, b)")
	same("composite is-null", "SELECT * FROM ntq WHERE a IS NULL", "SELECT * FROM ntq WHERE (a + 0) IS NULL")

	// Slice 2: the index-only producer files the same NullTest index qual
	// (covered columns; an all-visible heap after VACUUM).
	runSQL(t, ctx, "DROP INDEX IF EXISTS ntq_ab")
	runSQL(t, ctx, "CREATE INDEX ntq_a ON ntq (a)")
	runSQL(t, ctx, "VACUUM ANALYZE ntq")
	for _, tc := range []struct{ q, want string }{
		{"SELECT a FROM ntq WHERE a IS NOT NULL ORDER BY a LIMIT 1", "Index Cond: (a IS NOT NULL)"},
		{"SELECT count(*) FROM ntq WHERE a IS NULL", "Index Cond: (a IS NULL)"},
		{"SELECT a FROM ntq WHERE a IS NOT NULL AND a < 5", "Index Cond: ((a IS NOT NULL) AND (a < 5))"},
	} {
		if p := plan(tc.q); !strings.Contains(p, "Index Only Scan") || !strings.Contains(p, tc.want) || strings.Contains(p, "Filter") {
			t.Errorf("%s: plan\n%s\nwant an Index Only Scan with %q and no Filter", tc.q, p, tc.want)
		}
	}
	same("ios not-null order limit", "SELECT a FROM ntq WHERE a IS NOT NULL ORDER BY a LIMIT 3", "SELECT a FROM ntq WHERE (a + 0) IS NOT NULL ORDER BY a LIMIT 3")
	same("ios is-null count", "SELECT count(*) FROM ntq WHERE a IS NULL", "SELECT count(*) FROM ntq WHERE (a + 0) IS NULL")
	same("ios not-null count", "SELECT count(*) FROM ntq WHERE a IS NOT NULL", "SELECT count(*) FROM ntq WHERE (a + 0) IS NOT NULL")
	same("ios not-null range", "SELECT a FROM ntq WHERE a IS NOT NULL AND a < 5", "SELECT a FROM ntq WHERE (a + 0) IS NOT NULL AND a + 0 < 5")

	// Without NULL-keyed entries IS NULL cannot be an index qual.
	catalog.SetNullKeyedIndexEntries(false)
	if p := plan("SELECT * FROM ntq WHERE a IS NULL"); strings.Contains(p, "Index Cond: (a IS NULL)") {
		t.Errorf("IS NULL used an index that stores no NULL-keyed entries:\n%s", p)
	}
}
