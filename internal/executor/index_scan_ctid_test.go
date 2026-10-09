package executor

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/optimizer"
)

// TestIndexScanCarriesCtid pins M0146-0046 against PG 18.3: an Index Scan
// returns each heap tuple's ctid (the HOT-resolved live version), and a
// statement reading ctid never takes an index-only scan — no index stores a
// system column (check_index_only's attrs_used).
func TestIndexScanCarriesCtid(t *testing.T) {
	// PG elects the Index Scan over its own range bitmap here. goopg prices
	// a plain index probe at indexProbeCostMultiplier (2) times PG's, which
	// since M0146-0061's range bitmap path hands this two-row range to the
	// bitmap. The precondition is PG's plan under PG's costing; the shipped
	// multiplier is M0146-0068's owner decision.
	defer optimizer.SetIndexProbeCostMultiplier("1")()
	ctx, _, cleanup := newDDLFixture(t)
	t.Cleanup(cleanup)
	runSQL(t, ctx, "CREATE TABLE zc (a int PRIMARY KEY, b int)")
	runSQL(t, ctx, "INSERT INTO zc SELECT g, g FROM generate_series(1, 5000) g")
	runSQL(t, ctx, "ANALYZE zc")

	plan := explainText(t, ctx, "SELECT ctid, a FROM zc WHERE a BETWEEN 5 AND 6")
	if !strings.Contains(plan, "Index Scan using zc_pkey") {
		t.Fatalf("precondition: expected an index scan:\n%s", plan)
	}
	for _, c := range []struct{ sql, want string }{
		{"SELECT string_agg(ctid::text || '=' || a, ',' ORDER BY a) FROM zc WHERE a BETWEEN 5 AND 6", "(0,5)=5,(0,6)=6"},
		{"SELECT count(ctid), count(*) FROM zc WHERE a < 100", "99|99"},
	} {
		rows := runSQL(t, ctx, c.sql)
		parts := make([]string, len(rows[0]))
		for i, d := range rows[0] {
			parts[i] = datumTestString(d)
		}
		if got := strings.Join(parts, "|"); got != c.want {
			t.Errorf("%s\n got %q, want %q", c.sql, got, c.want)
		}
	}
	if plan := explainText(t, ctx, "SELECT count(ctid) FROM zc WHERE a < 100"); strings.Contains(plan, "Index Only Scan") {
		t.Errorf("a statement reading ctid took an index-only scan:\n%s", plan)
	}
}
