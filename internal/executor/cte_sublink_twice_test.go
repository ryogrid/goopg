package executor

import (
	"strings"
	"testing"
)

// TestCorrelatedCTEReadTwiceInSublink pins M0146-0071 against PG 18.3. A
// sublink whose CTE body reads the enclosing row, and whose FROM list reads
// that CTE twice, bound the second reference's outer value to the join's
// LEFT row: `(WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT c.k || '/' ||
// c2.k FROM c, c AS c2)` gave 2/4 where PG gives 2/2. The CTE body's outer
// reference made the join over the two scans lateral (nodeReferencesOuter
// walked into the body), and the lateral driver pushed the left row where
// the body's level-1 reference resolves. A CTE reference never reads a FROM
// sibling, so the join is no longer lateral. Every want is PG's output.
func TestCorrelatedCTEReadTwiceInSublink(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	for _, c := range []struct{ sql, want string }{
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT c.k || '/' || c2.k FROM c, c AS c2) FROM generate_series(1,3) g", "1|2/2;2|4/4;3|6/6"},
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g*2 AS k) SELECT sum(c.k + c2.k) FROM c, c AS c2) FROM generate_series(1,3) g", "1|4;2|8;3|12"},
		{"SELECT g, (WITH c AS (SELECT g*2 AS k) SELECT c.k || '/' || c2.k FROM c, c AS c2) FROM generate_series(1,3) g", "1|2/2;2|4/4;3|6/6"},
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g*10 + x AS k FROM generate_series(1,2) x) SELECT string_agg(c.k || '/' || c2.k, ',' ORDER BY c.k, c2.k) FROM c, c AS c2) FROM generate_series(1,2) g", "1|11/11,11/12,12/11,12/12;2|21/21,21/22,22/21,22/22"},
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g AS k) SELECT count(*) FROM c JOIN c AS c2 ON c.k = c2.k) FROM generate_series(1,3) g", "1|1;2|1;3|1"},
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g*10 + x AS k FROM generate_series(1,2) x) SELECT string_agg(c.k || '/' || c2.k, ',' ORDER BY c.k, c2.k) FROM c, c AS c2 WHERE c2.k > g*10 + 1) FROM generate_series(1,2) g", "1|11/12,12/12;2|21/22,22/22"},
		{"SELECT g, (WITH c AS MATERIALIZED (SELECT g*10 + x AS k FROM generate_series(1,2) x) SELECT string_agg(c.k || '/' || c2.k, ',' ORDER BY c.k, c2.k) FROM c JOIN c AS c2 ON c2.k = c.k + 1) FROM generate_series(1,2) g", "1|11/12;2|21/22"},
		{"SELECT g, (WITH c AS (SELECT g*10 + x AS k FROM generate_series(1,2) x) SELECT count(*) FROM c, c AS c2, c AS c3) FROM generate_series(1,2) g", "1|8;2|8"},
		{"SELECT (WITH c AS MATERIALIZED (SELECT 7 AS k) SELECT c.k + c2.k FROM c, c AS c2)", "14"},
	} {
		rows, err := runQueryWithErr(ctx, c.sql)
		if err != nil {
			t.Errorf("%s: %v", c.sql, err)
			continue
		}
		if got := strings.Join(renderRows(rows), ";"); got != c.want {
			t.Errorf("%s\ngot  %s\nwant PG's %s", c.sql, got, c.want)
		}
	}
}
