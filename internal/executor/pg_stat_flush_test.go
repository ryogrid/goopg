package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/planner"
)

// pg_stat_force_next_flush() and pg_stat_clear_snapshot() are void no-ops in
// goopg: there is no separate statistics-collector process to flush to (pending
// stats are applied synchronously) and no per-transaction stats snapshot cache.
// The isolation `stats` spec calls pg_stat_force_next_flush() in its global
// setup between mutating and observing steps, so before M0118-0009 every
// permutation hard-failed with "function pg_stat_force_next_flush does not
// exist (42883)". Each must now evaluate to a non-NULL void-like value.
func TestPgStatFlushSnapshotVoidNoops(t *testing.T) {
	for _, name := range []string{"pg_stat_force_next_flush", "pg_stat_clear_snapshot"} {
		call := &planner.FuncCall{Name: name}
		got, err := evalFuncCall(call, nil, &Context{})
		if err != nil {
			t.Fatalf("evalFuncCall(%s): %v", name, err)
		}
		// void renders as a non-NULL empty value (same convention as the
		// advisory-lock / pg_notify void builtins) so `IS NOT NULL` holds.
		if got.IsNull() {
			t.Errorf("%s returned NULL, want non-NULL void-like value", name)
		}
		if got.Kind != KindString || got.StringValue() != "" {
			t.Errorf("%s = (kind=%v, %q), want empty string", name, got.Kind, got.StringValue())
		}
	}
}
