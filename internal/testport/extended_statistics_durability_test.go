package testport

// End-to-end coverage of extended-statistics (CREATE STATISTICS) restart
// persistence after B5 Bstat: statistics objects now journal as real
// pg_statistic_ext heap rows (base/<dbOid>/3381) instead of the retired
// goopg-private RecordKindCreateStatistics(95)/DropStatistics(96)/
// AlterStatistics(97-99) records. The reload (loadStatisticsExtFromHeap)
// reconstructs the registry by decoding stxkeys (int2vector → column names),
// stxkind (char[] → kind strings) and stxexprs (text[] → expression targets) —
// this test exercises that full encode↔decode round-trip via
// pg_get_statisticsobjdef, which re-emits the CREATE STATISTICS from exactly
// those reconstructed fields.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_ExtendedStatisticsSurvivesRestart creates several statistics objects
// (multi-column with explicit kinds, an expression object, one that gets
// renamed, and one that gets dropped), restarts, and asserts each survives (or
// stays gone) with its full definition intact via pg_get_statisticsobjdef.
func TestPort_ExtendedStatisticsSurvivesRestart(t *testing.T) {
	c, err := cluster.New("ext-stats-durability", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c,
		"CREATE TABLE stx_t (a int, b int, c int, d text)"); err != nil {
		t.Fatalf("CREATE TABLE stx_t: %v", err)
	}
	// Multi-column, explicit kinds (exercises stxkeys with >1 attnum + stxkind).
	if err := runSQLSimple(t, c,
		"CREATE STATISTICS stx_multi (ndistinct, dependencies) ON a, b, c FROM stx_t"); err != nil {
		t.Fatalf("CREATE STATISTICS stx_multi: %v", err)
	}
	// Expression target (exercises stxexprs + the implicit 'e' stxkind).
	if err := runSQLSimple(t, c,
		"CREATE STATISTICS stx_expr ON a, (b + c) FROM stx_t"); err != nil {
		t.Fatalf("CREATE STATISTICS stx_expr: %v", err)
	}
	// One to rename, one to drop.
	if err := runSQLSimple(t, c,
		"CREATE STATISTICS stx_old ON a, b FROM stx_t"); err != nil {
		t.Fatalf("CREATE STATISTICS stx_old: %v", err)
	}
	if err := runSQLSimple(t, c,
		"CREATE STATISTICS stx_gone ON b, c FROM stx_t"); err != nil {
		t.Fatalf("CREATE STATISTICS stx_gone: %v", err)
	}
	if err := runSQLSimple(t, c, "ALTER STATISTICS stx_old RENAME TO stx_new"); err != nil {
		t.Fatalf("ALTER STATISTICS RENAME: %v", err)
	}
	if err := runSQLSimple(t, c, "DROP STATISTICS stx_gone"); err != nil {
		t.Fatalf("DROP STATISTICS stx_gone: %v", err)
	}

	// Capture the pre-restart definitions so we can compare byte-for-byte.
	defMultiPre := queryScalar(t, c,
		"SELECT pg_get_statisticsobjdef(oid) FROM pg_statistic_ext WHERE stxname = 'stx_multi'")
	defExprPre := queryScalar(t, c,
		"SELECT pg_get_statisticsobjdef(oid) FROM pg_statistic_ext WHERE stxname = 'stx_expr'")
	if !strings.Contains(defMultiPre, "ndistinct") || !strings.Contains(defMultiPre, "dependencies") {
		t.Fatalf("pre-restart stx_multi def missing kinds: %q", defMultiPre)
	}

	// Restart.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("restart: %v", err)
	}

	// Renamed object present under the new name; old name gone.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_statistic_ext WHERE stxname = 'stx_new'"); got != "1" {
		t.Fatalf("post-restart stx_new count = %q, want 1 (rename did not survive)", got)
	}
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_statistic_ext WHERE stxname = 'stx_old'"); got != "0" {
		t.Fatalf("post-restart stx_old count = %q, want 0 (renamed-away name resurfaced)", got)
	}
	// Dropped object stays gone.
	if got := queryScalar(t, c,
		"SELECT count(*) FROM pg_statistic_ext WHERE stxname = 'stx_gone'"); got != "0" {
		t.Fatalf("post-restart stx_gone count = %q, want 0 (drop did not survive)", got)
	}

	// Full definitions round-trip: pg_get_statisticsobjdef re-emits from the
	// reloaded stxkeys/stxkind/stxexprs, so byte-equality proves the decode.
	defMultiPost := queryScalar(t, c,
		"SELECT pg_get_statisticsobjdef(oid) FROM pg_statistic_ext WHERE stxname = 'stx_multi'")
	if defMultiPost != defMultiPre {
		t.Fatalf("post-restart stx_multi def = %q, want %q (stxkeys/stxkind decode drifted)", defMultiPost, defMultiPre)
	}
	defExprPost := queryScalar(t, c,
		"SELECT pg_get_statisticsobjdef(oid) FROM pg_statistic_ext WHERE stxname = 'stx_expr'")
	if defExprPost != defExprPre {
		t.Fatalf("post-restart stx_expr def = %q, want %q (stxexprs decode drifted)", defExprPost, defExprPre)
	}
}
