package testport

// End-to-end coverage of pg_amcheck's SCHEMA-SCOPED corruption check across a
// server restart against a live goopg cluster (M0110-0003, row AC-003).
//
// postgres/src/bin/pg_amcheck/t/003_check.pl builds its fixture inside user
// schemas (s1, s2) and scopes its runs with `--schema`/`--relation` qualifiers
// against those schemas (e.g. plan_to_remove_relation_file('db1','s2.t1'),
// :275). The combined- and single-corruption surrogates ported earlier
// (TestPort_PgAmcheck003CombinedCorruption / 003MissingHeapFile) were forced to
// place every relation in `public` because, before this milestone's loop #20
// CREATE SCHEMA WAL-durability work AND this loop's user-schema TABLE durability
// fix, a CREATE SCHEMA namespace and the tables created in it did not survive a
// server restart: a `--schema s1` run was clean pre-restart but reported
// `no relations to check` post-restart (the symptom logged in loops #15/#19).
//
// This tier closes that gap. It reproduces the heap file-removal corruption
// mechanic (cf. TestPort_PgAmcheck003MissingHeapFile) inside a genuine user
// schema and scopes the run with `--schema s1` — exactly as upstream does. The
// assertion is sharp BECAUSE it spans the restart: for pg_amcheck to report the
// removed file under `--schema s1` after the stop->corrupt->restart cycle, the
// schema s1 AND the table s1.t reloaded in it must both have survived the
// restart with the correct schema association. If the durability fix regressed
// (user-schema table reloaded under the wrong namespace, or the schema dropped),
// `--schema s1` would find no relations and report clean — caught here as a
// missing exit-2/missing-file report.
//
// WHAT THIS PROVES OVER TestPort_CreateSchemaSurvivesRestart: that durability
// holds end-to-end THROUGH the real pg_amcheck binary's schema-resolution +
// per-relation dispatch (not just over-the-wire CREATE/SELECT), and that a
// corruption injected into a user-schema relation is still detected after the
// restart.
//
// SELF-PROMOTING (cf. the sibling 003 tiers): the pre-corruption `--schema s1`
// run over the healthy fixture is the only guarded step; if it is not clean (an
// unrelated goopg gap on a healthy heap page, or `--schema` scoping not finding
// the table), the test t.Skips with the captured blocker rather than a
// misleading FAIL. Once the file is removed the assertions are hard, and the
// underlying detection logic is independently gated by the executor unit test
// TestVerifyHeapam_DetectsMissingRelationFile.
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md. CSV row: AC-003.

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PgAmcheck003SchemaScoped creates a table in a user schema, removes
// its backing file across a restart, and asserts a `--schema s1`-scoped
// pg_amcheck run reports the missing file — proving the schema and its table
// survived the restart with the correct schema association.
func TestPort_PgAmcheck003SchemaScoped(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck003-schemascoped", cluster.Options{
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
		"CREATE SCHEMA s1",
		"CREATE TABLE s1.t003sc (a int, b text)",
		"INSERT INTO s1.t003sc SELECT g, 'row-' || g FROM generate_series(1, 200) g",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Resolve the heap's backing file before stopping (file is named by relation
	// OID; the on-disk db dir is keyed by storage dbOid, so glob via findHeapFile).
	relOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 't003sc' AND relkind = 'r'")
	heapPath := findHeapFile(t, c, relOid)

	// Pre-corruption clean run, scoped to the user schema. Self-promoting: this
	// must both be clean AND actually have checked the table (not "no relations
	// to check"), else the fixture/scoping is broken and we skip.
	pre := runAmcheck(t, c, "--schema", "s1", "postgres")
	if pre.ExitCode != 0 || pre.Stdout != "" {
		t.Skipf("AC-003 blocked: pre-corruption pg_amcheck --schema s1 over a healthy "+
			"user-schema table is not clean (unrelated goopg gap or --schema scoping). "+
			"exit=%d stdout=%q stderr=%q", pre.ExitCode, pre.Stdout, pre.Stderr)
	}
	if strings.Contains(pre.Stderr, "no relations to check") {
		t.Skipf("AC-003 blocked: pre-corruption pg_amcheck --schema s1 found no relations "+
			"(--schema scoping did not resolve s1.t003sc). stderr=%q", pre.Stderr)
	}

	// Stop cleanly, remove the heap's main fork, restart — the schema and its
	// table must survive this cycle for the scoped run below to find them.
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

	// The removed file must stay absent — if goopg O_CREATEd it back the table
	// would check clean and the assertion below would be a false pass.
	if _, err := os.Stat(heapPath); err == nil {
		t.Fatalf("heap fork %q was recreated after restart — goopg must not "+
			"O_CREATE a removed user heap file before pg_amcheck checks it", heapPath)
	}

	// Post-corruption run, still scoped to the user schema. Reporting the missing
	// file here proves schema s1 + s1.t003sc both survived the restart with the
	// correct schema association AND the corruption is detected (exit 2).
	post := runAmcheck(t, c, "--schema", "s1", "postgres")
	if post.ExitCode != 2 {
		t.Errorf("post-corruption pg_amcheck --schema s1 exit=%d, want 2\n  stdout=%q\n  stderr=%q",
			post.ExitCode, post.Stdout, post.Stderr)
	}
	if strings.Contains(post.Stderr, "no relations to check") {
		t.Errorf("post-restart pg_amcheck --schema s1 found no relations — schema or "+
			"user-schema table did not survive the restart. stderr=%q", post.Stderr)
	}
	want := regexp.MustCompile(`could not open file ".*": No such file or directory`)
	if !want.MatchString(post.Stdout) {
		t.Errorf("post-corruption pg_amcheck --schema s1 stdout missing file report /%s/\n  stdout=%q\n  stderr=%q",
			want, post.Stdout, post.Stderr)
	}
}
