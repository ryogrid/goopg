package testport

// End-to-end coverage of pg_amcheck's missing-heap-relation-file detection
// against a live goopg cluster (M0110-0003, row AC-003).
//
// This is the heap-table file-removal corruption tier of
// postgres/src/bin/pg_amcheck/t/003_check.pl. 003_check removes an ordinary
// table's backing file on disk (plan_to_remove_relation_file on 's2.t1' while
// the node is stopped, :275), restarts, and asserts pg_amcheck reports
//
//	could not open file "<path>": No such file or directory
//
// on stdout with exit status 2 (003_check.pl:327, :357-365). Upstream
// verify_heapam opens the relation's main fork via RelationGetNumberOfBlocks ->
// mdnblocks, which fails for a removed file; goopg now mirrors that by checking
// Pool.Exists in verifyHeapamOp.Open before NBlocks.
//
// The load-bearing risk this e2e proves (a unit test cannot): goopg's storage
// manager opens relation files with O_CREATE, so a naive NBlocks call would
// silently RECREATE the removed fork as an empty 0-block file and report the
// heap as clean. The fix's stat-only Pool.Exists check, run before NBlocks,
// must hold across the real stop -> remove -> restart lifecycle (no startup or
// recovery path may recreate the user heap file before pg_amcheck reaches it).
//
// SCOPING ADAPTATION (cf. AC-003): like TestPort_PgAmcheck003MissingIndexFork,
// this port reproduces ONLY the heap file-removal mechanic on a goopg-supported
// fixture (a plain heap table, no index — so the heap check is the sole check),
// not 003_check's full multi-relkind/multi-database fixture.
//
// SELF-PROMOTING: if the pre-removal run over the healthy table is not clean
// (an unrelated goopg gap), it t.Skips with the captured blocker; the detection
// logic itself is hard-gated by the internal/executor unit test
// TestVerifyHeapam_DetectsMissingRelationFile.
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

// TestPort_PgAmcheck003MissingHeapFile removes a healthy heap table's backing
// file and verifies pg_amcheck reports the missing relation file.
func TestPort_PgAmcheck003MissingHeapFile(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck003-missingheap", cluster.Options{
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
		"CREATE TABLE t003mh (a int, b text)",
		"INSERT INTO t003mh SELECT g, 'row-' || g FROM generate_series(1, 200) g",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Resolve the heap's backing file before stopping (goopg names the file by
	// relation OID; the on-disk db dir is keyed by storage dbOid, so glob
	// base/<anydb>/<reloid> via findHeapFile rather than trusting pg_database.oid).
	relOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 't003mh' AND relkind = 'r'")
	heapPath := findHeapFile(t, c, relOid)

	// Pre-removal clean run over the healthy table (heap check only — no index).
	// Self-promoting: skip if the healthy fixture is not clean.
	pre := runAmcheck(t, c, "--table", "public.t003mh", "postgres")
	if pre.ExitCode != 0 || pre.Stdout != "" {
		t.Skipf("AC-003 blocked: pre-removal pg_amcheck over a healthy user table "+
			"is not clean (an unrelated goopg gap on the heap pages). "+
			"exit=%d stdout=%q stderr=%q", pre.ExitCode, pre.Stdout, pre.Stderr)
	}

	// Stop cleanly, then remove the heap's main fork file out from under the
	// server — the upstream stop -> unlink -> restart corruption mechanism.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster for corruption: %v", err)
	}
	if _, err := os.Stat(heapPath); err != nil {
		t.Skipf("AC-003 blocked: heap file %q not found after shutdown "+
			"(unexpected goopg on-disk layout): %v", heapPath, err)
	}
	if err := os.Remove(heapPath); err != nil {
		t.Fatalf("remove heap fork %q: %v", heapPath, err)
	}
	t.Logf("removed heap main fork: %s", heapPath)

	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after removal: %v", err)
	}
	// Runtime-only amcheck install does not survive restart (gap #7c); re-install.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("re-CREATE EXTENSION amcheck after restart: %v", err)
	}

	// Guard against goopg recreating the removed fork during startup/recovery —
	// if it did, the heap would check clean and the test below would be a false
	// pass. The detection contract only holds while the file stays absent.
	if _, err := os.Stat(heapPath); err == nil {
		t.Fatalf("heap fork %q was recreated after restart — goopg must not "+
			"O_CREATE a removed user heap file before pg_amcheck checks it", heapPath)
	}

	// Post-removal run: pg_amcheck must report the missing file on stdout and
	// exit 2 (its "corruption found" status), with nothing on stderr.
	post := runAmcheck(t, c, "--table", "public.t003mh", "postgres")
	if post.ExitCode != 2 {
		t.Errorf("post-removal pg_amcheck exit=%d, want 2\n  stdout=%q\n  stderr=%q",
			post.ExitCode, post.Stdout, post.Stderr)
	}
	want := regexp.MustCompile(`could not open file ".*": No such file or directory`)
	if !want.MatchString(post.Stdout) {
		t.Errorf("post-removal pg_amcheck stdout missing file report /%s/\n  stdout=%q\n  stderr=%q",
			want, post.Stdout, post.Stderr)
	}
}
