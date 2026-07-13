package testport

import (
	"os"
	"strings"
	"testing"
)

// skipUnlessCanonicalWAL assert-skips tests whose oracle is a REAL
// PostgreSQL consumer of goopg's canonical WAL records (a PG standby
// replaying goopg WAL, or pg_waldump decoding rmgr content). Since
// perf-optimize3-dash S4 the production default is a native-only stream
// (EmitCanonical off), which those consumers cannot apply — the tests run
// only when the resume path is explicitly enabled with
// GOOPG_WAL_CANONICAL=on (deferral ledger: perf-optimize3-dash; resume =
// re-enable + land perf-optimize3/05-improvement-designs/01 (C1)).
func skipUnlessCanonicalWAL(t *testing.T) {
	t.Helper()
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GOOPG_WAL_CANONICAL"))) {
	case "on", "1", "true":
		return
	}
	t.Skip("canonical WAL emission is off by default (perf-optimize3-dash S4, native-only stream); " +
		"this test needs a real-PG-consumable stream — run with GOOPG_WAL_CANONICAL=on " +
		"(see .ralph/deferral_ledger.md perf-optimize3-dash)")
}
