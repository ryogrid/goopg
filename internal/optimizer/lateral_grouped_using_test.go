package optimizer

import (
	"testing"

	"github.com/goopg/goopg/internal/parser/analyzer"
)

func TestPlanLateralGroupedUsingBindings(t *testing.T) {
	cat := pgbenchCatalog(t)
	for _, sql := range []string{
		`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) JOIN pgbench_history h ON a.aid = h.aid JOIN LATERAL (SELECT a.aid AS aid) q ON true`,
		`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) JOIN LATERAL (SELECT b.aid AS aid) q ON true`,
		`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) JOIN LATERAL (SELECT aid) q ON true`,
		`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) j JOIN LATERAL (SELECT j.aid) q ON true`,
	} {
		if _, err := Plan(parseOne(t, sql), cat); err != nil {
			t.Errorf("Plan(%q): %v", sql, err)
		}
	}
	for _, tc := range []struct{ sql, code string }{
		{`SELECT q.bid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) JOIN LATERAL (SELECT bid) q ON true`, "42702"},
		{`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) j JOIN LATERAL (SELECT a.aid) q ON true`, "42P01"},
		{`SELECT q.aid FROM (pgbench_accounts a JOIN pgbench_history b USING (aid)) JOIN (SELECT a.aid) q ON true`, "42P01"},
	} {
		_, err := Plan(parseOne(t, tc.sql), cat)
		if err == nil || groupedUsingCode(err) != tc.code {
			t.Errorf("Plan(%q) = %v, want SQLSTATE %s", tc.sql, err, tc.code)
		}
	}
}

func groupedUsingCode(err error) string {
	if e, ok := err.(*analyzer.AnalyzeError); ok { return e.Code }
	if e, ok := err.(*PlanError); ok { return e.Code }
	return ""
}
