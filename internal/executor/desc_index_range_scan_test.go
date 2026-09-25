package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestDescIndexRangeScans pins range scans over DESC key columns of a
// tuple-format btree index. The tuple comparator honours DESC, so larger
// values come FIRST in index order; the range scan used the ascending ends
// anyway, and `a > 97` on (a DESC) returned 2910 of 3000 rows instead of 60
// while BETWEEN returned none. Value bounds now swap ends (and strictness) for
// a DESC column (rangeColumnReversed), the NULL stop works in index order
// (NULLS FIRST is DESC's default), and a SAOP probe leaves a DESC second-column
// bound to its Filter recheck. Every shape is compared with the same query
// with the index hidden, with the null_keyed_index_entries capability off and
// on (on: the NULL rows have index entries).
func TestDescIndexRangeScans(t *testing.T) {
	for _, capable := range []bool{false, true} {
		catalog.SetNullKeyedIndexEntries(capable)
		func() {
			defer catalog.SetNullKeyedIndexEntries(false)
			ctx, cleanup := newVMFixture(t)
			defer cleanup()
			for _, ddl := range []string{
				"CREATE TABLE dsc (a int, b int, c int)",
				"INSERT INTO dsc SELECT g % 100, g % 13, g FROM generate_series(1, 3000) g",
				"INSERT INTO dsc VALUES (NULL, 1, -1), (NULL, NULL, -2), (5, NULL, -3), (98, NULL, -4)",
				"CREATE INDEX dsc_a ON dsc (a DESC)",
				"CREATE TABLE dsl (a int, c int)",
				"INSERT INTO dsl SELECT g % 100, g FROM generate_series(1, 3000) g",
				"INSERT INTO dsl VALUES (NULL, -1), (NULL, -2)",
				"CREATE INDEX dsl_a ON dsl (a DESC NULLS LAST)",
				"CREATE TABLE dcb (a int, b int, c int)",
				"INSERT INTO dcb SELECT g % 20, g % 50, g FROM generate_series(1, 3000) g",
				"INSERT INTO dcb VALUES (3, NULL, -1), (4, NULL, -2)",
				"CREATE INDEX dcb_ab ON dcb (a, b DESC)",
				"VACUUM dsc", "ANALYZE dsc", "ANALYZE dsl", "ANALYZE dcb",
			} {
				runSQL(t, ctx, ddl)
			}
			for _, tc := range []struct{ indexed, noIndex string }{
				{"SELECT a, c FROM dsc WHERE a > 97", "SELECT a, c FROM dsc WHERE a + 0 > 97"},
				{"SELECT a, c FROM dsc WHERE a >= 99", "SELECT a, c FROM dsc WHERE a + 0 >= 99"},
				{"SELECT a, c FROM dsc WHERE a < 2", "SELECT a, c FROM dsc WHERE a + 0 < 2"},
				{"SELECT a, c FROM dsc WHERE a <= 0", "SELECT a, c FROM dsc WHERE a + 0 <= 0"},
				{"SELECT a, c FROM dsc WHERE a BETWEEN 3 AND 4", "SELECT a, c FROM dsc WHERE a + 0 BETWEEN 3 AND 4"},
				{"SELECT a FROM dsc WHERE a > 97", "SELECT a FROM dsc WHERE a + 0 > 97"},
				{"SELECT a, c FROM dsl WHERE a > 97", "SELECT a, c FROM dsl WHERE a + 0 > 97"},
				{"SELECT a, c FROM dsl WHERE a < 2", "SELECT a, c FROM dsl WHERE a + 0 < 2"},
				{"SELECT a, b, c FROM dcb WHERE a = 3 AND b > 45", "SELECT a, b, c FROM dcb WHERE a + 0 = 3 AND b + 0 > 45"},
				{"SELECT a, b, c FROM dcb WHERE a = 3 AND b < 4", "SELECT a, b, c FROM dcb WHERE a + 0 = 3 AND b + 0 < 4"},
				{"SELECT a, b, c FROM dcb WHERE a IN (3, 4) AND b > 45", "SELECT a, b, c FROM dcb WHERE a + 0 IN (3, 4) AND b + 0 > 45"},
			} {
				got := sortedRowStrings(t, ctx, tc.indexed)
				want := sortedRowStrings(t, ctx, tc.noIndex)
				if strings.Join(got, "|") != strings.Join(want, "|") {
					t.Errorf("capable=%v %s: indexed %d rows, no index %d rows\nplan:\n%s", capable, tc.indexed, len(got), len(want),
						strings.Join(runExplainRows(t, ctx, "EXPLAIN "+tc.indexed), "\n"))
				}
			}
			if plan := strings.Join(runExplainRows(t, ctx, "EXPLAIN SELECT a, c FROM dsc WHERE a > 97"), "\n"); !strings.Contains(plan, "Index") {
				t.Errorf("capable=%v: the DESC range shape uses no index; nothing is exercised:\n%s", capable, plan)
			}
		}()
	}
}
