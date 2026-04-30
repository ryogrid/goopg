package testport

import (
	"testing"
)

// TestE2E_PgbenchWorkload tests the pgbench initialization and workload.
// SKIPPED: pgbench requires LD_LIBRARY_PATH set to local_install/lib,
// which cluster.PGbench doesn't support. Run manually with:
//   LD_LIBRARY_PATH=postgres/local_install/lib make pgbench
func TestE2E_PgbenchWorkload(t *testing.T) {
	t.Skip("pgbench requires LD_LIBRARY_PATH; cluster.PGbench doesn't support custom env")
}
