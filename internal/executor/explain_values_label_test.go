package executor

import (
	"strings"
	"testing"
)

// M0146-0109: PG plans a FROM-less SELECT and a one-row VALUES as a Result,
// whose WHERE is a `One-Time Filter:`, and a multi-row VALUES as `Values Scan
// on "*VALUES*"` (set_rtable_names numbers the next `"*VALUES*_1"`). goopg
// printed both as `Values (N rows)`.
func TestExplainValuesAndResultLabels(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	for _, tc := range []struct {
		q    string
		want []string
	}{
		{"SELECT 1", []string{"Result"}},
		{"SELECT (SELECT 1)", []string{"Result", "InitPlan 1", "    ->  Result"}},
		{"SELECT 1 WHERE false", []string{"Result", "  One-Time Filter: false"}},
		{"VALUES (1), (2)", []string{`Values Scan on "*VALUES*"`}},
		{"SELECT * FROM (VALUES (1), (2)) v(x) JOIN (VALUES (3), (4)) w(y) ON x = y",
			[]string{`Values Scan on "*VALUES*"`, `Values Scan on "*VALUES*_1"`}},
	} {
		rows := runExplainRows(t, ctx, "EXPLAIN (COSTS OFF) "+tc.q)
		joined := strings.Join(rows, "\n")
		if strings.Contains(joined, "Values (") {
			t.Errorf("%s: goopg-only `Values (N rows)` label:\n%s", tc.q, joined)
		}
		for _, w := range tc.want {
			found := false
			for _, l := range rows {
				if strings.HasSuffix(l, w) || strings.TrimSpace(l) == strings.TrimSpace(w) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s: want %q; got:\n%s", tc.q, w, joined)
			}
		}
	}
}
