package optimizer

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/testutil/tpch"
)

// TestPlanTPCHQueriesPlannable parses+plans every TPC-H query against
// the v0 planner. Queries that fail are captured for triage; this is
// the canonical place to track plan-time regressions as M0003
// query-coverage work proceeds.
//
// A query is "plannable" when parser.Parse and planner.Plan both
// succeed; it is *not* a guarantee that execution produces the right
// rows — that is verified separately when the HammerDB harness
// becomes available. See `internal/executor/tpch_test.go` for the
// build-time companion.
func TestPlanTPCHQueriesPlannable(t *testing.T) {
	cat, err := tpch.Catalog()
	if err != nil {
		t.Fatalf("tpch.Catalog: %v", err)
	}
	queries := tpch.Queries()
	type result struct {
		ok  bool
		err string
	}
	results := make(map[int]result)
	for q := 1; q <= 22; q++ {
		stmts, perr := parser.Parse(queries[q])
		if perr != nil {
			results[q] = result{false, "parse: " + perr.Error()}
			continue
		}
		if len(stmts) == 0 {
			results[q] = result{false, "no statements parsed"}
			continue
		}
		// Plan only the first statement; Q15 ships three but the
		// CREATE VIEW is the gating step.
		if _, err := Plan(stmts[0], cat); err != nil {
			results[q] = result{false, "plan: " + err.Error()}
			continue
		}
		results[q] = result{true, ""}
	}
	var passed, failed []int
	for q := 1; q <= 22; q++ {
		if results[q].ok {
			passed = append(passed, q)
		} else {
			failed = append(failed, q)
		}
	}
	t.Logf("TPC-H plan-time coverage: %d/22 plannable", len(passed))
	for _, q := range failed {
		t.Logf("  Q%-2d FAIL: %s", q, truncateErr(results[q].err))
	}
	for q := 1; q <= 22; q++ {
		if !results[q].ok {
			t.Errorf("Q%d expected to plan but failed: %s", q, results[q].err)
		}
	}
}

func truncateErr(s string) string {
	const max = 240
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
