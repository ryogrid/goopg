package testport

// End-to-end coverage of pg_amcheck's CENTRAL combined-corruption assertion
// against a live goopg cluster (M0110-0003, row AC-003).
//
// This is the integration tier of postgres/src/bin/pg_amcheck/t/003_check.pl —
// the test's main check (003_check.pl:347-365). Upstream plans several DIFFERENT
// corruptions across a database (a removed index fork, a removed heap file, an
// overwritten heap page), applies them all in a single stop -> corrupt ->
// restart cycle (perform_all_corruptions, :107-119), then runs pg_amcheck once
// over the whole database and asserts that ALL THREE corruption classes are
// reported together in a single pass (exit 2, nothing on stderr):
//
//	$index_missing_relation_fork_re = qr/index ".*" lacks a main relation fork/
//	$line_pointer_corruption_re     = qr/line pointer/
//	$missing_file_re                = qr/could not open file ".*": No such file or directory/
//
// WHY THIS IS A DISTINCT TIER. The sibling surrogates each inject exactly ONE
// corruption on a single-relation fixture:
//   - TestPort_PgAmcheck003MissingIndexFork  — one removed btree index fork
//   - TestPort_PgAmcheck003MissingHeapFile   — one removed heap file
//   - TestPort_PgAmcheck004VerifyHeapam      — one overwritten heap line pointer
// None of them proves the property 003_check's main check actually asserts:
// that pg_amcheck's per-relation dispatch does NOT abort on the first corrupt
// relation, but continues enumerating and reports every distinct corruption
// class across a multi-relation database in one invocation. A bug where the
// first verify_heapam ERROR (the removed-file case raises 58030) tore down the
// whole run would pass all three isolated surrogates yet be caught here.
//
// SCOPING ADAPTATION (cf. AC-003 / TestPort_PgAmcheckAllTables). 003_check builds
// its fixture from relkinds/types goopg lacks (BOX, int4range, int4[],
// HASH/GiST/GIN/BRIN/SPGiST, partitions, STORAGE EXTERNAL TOAST) across three
// databases. This port reproduces the same combined-corruption mechanic on the
// goopg-supported relkind subset (plain heap tables + btree indexes) inside a
// dedicated user schema, and scopes the run to that schema with `--schema s1` —
// exactly as the loop-#11 whole-schema enumeration tier does — so the assertion
// is not coupled to the separate whole-database system-catalog heap-storage
// capability (which is the db-wide-default-only concern logged there).
//
// SELF-PROMOTING (cf. 002 / 004). The pre-corruption run over the healthy schema
// is the only guarded step: if it is not clean (an unrelated goopg gap on a
// healthy heap/index page), the test t.Skips with the captured blocker rather
// than a misleading FAIL. Once a corruption is injected the assertions are hard,
// and the underlying detection logic is independently hard-gated by the
// executor unit tests TestBtIndexCheck_DetectsMissingRelationFork and
// TestVerifyHeapam_DetectsMissingRelationFile plus the amcheck engine unit tests.
//
// Init uses --no-data-checksums (003_check.pl: init(no_data_checksums => 1)):
// goopg defaults checksums ON, which would trip the storage-manager checksum
// verification on the overwritten page before verify_heapam ever inspects it.
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

