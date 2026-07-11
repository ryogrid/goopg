package executor

import "testing"

// TestPgStatUserFunctionsEndToEnd drives a real SELECT through the planner and
// executor to confirm pg_stat_user_functions (and its _xact_ sibling) resolve as
// virtual views and return zero rows — matching a stock PostgreSQL cluster with
// the default track_functions = none, since goopg has no per-function call/time
// tracking. The previous behaviour was an unknown-relation error. M0122-0003.
func TestPgStatUserFunctionsEndToEnd(t *testing.T) {
	ctx, _, cleanup := newStorageFixture(t)
	defer cleanup()

	for _, view := range []string{"pg_stat_user_functions", "pg_stat_xact_user_functions"} {
		rows := runQueryRows(t, ctx,
			"SELECT funcid, schemaname, funcname, calls, total_time, self_time FROM "+view)
		if len(rows) != 0 {
			t.Errorf("%s returned %d rows, want 0 (no function-call tracking)", view, len(rows))
		}
	}
}
