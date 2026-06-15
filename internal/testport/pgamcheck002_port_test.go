package testport

// Port of postgres/src/bin/pg_amcheck/t/002_nonesuch.pl into a Go test
// (M0110-0003, slice S5 / row AC-002).
//
// 002_nonesuch.pl drives the real pg_amcheck binary against a live cluster and
// asserts its database/schema/table/index *pattern-resolution* behavior: the
// client compiles a database list from pg_database, gathers checkable relations,
// probes each connectable database for the amcheck extension, and rejects
// ungrammatical qualified names before ever touching a relation. None of these
// paths run verify_heapam()/bt_index_check() — they exercise pg_amcheck's
// catalog resolution + argument grammar.
//
// This is a SELF-PROMOTING reproduction (see WD-002 / loop #17 for the
// pattern): it drives the upstream pg_amcheck end-to-end against goopg and runs
// the full faithful assertion set. While goopg is missing the SQL features the
// client's bootstrap queries need, the preflight detects the goopg-side
// "query failed" signature and t.Skips with the precise blocker; the day those
// features land the skip clears and the assertions run unchanged.
//
// Empirically (loop this was filed), pg_amcheck's bootstrap queries surface
// three goopg SQL-engine gaps — all general features, none amcheck-specific:
//
//   1. `index` is rejected as a CTE name. The relation-gathering query defines
//      a CTE literally named "index" (pg_amcheck.c compile_relation_list_one_db);
//      goopg's parser errors: syntax error ... expected CTE name after ',' (got
//      index). PG accepts it (unreserved as a CTE name).
//   2. A VALUES list as a CTE's inner query reports 0 output columns. The
//      database-resolution query (compile_database_list) uses
//      `include_raw (pattern_id, rgx) AS (VALUES (0,'^(x)$'), ...)`; goopg
//      errors: CTE "include_raw" has 2 column aliases but inner query produces
//      0 columns — i.e. the analyzer does not derive a VALUES list's column
//      count when it backs a CTE.
//   3. Connecting to a non-existent database succeeds instead of failing with
//      `database "qqq" does not exist` (pg_amcheck qqq reached the relation
//      query rather than failing at connect).
//
// All three live in internal/parser, internal/analyzer and the connection
// handshake. Promote AC-002 to `port` once they are implemented and this test
// stops skipping.
//
// UPDATE (M0110-0003, parser/analyzer slice): gaps #1 and #2 are FIXED —
// parseWithClause now accepts an unreserved/col_name keyword (`index`) as a
// CTE name, and analyzer.registerAnalyzedCTE derives a VALUES-list CTE's
// column count from its first row (mirroring analyzeRecursiveCTE).
//
// UPDATE (M0110-0003, gap #3): FIXED — pg_database now registers
// template1/template0 (with per-DB datallowconn/datistemplate) and a
// non-replication connection to an unregistered database is rejected at
// startup with 3D000 `database "%s" does not exist`. Also a sub-gap #2b
// (CTE alias list shorter than the inner query) and gap #4 below.
//
// UPDATE (M0110-0003, gap #4): FIXED — a WITH-list CTE is now visible inside
// a non-correlated FROM-clause derived table of the OUTER statement
// (`WITH x AS (...) SELECT ... FROM (SELECT ... FROM x) s`).
// planSubqueryRangeVar previously re-planned the subquery via Plan(), which
// re-ran the analyzer standalone (no enclosing WITH scope) and rejected the
// CTE; it now uses planSelectWithParent (skips the re-analyze, inherits the
// planCTEs scope), mirroring the lateral branch.
//
// REMAINING blocker is now #5: pg_amcheck's per-relation heap check
// schema-qualifies the verify_heapam() function in the FROM clause
// (`"public".verify_heapam(...)`), which goopg's parser rejects — only an
// UNqualified FROM-clause table function parses. The preflight below probes
// for it directly so the test self-skips on #5 rather than failing.
//
// Like 001_basic, the bundled pg_amcheck links a PG-17+ libpq symbol
// (PQcancelBlocking), so it is run with LD_LIBRARY_PATH pointed at
// postgres/local_install/lib.
//
// Design doc: docs/design/0110-0003-pg-amcheck-tap-port.md (M0110-0003).
// CSV row: AC-002.