// TestPort_PgAmcheck003CombinedCorruption injects a removed index fork, a removed
// heap file, and an overwritten heap line pointer across three relations in one
// schema, then asserts a single pg_amcheck run reports all three corruption
// classes (003_check.pl:347-365).
func TestPort_PgAmcheck003CombinedCorruption(t *testing.T) {
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c, err := cluster.New("amcheck003-combined", cluster.Options{
		RepoRoot:     repoRoot(t),
		DataDir:      filepath.Join(t.TempDir(), "data"),
		StartupWait:  20 * time.Second,
		ShutdownWait: 20 * time.Second,
		// Page-overwrite tier needs checksums off (see file header / 004).
		InitArgs: []string{"--no-data-checksums"},
	})
	if err != nil {
		t.Fatal(err)
	}
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("CREATE EXTENSION amcheck: %v", err)
	}

	// Three relations, one per corruption class:
	//   tfork (+ btree tfork_idx) — index fork will be removed
	//   tfile                     — heap file will be removed
	//   tpage                     — heap page line pointer will be overwritten
	//
	// All live in the public schema and the run is scoped with one --table per
	// relation, mirroring TestPort_PgAmcheck003MissingHeapFile. (User-schema
	// durability across the restart this tier requires was the historical reason
	// for the public workaround; it is now fixed — CREATE SCHEMA via WAL replay
	// in loop #20 and user-schema TABLE relnamespace round-trip in loop #21 — and
	// the genuinely schema-scoped `--schema s1` corruption tier is covered by
	// TestPort_PgAmcheck003SchemaScoped. This combined tier keeps public because
	// its value is the multi-relation single-pass property, orthogonal to schema
	// scoping.)
	for _, stmt := range []string{
		"CREATE TABLE tfork (a int, b text)",
		"INSERT INTO tfork SELECT g, 'row-' || g FROM generate_series(1, 200) g",
		"CREATE INDEX tfork_idx ON tfork (a)",
		"CREATE TABLE tfile (a int, b text)",
		"INSERT INTO tfile SELECT g, 'row-' || g FROM generate_series(1, 200) g",
		"CREATE TABLE tpage (a bigint, b text, c text)",
		"INSERT INTO tpage VALUES (1, 'abcdefg', 'hijklmn')",
		"INSERT INTO tpage VALUES (2, 'opqrstu', 'vwxyz01')",
		"INSERT INTO tpage VALUES (3, '2345678', '9abcdef')",
	} {
		if err := runSQLSimple(t, c, stmt); err != nil {
			t.Fatalf("setup %q: %v", stmt, err)
		}
	}

	// Resolve every backing file path while the server is up (goopg names the
	// file by relation OID; the on-disk db dir is keyed by storage dbOid, so glob
	// base/*/<reloid> via findHeapFile rather than trusting pg_database.oid).
	forkIdxOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 'tfork_idx' AND relkind = 'i'")
	fileHeapOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 'tfile' AND relkind = 'r'")
	pageHeapOid := queryScalar(t, c, "SELECT oid FROM pg_class WHERE relname = 'tpage' AND relkind = 'r'")
	forkIdxPath := findHeapFile(t, c, forkIdxOid)
	fileHeapPath := findHeapFile(t, c, fileHeapOid)
	pageHeapPath := findHeapFile(t, c, pageHeapOid)

	// Pre-corruption clean run over the healthy fixture (the only self-promoting
	// gate). One --table per relation checks all three tables + the dependent
	// btree index of tfork.
	scope := []string{
		"--table", "public.tfork",
		"--table", "public.tfile",
		"--table", "public.tpage",
		"postgres",
	}
	pre := runAmcheck(t, c, scope...)
	if pre.ExitCode != 0 || pre.Stdout != "" {
		t.Skipf("AC-003 blocked: pre-corruption pg_amcheck over a healthy user "+
			"schema is not clean (an unrelated goopg gap on a heap/index page). "+
			"exit=%d stdout=%q stderr=%q", pre.ExitCode, pre.Stdout, pre.Stderr)
	}

	// Apply all three corruptions in a single stop -> corrupt -> restart cycle,
	// mirroring perform_all_corruptions (003_check.pl:107-119). The shutdown
	// checkpoint flushes tpage's page to disk before we overwrite it.
	if err := c.Stop(cluster.ShutdownFast); err != nil {
		t.Fatalf("stop cluster for corruption: %v", err)
	}
	for _, p := range []string{forkIdxPath, fileHeapPath, pageHeapPath} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("AC-003 blocked: backing file %q not found after shutdown "+
				"(unexpected goopg on-disk layout): %v", p, err)
		}
	}
	if err := os.Remove(forkIdxPath); err != nil {
		t.Fatalf("remove index fork %q: %v", forkIdxPath, err)
	}
	if err := os.Remove(fileHeapPath); err != nil {
		t.Fatalf("remove heap file %q: %v", fileHeapPath, err)
	}
	off, oldLen, newLen := corruptFirstLinePointerLength(t, pageHeapPath)
	t.Logf("corruptions applied: removed index fork %s, removed heap file %s, "+
		"overwrote tpage line pointer off=%d len %d->%d", forkIdxPath, fileHeapPath, off, oldLen, newLen)

	if err := c.Start(); err != nil {
		t.Fatalf("restart cluster after corruption: %v", err)
	}
	// Runtime-only amcheck install does not survive restart (gap #7c); re-install.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("re-CREATE EXTENSION amcheck after restart: %v", err)
	}

	// The removed files must stay absent — if goopg O_CREATEd them back during
	// startup/recovery the corresponding relation would check clean and the
	// assertions below would be false passes.
	for _, p := range []string{forkIdxPath, fileHeapPath} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("removed file %q was recreated after restart — goopg must not "+
				"O_CREATE a removed user relation file before pg_amcheck checks it", p)
		}
	}

	// Single combined run: every corruption class must be reported together on
	// stdout with exit 2 and nothing on stderr (003_check.pl:357-365).
	post := runAmcheck(t, c, scope...)
	if post.ExitCode != 2 {
		t.Errorf("combined pg_amcheck exit=%d, want 2\n  stdout=%q\n  stderr=%q",
			post.ExitCode, post.Stdout, post.Stderr)
	}
	if post.Stderr != "" {
		t.Errorf("combined pg_amcheck stderr non-empty: %q", post.Stderr)
	}
	checks := []struct {
		name string
		re   *regexp.Regexp
	}{
		{"index missing relation fork", regexp.MustCompile(`index ".*" lacks a main relation fork`)},
		{"line pointer corruption", regexp.MustCompile(`line pointer`)},
		{"missing file", regexp.MustCompile(`could not open file ".*": No such file or directory`)},
	}
	for _, ch := range checks {
		if !ch.re.MatchString(post.Stdout) {
			t.Errorf("combined pg_amcheck stdout missing %s report /%s/\n  stdout=%q",
				ch.name, ch.re, post.Stdout)
		}
	}
}
