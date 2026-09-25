package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestNullKeyedIndexEntries covers store-null-keys S1+S2: in a cluster with
// the null_keyed_index_entries capability a tuple-format btree index files an
// entry for every heap row, NULL key columns included (as PostgreSQL's
// index_form_tuple does), from the bulk build, from runtime inserts and from
// ON CONFLICT arbiters; uniqueness still ignores NULLs; and a range scan with
// one open end stops at the NULL group instead of returning it. Design:
// docs/design/0100-0149/m-nightly-store-null-keyed-index-entries.md.
func TestNullKeyedIndexEntries(t *testing.T) {
	catalog.SetNullKeyedIndexEntries(true)
	defer catalog.SetNullKeyedIndexEntries(false)
	ctx, cleanup := newVMFixture(t)
	defer cleanup()

	runSQL(t, ctx, "CREATE TABLE nk (a int, b int, c int)")
	runSQL(t, ctx, "INSERT INTO nk SELECT g % 100, g % 7, g FROM generate_series(1, 3000) g")
	runSQL(t, ctx, "INSERT INTO nk VALUES (NULL, 1, NULL), (NULL, NULL, NULL), (3, NULL, -1), (95, NULL, -2)")
	// Bulk builds over NULL-keyed rows; the unique index holds two NULL c's.
	runSQL(t, ctx, "CREATE INDEX nk_a ON nk (a)")
	runSQL(t, ctx, "CREATE INDEX nk_ab ON nk (a, b)")
	runSQL(t, ctx, "CREATE UNIQUE INDEX nk_c ON nk (c)")
	// Runtime inserts of NULL-keyed rows, including through the arbiter: a
	// NULL conflict key never conflicts, so both rows are inserted.
	runSQL(t, ctx, "INSERT INTO nk VALUES (NULL, 2, NULL), (96, NULL, -3)")
	runSQL(t, ctx, "INSERT INTO nk VALUES (97, 1, NULL) ON CONFLICT (c) DO NOTHING")
	runSQL(t, ctx, "INSERT INTO nk VALUES (97, 1, NULL) ON CONFLICT (c) DO NOTHING")
	if got := sortedRowStrings(t, ctx, "SELECT count(*) FROM nk WHERE c IS NULL"); strings.Join(got, "") != "5" {
		t.Fatalf("NULL-c rows = %v, want 5 (NULL never conflicts)", got)
	}
	runSQL(t, ctx, "VACUUM nk")
	runSQL(t, ctx, "ANALYZE nk")

	// heapallindexed: every heap row, NULL-keyed ones included, must have its
	// entry — this fails if the writers skipped them.
	for _, idx := range []string{"nk_a", "nk_ab", "nk_c"} {
		if _, err := runQueryWithErr(ctx, "SELECT bt_index_check('"+idx+"', true)"); err != nil {
			t.Errorf("bt_index_check(%s, heapallindexed): %v", idx, err)
		}
	}

	for _, tc := range []struct{ name, indexed, noIndex string }{
		{"open-high", "SELECT a, b, c FROM nk WHERE a > 94", "SELECT a, b, c FROM nk WHERE a + 0 > 94"},
		{"open-high-inclusive", "SELECT a, b, c FROM nk WHERE a >= 96", "SELECT a, b, c FROM nk WHERE a + 0 >= 96"},
		{"open-low", "SELECT a, b, c FROM nk WHERE a < 2", "SELECT a, b, c FROM nk WHERE a + 0 < 2"},
		{"index-only-open-high", "SELECT a FROM nk WHERE a > 95", "SELECT a FROM nk WHERE a + 0 > 95"},
		{"prefix-open-high", "SELECT a, b, c FROM nk WHERE a = 3 AND b > 4", "SELECT a, b, c FROM nk WHERE a + 0 = 3 AND b + 0 > 4"},
		{"prefix-eq-keeps-null", "SELECT a, b, c FROM nk WHERE a = 3", "SELECT a, b, c FROM nk WHERE a + 0 = 3"},
		{"two-sided", "SELECT a, b, c FROM nk WHERE a BETWEEN 95 AND 97", "SELECT a, b, c FROM nk WHERE a + 0 BETWEEN 95 AND 97"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := sortedRowStrings(t, ctx, tc.indexed)
			want := sortedRowStrings(t, ctx, tc.noIndex)
			if strings.Join(got, "|") != strings.Join(want, "|") {
				t.Errorf("%s:\n  indexed  %d rows\n  no index %d rows\nplan:\n%s", tc.indexed, len(got), len(want),
					strings.Join(runExplainRows(t, ctx, "EXPLAIN "+tc.indexed), "\n"))
			}
		})
	}
	// NULLS FIRST files the NULLs before every value: a range with an open
	// LOW end must skip that group (the exclusive low pivot).
	runSQL(t, ctx, "CREATE TABLE nkf (a int, c int)")
	runSQL(t, ctx, "INSERT INTO nkf SELECT g % 100, g FROM generate_series(1, 3000) g")
	runSQL(t, ctx, "INSERT INTO nkf VALUES (NULL, -1), (NULL, -2), (1, -3)")
	runSQL(t, ctx, "CREATE INDEX nkf_a ON nkf (a NULLS FIRST)")
	runSQL(t, ctx, "ANALYZE nkf")
	if got, want := sortedRowStrings(t, ctx, "SELECT a, c FROM nkf WHERE a < 2"), sortedRowStrings(t, ctx, "SELECT a, c FROM nkf WHERE a + 0 < 2"); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("NULLS FIRST open-low: indexed %d rows, no index %d rows\nplan:\n%s", len(got), len(want),
			strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT a, c FROM nkf WHERE a < 2"), "\n"))
	}
	if plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT a, c FROM nkf WHERE a < 2"), "\n"); !strings.Contains(plan, "Index") {
		t.Errorf("NULLS FIRST open-low plan uses no index; the low NULL stop is not exercised:\n%s", plan)
	}

	// The open-high shape must really be an index range scan, or the NULL stop
	// is not exercised.
	if plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT a, b, c FROM nk WHERE a > 94"), "\n"); !strings.Contains(plan, "Index") {
		t.Errorf("open-high plan uses no index; the NULL stop is not exercised:\n%s", plan)
	}
}
