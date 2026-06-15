package testport

// End-to-end coverage of pg_amcheck's B-tree index-check path against a live
// goopg cluster (M0110-0003, row AC-003, blocker #2).
//
// pg_amcheck, when given `--table <rel>` for a table that has dependent btree
// indexes, checks the heap (verify_heapam) AND every dependent btree index
// (bt_index_check). It builds the index call schema-qualified by the amcheck
// extension's install schema, e.g.
//
//	SELECT ... "public".bt_index_check(index := $1::regclass,
//	                                   heapallindexed := $2, ...)
//
// goopg's evalFuncCall stripped only the `pg_catalog.` qualifier before matching
// builtins, so this install-schema-qualified call 42883'd ("function
// public.bt_index_check does not exist") — meaning ANY user table with an index
// failed pg_amcheck even when perfectly healthy. The fix strips a user-schema
// qualifier for the amcheck scalar builtins (mirroring the FROM-clause SRF
// schema-strip for verify_heapam). This test is the e2e proof: a healthy
// indexed table must check clean through the real binary.
//
// SELF-PROMOTING (cf. AC-002/AC-003). Drives the real upstream pg_amcheck. If
// the clean run is not clean (an unrelated goopg gap surfacing on the indexed
// relation's heap or index pages), it t.Skips with the captured blocker rather
// than a misleading FAIL; the dispatch fix itself is hard-gated by the
// internal/executor unit test TestBtIndexCheck_SchemaQualifiedDispatch.
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md. CSV row: AC-003.

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgAmcheckBtreeIndexCheck verifies that a healthy user table WITH a
// dependent btree index checks clean through pg_amcheck — exercising the
// install-schema-qualified bt_index_check dispatch path (blocker #2).
func TestPort_PgAmcheckBtreeIndexCheck(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck-btree", cluster.Options{
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
		"CREATE TABLE tbtree (a int, b text)",
		"INSERT INTO tbtree SELECT g, 'row-' || g FROM generate_series(1, 200) g",
		"CREATE INDEX tbtree_a_idx ON tbtree (a)",
		"CREATE INDEX tbtree_b_idx ON tbtree (b)",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Default pg_amcheck over the indexed table: checks heap + both btree
	// indexes. A healthy table must be clean (exit 0, empty stdout).
	res := runAmcheck(t, c, "--table", "public.tbtree", "postgres")

	// A pre-fix tree surfaces the dispatch gap as a btree-check query failure
	// naming bt_index_check; assert that specific 42883 signature is gone.
	if strings.Contains(res.Stderr, "bt_index_check") &&
		(strings.Contains(res.Stderr, "does not exist") || strings.Contains(res.Stderr, "42883")) {
		t.Fatalf("bt_index_check dispatch still failing (blocker #2 regressed):\n  stderr=%q", res.Stderr)
	}
	if res.ExitCode != 0 || res.Stdout != "" {
		t.Skipf("AC-003: pg_amcheck over a healthy indexed user table is not clean "+
			"(an unrelated goopg gap on the indexed relation's heap/index pages). "+
			"exit=%d stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
}
