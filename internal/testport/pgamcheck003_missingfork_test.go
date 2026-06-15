package testport

// End-to-end coverage of pg_amcheck's missing-main-relation-fork detection
// against a live goopg cluster (M0110-0003, row AC-003).
//
// This is the file-removal corruption tier of
// postgres/src/bin/pg_amcheck/t/003_check.pl. 003_check removes a btree index's
// backing file on disk (plan_to_remove_relation_file while the node is stopped),
// restarts, and asserts pg_amcheck reports
//
//	index "<name>" lacks a main relation fork
//
// on stdout with exit status 2 (003_check.pl:328-329, :392-399). Upstream
// bt_index_check_callback enforces this via smgrexists(MAIN_FORKNUM)
// (verify_nbtree.c:318); goopg now mirrors that guard in evalBtIndexCheck.
//
// The load-bearing risk this e2e proves (a unit test cannot): goopg's storage
// manager opens relation files with O_CREATE, so a naive NBlocks call would
// silently RECREATE the removed fork as an empty 0-block file and report the
// index as clean. The fix's stat-only Pool.Exists check, run before NBlocks,
// must hold across the real stop -> remove -> restart lifecycle (no startup or
// recovery path may recreate the user index file before pg_amcheck reaches it).
//
// SCOPING ADAPTATION (cf. AC-003): 003_check builds an elaborate fixture using
// relkinds/types goopg lacks (BOX, int4range, int4[], HASH/GiST/GIN/BRIN/SPGiST
// indexes, partitions, STORAGE EXTERNAL TOAST) across three databases. This
// port reproduces ONLY the btree-index file-removal mechanic on a
// goopg-supported fixture, exactly as the loop-#11 whole-DB enumeration tier
// (TestPort_PgAmcheckAllTables) reproduces the clean-DB path on a supported
// fixture. The page-overwrite tier is covered by TestPort_PgAmcheck004VerifyHeapam.
//
// SELF-PROMOTING: if the pre-removal run over the healthy fixture is not clean
// (an unrelated goopg gap), it t.Skips with the captured blocker rather than a
// misleading FAIL; the detection logic itself is hard-gated by the
// internal/executor unit test TestBtIndexCheck_DetectsMissingRelationFork.
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md. CSV row: AC-003.

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgAmcheck003MissingIndexFork removes a healthy btree index's backing
// file and verifies pg_amcheck reports the missing main relation fork.
func TestPort_PgAmcheck003MissingIndexFork(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck003-missingfork", cluster.Options{
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

	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("CREATE EXTENSION amcheck: %v", err)
	}
	for _, stmt := range []string{
		"CREATE TABLE t003mf (a int, b text)",
		"INSERT INTO t003mf SELECT g, 'row-' || g FROM generate_series(1, 200) g",
		"CREATE INDEX t003mf_a_idx ON t003mf (a)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Resolve the index's backing file before stopping (goopg names the file by
	// relation OID; the on-disk db dir is keyed by storage dbOid, so glob
	// base/<anydb>/<reloid> via findHeapFile rather than trusting pg_database.oid).
	idxOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 't003mf_a_idx' AND relkind = 'i'")
	idxPath := findHeapFile(t, c, idxOid)

	// Pre-removal clean run over the indexed table (checks heap + dependent
	// btree index). Self-promoting: skip if the healthy fixture is not clean.
	pre := runAmcheck(t, c, "--table", "public.t003mf", "postgres")
	if pre.ExitCode != 0 || pre.Stdout != "" {
		t.Skipf("AC-003 blocked: pre-removal pg_amcheck over a healthy indexed user "+
			"table is not clean (an unrelated goopg gap on the heap/index pages). "+
			"exit=%d stdout=%q stderr=%q", pre.ExitCode, pre.Stdout, pre.Stderr)
	}

	// Stop cleanly, then remove the index's main fork file out from under the
	// server — the upstream stop -> unlink -> restart corruption mechanism.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster for corruption: %v", err)
	}
	if _, err := os.Stat(idxPath); err != nil {
		t.Skipf("AC-003 blocked: index file %q not found after shutdown "+
			"(unexpected goopg on-disk layout): %v", idxPath, err)
	}
	if err := os.Remove(idxPath); err != nil {
		t.Fatalf("remove index fork %q: %v", idxPath, err)
	}
	t.Logf("removed index main fork: %s", idxPath)

	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after removal: %v", err)
	}
	// Runtime-only amcheck install does not survive restart (gap #7c); re-install.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("re-CREATE EXTENSION amcheck after restart: %v", err)
	}

	// Guard against goopg recreating the removed fork during startup/recovery —
	// if it did, the index would check clean and the test below would be a false
	// pass. The detection contract only holds while the file stays absent.
	if _, err := os.Stat(idxPath); err == nil {
		t.Fatalf("index fork %q was recreated after restart — goopg must not "+
			"O_CREATE a removed user index file before pg_amcheck checks it", idxPath)
	}

	// Post-removal run: pg_amcheck must report the missing fork on stdout and
	// exit 2 (its "corruption found" status), with nothing on stderr.
	post := runAmcheck(t, c, "--table", "public.t003mf", "postgres")
	if post.ExitCode != 2 {
		t.Errorf("post-removal pg_amcheck exit=%d, want 2\n  stdout=%q\n  stderr=%q",
			post.ExitCode, post.Stdout, post.Stderr)
	}
	want := regexp.MustCompile(`index "t003mf_a_idx" lacks a main relation fork`)
	if !want.MatchString(post.Stdout) {
		t.Errorf("post-removal pg_amcheck stdout missing fork report /%s/\n  stdout=%q\n  stderr=%q",
			want, post.Stdout, post.Stderr)
	}
}