import (
	"net"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// amcheckEnv builds the environment for an in-tree pg_amcheck invocation: the
// bundled libpq plus libpq connection variables pointed at the cluster. The
// PostgreSQL TAP harness injects PGHOST/PGPORT the same way (a positional
// dbname or --username on the command line overrides PGDATABASE/PGUSER).
func amcheckEnv(t *testing.T, c *cluster.Cluster) []string {
	t.Helper()
	host, port, err := net.SplitHostPort(c.ListenAddr())
	if err != nil {
		t.Fatalf("split listen addr %q: %v", c.ListenAddr(), err)
	}
	libDir := filepath.Join(repoRoot(t), "postgres", "local_install", "lib")
	return []string{
		"LD_LIBRARY_PATH=" + libDir,
		"PGHOST=" + host,
		"PGPORT=" + port,
		"PGUSER=postgres",
		"PGDATABASE=postgres",
		"PGPASSWORD=",
	}
}

// runAmcheck runs the bundled pg_amcheck with connection env pointed at c.
func runAmcheck(t *testing.T, c *cluster.Cluster, args ...string) util.CommandResult {
	t.Helper()
	bin := clientToolBin(t, "pg_amcheck")
	res, err := util.RunCommand(util.CommandSpec{
		Name:    bin,
		Args:    args,
		Env:     amcheckEnv(t, c),
		Timeout: 30 * time.Second,
	})
	if err != nil {
		t.Fatalf("run pg_amcheck %v: %v", args, err)
	}
	return res
}

// checkAmcheck mirrors PostgreSQL::Test::command_checks_all: run pg_amcheck with
// the given args, assert the exit code, an empty stdout, and that every stderr
// regex matches somewhere in the captured stderr.
func checkAmcheck(t *testing.T, c *cluster.Cluster, desc string, args []string, wantExit int, stderrRes []string) {
	t.Helper()
	res := runAmcheck(t, c, args...)
	if res.ExitCode != wantExit {
		t.Errorf("[%s] exit=%d want %d\n  args=%v\n  stdout=%q\n  stderr=%q",
			desc, res.ExitCode, wantExit, args, res.Stdout, res.Stderr)
	}
	if res.Stdout != "" {
		t.Errorf("[%s] stdout non-empty: %q", desc, res.Stdout)
	}
	for _, pat := range stderrRes {
		re := regexp.MustCompile(pat)
		if !re.MatchString(res.Stderr) {
			t.Errorf("[%s] stderr missing /%s/\n  stderr=%q", desc, pat, res.Stderr)
		}
	}
}

func TestPort_PgAmcheck002Nonesuch(t *testing.T) {
	// upstream: postgres/src/bin/pg_amcheck/t/002_nonesuch.pl
	if clientToolBin(t, "pg_amcheck") == "" {
		t.Skip("pg_amcheck not in PATH or postgres/local_install/bin")
	}
	c := newCluster(t, "amcheck002")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Load the amcheck extension, upon which pg_amcheck depends.
	if err := runSQLSimple(t, c, "CREATE EXTENSION amcheck"); err != nil {
		t.Fatalf("CREATE EXTENSION amcheck: %v", err)
	}

	// Preflight: a --no-strict-names run over an unresolvable pattern exercises
	// pg_amcheck's database-resolution (VALUES-CTE) bootstrap query. Upstream it
	// exits 0 with a warning; while goopg lacks the bootstrap SQL features it
	// fails the query and prints "query failed". Skip (self-promoting) with the
	// precise blocker rather than reporting a misleading FAIL.
	pre := runAmcheck(t, c, "--no-strict-names", "--database", "qqq", "--database", "postgres")
	if strings.Contains(pre.Stderr, "query failed") {
		t.Skipf("AC-002 blocked: pg_amcheck's bootstrap queries hit goopg SQL gaps "+
			"(see file header) — `index` as a CTE name, VALUES-list-as-CTE column "+
			"derivation, and non-existent-database connect rejection. stderr=%q", pre.Stderr)
	}

	// Blockers #1 (`index` CTE name) and #2 (VALUES-list-as-CTE column
	// derivation) are fixed (M0110-0003, loop landing the parser/analyzer
	// slices), so the bootstrap "query failed" signature above may already be
	// gone. The remaining blocker #3 is connection-level: goopg does not yet
	// reject a connection to a non-existent database at startup (3D000), and
	// template1/template0 are not registered in pg_database. Until then
	// pg_amcheck reaches the relation query for `qqq` instead of failing with
	// `database "qqq" does not exist`, so the first assertion below would FAIL
	// rather than the suite skipping. Probe for that gap and self-skip with the
	// precise remaining blocker; the day #3 lands this clears and the full
	// assertion set runs unchanged.
	db := runAmcheck(t, c, "qqq")
	if !strings.Contains(db.Stderr, `database "qqq" does not exist`) {
		t.Skipf("AC-002 blocked on remaining gap #3: goopg does not reject a "+
			"connection to a non-existent database at startup (3D000), and "+
			"template1/template0 are not registered in pg_database. stderr=%q", db.Stderr)
	}

	// Probe for remaining gap #5 (M0110-0003): pg_amcheck builds its
	// per-relation heap check as
	//   ... FROM pg_catalog.pg_class c, "public".verify_heapam(...) v
	// i.e. a SCHEMA-QUALIFIED function call in the FROM clause. goopg's
	// parser only accepts an UNqualified function name as a FROM-clause
	// table function, so the qualified form fails with `syntax error at or
	// near "...(got ()"`. That surfaces in pg_amcheck's per-relation stdout
	// (`verify_heapam(... syntax error ...`) and also breaks the database
	// pattern-resolution assertions below (the relation-gathering query also
	// schema-qualifies functions). Self-skip with the precise blocker; the
	// day schema-qualified FROM-clause functions parse, this clears and the
	// full assertion set runs unchanged.
	heap := runAmcheck(t, c, "postgres")
	if strings.Contains(heap.Stdout, "verify_heapam(") && strings.Contains(heap.Stdout, "syntax error") {
		t.Skipf("AC-002 blocked on remaining gap #5: goopg's parser rejects a "+
			"schema-qualified function call in the FROM clause "+
			"(`\"public\".verify_heapam(...)`); only unqualified FROM-clause table "+
			"functions parse. stdout=%q", heap.Stdout)
	}

	// --- Non-existent databases ------------------------------------------
	checkAmcheck(t, c, "non-existent database",
		[]string{"qqq"}, 1,
		[]string{`database "qqq" does not exist`})

	checkAmcheck(t, c, "unresolvable database pattern",
		[]string{"--database", "qqq", "--database", "postgres"}, 1,
		[]string{`pg_amcheck: error: no connectable databases to check matching "qqq"`})

	checkAmcheck(t, c, "unresolvable pattern under --no-strict-names",
		[]string{"--no-strict-names", "--database", "qqq", "--database", "postgres"}, 0,
		[]string{`pg_amcheck: warning: no connectable databases to check matching "qqq"`})

	checkAmcheck(t, c, "substring of existent database",
		[]string{"--database", "post", "--database", "postgres"}, 1,
		[]string{`pg_amcheck: error: no connectable databases to check matching "post"`})

	checkAmcheck(t, c, "superstring of existent database",
		[]string{"--database", "postgresql", "--database", "postgres"}, 1,
		[]string{`pg_amcheck: error: no connectable databases to check matching "postgresql"`})

	// --- Connecting with a non-existent user -----------------------------
	checkAmcheck(t, c, "non-existent user",
		[]string{"--username", "no_such_user", "postgres"}, 1,
		[]string{`role "no_such_user" does not exist`})

	// --- Databases without amcheck installed -----------------------------
	checkAmcheck(t, c, "by name without amcheck, no other databases",
		[]string{"template1"}, 1,
		[]string{
			`pg_amcheck: warning: skipping database "template1": amcheck is not installed`,
			`pg_amcheck: error: no relations to check`,
		})

	checkAmcheck(t, c, "by name without amcheck, with other databases",
		[]string{"--database", "template1", "--database", "postgres"}, 0,
		[]string{`pg_amcheck: warning: skipping database "template1": amcheck is not installed`})

	checkAmcheck(t, c, "by pattern without amcheck, with other databases",
		[]string{"--all"}, 0,
		[]string{`pg_amcheck: warning: skipping database "template1": amcheck is not installed`})

	// --- Unreasonable patterns (pure client-side grammar) ----------------
	checkAmcheck(t, c, `table pattern ".."`,
		[]string{"--database", "postgres", "--table", ".."}, 1,
		[]string{`pg_amcheck: error: no connectable databases to check matching "\.\."`})

	checkAmcheck(t, c, `table pattern ".foo.bar"`,
		[]string{"--database", "postgres", "--table", ".foo.bar"}, 1,
		[]string{`pg_amcheck: error: no connectable databases to check matching "\.foo\.bar"`})

	checkAmcheck(t, c, `table pattern "."`,
		[]string{"--database", "postgres", "--table", "."}, 1,
		[]string{`pg_amcheck: error: no heap tables to check matching "\."`})

	checkAmcheck(t, c, "multipart database name rejected",
		[]string{"--database", "localhost.postgres"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): localhost\.postgres`})

	checkAmcheck(t, c, "three-part schema name rejected",
		[]string{"--schema", "localhost.postgres.pg_catalog"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): localhost\.postgres\.pg_catalog`})

	checkAmcheck(t, c, "four-part table name rejected",
		[]string{"--table", "localhost.postgres.pg_catalog.pg_class"}, 2,
		[]string{`pg_amcheck: error: improper relation name \(too many dotted names\): localhost\.postgres\.pg_catalog\.pg_class`})

	// --- Too many dotted names under --no-strict-names -------------------
	checkAmcheck(t, c, "ungrammatical table under --no-strict-names",
		[]string{"--no-strict-names", "--table", "this.is.a.really.long.dotted.string"}, 2,
		[]string{`pg_amcheck: error: improper relation name \(too many dotted names\): this\.is\.a\.really\.long\.dotted\.string`})

	checkAmcheck(t, c, "ungrammatical schema under --no-strict-names",
		[]string{"--no-strict-names", "--schema", "postgres.long.dotted.string"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): postgres\.long\.dotted\.string`})

	checkAmcheck(t, c, "ungrammatical database under --no-strict-names",
		[]string{"--no-strict-names", "--database", "postgres.long.dotted.string"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): postgres\.long\.dotted\.string`})

	// Exclusion patterns
	checkAmcheck(t, c, "ungrammatical exclude-table",
		[]string{"--no-strict-names", "--exclude-table", "a.b.c.d"}, 2,
		[]string{`pg_amcheck: error: improper relation name \(too many dotted names\): a\.b\.c\.d`})

	checkAmcheck(t, c, "ungrammatical exclude-schema",
		[]string{"--no-strict-names", "--exclude-schema", "a.b.c"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): a\.b\.c`})

	checkAmcheck(t, c, "ungrammatical exclude-database",
		[]string{"--no-strict-names", "--exclude-database", "a.b"}, 2,
		[]string{`pg_amcheck: error: improper qualified name \(too many dotted names\): a\.b`})
}
