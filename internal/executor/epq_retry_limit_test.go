package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/mvcc"
)

// TestEPQRetryLimitByIsolation pins the pgbench TPC-B contention fix: under
// READ COMMITTED the EvalPlanQual loop must not escalate plain UPDATE/DELETE
// row contention to SQLSTATE 40001 after only a handful of re-checks (real
// PostgreSQL blocks and retries until it can apply the change). REPEATABLE
// READ and SERIALIZABLE keep the prompt low cap so first-update-wins
// serialization failures surface immediately.
//
// Before the fix, maxEPQRetries=3 applied to every isolation level, so 8
// concurrent pgbench clients lapping the same teller row exhausted the budget
// and raised a spurious 40001 — which cascaded into "current transaction is
// aborted, commands ignored" client aborts.
func TestEPQRetryLimitByIsolation(t *testing.T) {
	if got := epqRetryLimit(mvcc.IsolationReadCommitted); got != maxEPQRetriesRC {
		t.Errorf("epqRetryLimit(ReadCommitted) = %d; want %d (high backstop, no spurious 40001)", got, maxEPQRetriesRC)
	}
	if maxEPQRetriesRC <= maxEPQRetries {
		t.Errorf("maxEPQRetriesRC (%d) must be far larger than maxEPQRetries (%d)", maxEPQRetriesRC, maxEPQRetries)
	}
	for _, iso := range []mvcc.IsolationLevel{mvcc.IsolationRepeatableRead, mvcc.IsolationSerializable} {
		if got := epqRetryLimit(iso); got != maxEPQRetries {
			t.Errorf("epqRetryLimit(%v) = %d; want %d (prompt serialization failure)", iso, got, maxEPQRetries)
		}
	}
}
