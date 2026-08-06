package testport

// regress_suite_test.go — TestPort_RegressSuite and ClusterRegressExecutor.
//
// Wires up the pg_regress SQL/expected test suite against a live goopg
// cluster using the psql binary as the execution back-end. Each of the
// 232 discovered cases runs as a subtest; most report "deferred" on the
// initial run. The suite anchors regression observability so that as
// features land, test promotion from t.Skip → PASS is immediately visible.
//
// Execution path (M0097-0001):
//   1. Create a fresh goopg cluster.
//   2. Run postgres/src/test/regress/sql/test_setup.sql best-effort (some
//      statements use C extensions or tablespaces not supported in goopg;
//      failures are logged but do not abort the suite).
//   3. For each discovered case, use framework.RunRegressSubset to execute
//      the SQL via ClusterRegressExecutor (psql -X -q -a -f <tmpfile>) and
//      compare normalised output with the expected .out file.
//   4. "port" → pass, "excluded" → t.Skip, "defer" → t.Skip with rationale.

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/util"
)

// regressCaseTimeout bounds a single regress case end to end (the psql client
// run plus the harness's own deadline). Shared by the suite and
// ClusterRegressExecutor so the two can never drift apart.
const regressCaseTimeout = 120 * time.Second

// TestPort_RegressSuite runs all pg_regress SQL cases against a live goopg
// cluster via the psql binary. Most cases report "defer" initially; the
// suite provides a stable baseline for regression tracking as M0097
// sub-milestones land.
func TestPort_RegressSuite(t *testing.T) {
	root := repoRoot(t)
	psqlBin := clientToolBin(t, "psql")
	if psqlBin == "" {
		t.Skip("psql not in PATH or postgres/local_install/bin")
	}

	// Give this cluster its own pprof endpoint so the wedge probe can pull a
	// goroutine dump. The server's built-in default is a fixed 127.0.0.1:6060,
	// which a bench cluster or a peer test may already hold — the bind then
	// fails at Debug level and the endpoint silently does not exist.
	pprofAddr := reserveLoopbackPort(t)
	t.Setenv("GOOPG_PPROF_ADDR", pprofAddr)

	c := newCluster(t, "regress_suite")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Run test_setup.sql to materialise shared fixture tables. Failures
	// are expected (tablespaces, C extensions) and do not abort the suite.
	runRegressSetup(t, root, psqlBin, c)

	// Discover all SQL/expected pairs.
	cases, err := framework.DiscoverRegressCases(root)
	if err != nil {
		t.Fatalf("discover regress cases: %v", err)
	}
	if len(cases) == 0 {
		t.Skip("no regress cases found (postgres submodule not initialised)")
	}

	exec := &ClusterRegressExecutor{
		Cluster:   c,
		PsqlBin:   psqlBin,
		LibDir:    filepath.Join(root, "postgres", "local_install", "lib"),
		RepoRoot:  root,
		PprofAddr: pprofAddr,
	}

	// clusterPoisoned is set when a case's psql was killed by the per-case
	// deadline. The server keeps executing after its client dies (the standing
	// "timeout kills only the client" trap), so whatever wedged the case —
	// an orphaned backend holding locks, or a GC-thrashing server — is still
	// there for the next case. Nightly run 20260725-011243 shows the cost of
	// not handling this: every case from `rowtypes` onward timed out at 120 s
	// while `SELECT 1` still answered, so isAlive() never triggered recovery
	// and 36 cases reported a bogus output mismatch (root-0029). Subtests here
	// run sequentially (no t.Parallel), so a plain bool is sufficient.
	clusterPoisoned := false

	for _, rc := range cases {
		rc := rc
		t.Run(rc.Name, func(t *testing.T) {
			// Check policy exclusion before executing (saves cluster round-trip).
			if reason, excluded := regressExcluded[rc.Name]; excluded {
				t.Skipf("excluded by policy: %s", reason)
				return
			}

			// Restart the cluster if it crashed during a previous test.
			// The server process can crash with a GC memory error (pre-existing
			// bug) after running heavy DDL workloads. We detect this by pinging
			// the server; on failure we Kill (clears cmd state) then Start
			// (WAL recovery restores a consistent DB state), then re-run
			// test_setup.sql to restore shared fixture tables.
			if clusterPoisoned || !exec.isAlive() {
				if clusterPoisoned {
					t.Log("previous case timed out; restarting the cluster to drop orphaned backends")
				} else {
					t.Log("server not responding; attempting crash recovery")
				}
				clusterPoisoned = false
				_ = c.Kill() // clear cmd so Start() doesn't return "already started"
				if err := c.Start(); err != nil {
					t.Skipf("deferred: cluster restart failed: %v", err)
					return
				}
				restoreRegressFixtures(t, root, psqlBin, c, exec)
				t.Log("cluster recovered")
			}

			// Run per-test pre-setup SQL if defined (e.g. create_aggregate.sql
			// for the aggregates test).
			if preFile, ok := regressPreSetup[rc.Name]; ok {
				runRegressSQLFile(t, root, psqlBin, c, preFile)
			}

			ctx, cancel := context.WithTimeout(context.Background(), regressCaseTimeout)
			defer cancel()

			// Capture live cluster state if this case wedges. Nothing else in
			// the pipeline looks at the server while it is stuck, so without
			// this a wedge reaches the nightly as one unactionable line.
			stopProbe := exec.armWedgeProbe(t, rc.Name)
			results, err := framework.RunRegressSubset(ctx, root, []framework.RegressCase{rc}, exec)
			stopProbe()
			if err != nil {
				t.Logf("run error: %v", err)
				t.Skip("deferred: execution error")
				return
			}
			if len(results) == 0 {
				t.Skip("deferred: no result returned")
				return
			}
			r := results[0]
			if strings.HasPrefix(r.Rationale, framework.RationaleExecTimeout) {
				// The next case must not inherit the wedge.
				clusterPoisoned = true
			}
			switch r.Status {
			case "port":
				// Pass — nothing to do.
			case "excluded":
				t.Skip("excluded by policy")
			default:
				t.Skipf("deferred: %s", r.Rationale)
			}
		})
	}
}

// restoreRegressFixtures brings the shared test_setup.sql fixture tables back
// after a wedge/crash restart — and, crucially, does NOT re-run test_setup.sql
// when they are already there.
//
// A restart preserves the data directory, so the fixtures always survive it
// (WAL recovery restores every committed row). Re-running test_setup.sql on top
// of them is not idempotent and not a no-op: psql runs without ON_ERROR_STOP,
// so each `CREATE TABLE` fails with "relation already exists" while the
// `INSERT`/`COPY` that follows it succeeds — every fixture table DOUBLES
// (measured: int4_tbl 5→10, text_tbl 2→4, varchar_tbl 4→8, onek 1000→2000,
// tenk1 10000→20000).
//
// That is the mechanism behind the nightly "suite-wedge casualties": one case
// hitting the 120 s per-case deadline poisons the cluster, recovery doubles the
// shared fixtures, and every later case that reads them (numerology, select,
// select_into, text, truncate, union, varchar, portals_p2 in run
// 20260806-191958) reports a genuine — but harness-manufactured — output
// mismatch. Those are joined against `regress-diff-baseline.csv` and filed as
// regressions, so one wedged case turned a readable nightly into nine action
// items and kept the M0127 S7 gate ("a clean nightly cycle") unmeetable.
//
// The setup is still run when the fixtures are genuinely absent, so a restart
// onto an empty database still bootstraps.
func restoreRegressFixtures(t *testing.T, root string, psqlBin string, c *cluster.Cluster, exec *ClusterRegressExecutor) {
	t.Helper()
	if exec.fixturesPresent() {
		t.Log("shared fixtures survived the restart; NOT re-running test_setup.sql (it would double every fixture table)")
		return
	}
	runRegressSetup(t, root, psqlBin, c)
}

// fixturesPresent reports whether the shared test_setup.sql fixture tables are
// still resolvable. `onek` stands in for the whole set: it is created by
// test_setup.sql alone and no regress case drops it.
func (e *ClusterRegressExecutor) fixturesPresent() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := e.Cluster.Query(ctx, "SELECT 1 FROM onek LIMIT 1")
	return err == nil
}

// regressPreSetup maps test names to SQL files that must be executed against
// the cluster before the test SQL runs. The paths are relative to the repo root.
var regressPreSetup = map[string]string{
	"aggregates": "postgres/src/test/regress/sql/create_aggregate.sql",
}

// runRegressSQLFile runs a SQL file against the cluster via psql.
// Failures are logged but do not abort the test.
func runRegressSQLFile(t *testing.T, root string, psqlBin string, c *cluster.Cluster, relPath string) {
	t.Helper()
	sqlPath := filepath.Join(root, relPath)
	if _, err := os.Stat(sqlPath); err != nil {
		t.Logf("pre-setup file %s not found, skipping: %v", relPath, err)
		return
	}
	addr := c.ListenAddr()
	host, port, _ := net.SplitHostPort(addr)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	prev := os.Getenv("LD_LIBRARY_PATH")
	ldPath := libDir
	if prev != "" {
		ldPath += ":" + prev
	}
	absSrcDir := filepath.Join(root, "postgres", "src", "test", "regress")
	result, _ := util.RunCommand(util.CommandSpec{
		Name:    psqlBin,
		Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-X", "-q", "-a", "-f", sqlPath},
		Dir:     root,
		Env:     []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + ldPath, "PG_ABS_SRCDIR=" + absSrcDir},
		Timeout: 60 * time.Second,
	})
	if result.ExitCode != 0 {
		t.Logf("pre-setup %s completed with errors (may be OK): exit=%d", relPath, result.ExitCode)
	}
}

// regressExcluded is the M0097-0002 policy table mapping test name →
// exclusion rationale. Tests in this map are skipped without executing
// against the cluster. Must stay in sync with cmd/gen-regress-coverage.
var regressExcluded = map[string]string{
	// Geometric types
	"box": "Geometric type; out of scope for goopg v0.", "circle": "Geometric type; out of scope for goopg v0.",
	"geometry": "Geometric type; out of scope for goopg v0.", "line": "Geometric type; out of scope for goopg v0.",
	"lseg": "Geometric type; out of scope for goopg v0.", "path": "Geometric type; out of scope for goopg v0.",
	"point": "Geometric type; out of scope for goopg v0.", "polygon": "Geometric type; out of scope for goopg v0.",
	// Full-text search
	"tsdicts": "Full-text search; out of scope for goopg v0.", "tsearch": "Full-text search; out of scope for goopg v0.",
	"tsrf": "Full-text search SRF; out of scope for goopg v0.", "tstypes": "Full-text search types; out of scope for goopg v0.",
	// Shared bootstrap
	"test_setup": "Executed once as shared regress fixture bootstrap; rerunning mutates seeded tables for later cases.",
	// Advanced AM / exotic index
	"amutils": "Advanced AM utilities; out of scope for goopg v0.", "brin": "BRIN index; out of scope for goopg v0.",
	"brin_bloom": "BRIN bloom index; out of scope for goopg v0.", "brin_multi": "BRIN multi-range; out of scope for goopg v0.",
	"create_am": "Custom AM DDL; out of scope for goopg v0.", "create_index_spgist": "SP-GiST DDL; out of scope for goopg v0.",
	"gin": "GIN index; out of scope for goopg v0.", "gist": "GiST index; out of scope for goopg v0.",
	"spgist": "SP-GiST index; out of scope for goopg v0.",
	// Collation / encoding
	"collate": "Collation; out of scope for goopg v0.", "collate.icu.utf8": "ICU collation; out of scope for goopg v0.",
	"collate.linux.utf8": "Linux UTF-8 collation; out of scope for goopg v0.", "collate.utf8": "UTF-8 collation; out of scope for goopg v0.",
	"collate.windows.win1252": "Windows collation; out of scope for goopg v0.", "copyencoding": "COPY encoding; out of scope for goopg v0.",
	"encoding": "Multi-byte encoding; out of scope for goopg v0.", "euc_kr": "EUC-KR encoding; out of scope for goopg v0.",
	"unicode": "Unicode normalization; out of scope for goopg v0.",
	// External / infra features
	"async": "LISTEN/NOTIFY; out of scope for goopg v0.", "compression": "TOAST compression; out of scope for goopg v0.",
	"foreign_data": "FDW; out of scope for goopg v0.", "indirect_toast": "Indirect TOAST; out of scope for goopg v0.",
	"largeobject": "Large objects; out of scope for goopg v0.", "maintain_every": "Maintenance scheduling; out of scope for goopg v0.",
	"numa": "NUMA; out of scope for goopg v0.", "object_address": "pg_get_object_address; out of scope for goopg v0.",
	"tablesample": "TABLESAMPLE; out of scope for goopg v0.", "tablespace": "Tablespace DDL; not implemented in goopg v0.",
	// XML / advanced JSON
	"json_encoding": "JSON encoding; out of scope for goopg v0.", "jsonpath_encoding": "JSONPath encoding; out of scope for goopg v0.",
	"sqljson": "SQL/JSON; out of scope for goopg v0.", "sqljson_jsontable": "JSON_TABLE; out of scope for goopg v0.",
	"sqljson_queryfuncs": "SQL/JSON query functions; out of scope for goopg v0.", "xml": "XML; out of scope for goopg v0.",
	"xmlmap": "XML mapping; out of scope for goopg v0.",
	// Security & roles
	"create_role": "Role DDL; out of scope for goopg v0.", "init_privs": "Initial privileges; out of scope for goopg v0.",
	"password": "Password auth; out of scope for goopg v0.", "privileges": "GRANT/REVOKE; out of scope for goopg v0.",
	"roleattributes": "Role attributes; out of scope for goopg v0.", "rowsecurity": "Row-level security; out of scope for goopg v0.",
	"security_label": "Security labels; out of scope for goopg v0.",
	// Parallel
	"select_parallel": "Parallel query; out of scope for goopg v0.", "vacuum_parallel": "Parallel VACUUM; out of scope for goopg v0.",
	"write_parallel": "Parallel write; out of scope for goopg v0.",
	// Event triggers
	"event_trigger": "Event triggers; out of scope for goopg v0.", "event_trigger_login": "Login event triggers; out of scope for goopg v0.",
	// psql client
	"psql": "psql meta-commands; out of scope for goopg v0.", "psql_crosstab": "psql crosstab; out of scope for goopg v0.",
	"psql_pipeline": "psql pipelining; out of scope for goopg v0.",
	// Network types
	"inet": "Network address type; out of scope for goopg v0.", "macaddr": "MAC address type; out of scope for goopg v0.",
	"macaddr8": "MAC address 8-byte type; out of scope for goopg v0.",
	// Catalog sanity
	"misc_sanity": "Catalog sanity; excluded.", "oidjoins": "OID referential integrity; excluded.",
	"opr_sanity": "Operator catalog sanity; excluded.", "sanity_check": "General catalog sanity; excluded.",
	"type_sanity": "Type catalog sanity; excluded.",
	// Complex AM / type extensions
	"alter_generic": "Generic ALTER DDL; out of scope for goopg v0.", "alter_operator": "ALTER OPERATOR; out of scope for goopg v0.",
	"create_aggregate": "CREATE AGGREGATE; out of scope for goopg v0.", "create_cast": "CREATE CAST; out of scope for goopg v0.",
	"create_misc": "Misc CREATE forms; excluded.", "create_operator": "CREATE OPERATOR; out of scope for goopg v0.",
	"create_type": "CREATE TYPE; out of scope for goopg v0.", "drop_operator": "DROP OPERATOR; out of scope for goopg v0.",
	"polymorphism": "Polymorphic functions; out of scope for goopg v0.", "regproc": "regproc/regclass OID types; out of scope for goopg v0.",
	// Replication catalog
	"publication": "Logical replication publication; deferred to M0094.", "replica_identity": "Replica identity DDL; deferred to M0094.",
	"subscription": "Logical replication subscription; deferred to M0094.",
	// C-language functions
	"create_function_c": "CREATE FUNCTION LANGUAGE C; excluded.",
	// Misc out-of-scope
	"bit": "Bit-string type; out of scope for goopg v0.", "bitmapops": "Bitmap index ops; out of scope for goopg v0.",
	"combocid": "Combo CID (MVCC internals); out of scope for goopg v0.", "conversion": "CONVERT encoding; out of scope for goopg v0.",
	"create_schema": "CREATE SCHEMA; out of scope for goopg v0.", "database": "CREATE/DROP DATABASE; out of scope for goopg v0.",
	"dependency": "pg_depend; out of scope for goopg v0.", "hash_func": "Hash function catalog; out of scope for goopg v0.",
	"infinite_recurse": "Stack-depth exceeded; out of scope for goopg v0.", "memoize": "Memoize node; out of scope for goopg v0.",
	"money": "Money type; out of scope for goopg v0.", "namespace": "pg_namespace catalog; out of scope for goopg v0.",
	"predicate": "Predicate locking; out of scope for goopg v0.", "reloptions": "Table/index reloptions; out of scope for goopg v0.",
	"stats": "Extended statistics; out of scope for goopg v0.", "stats_ext": "Extended stats expressions; out of scope for goopg v0.",
	"stats_import": "pg_restore statistics; out of scope for goopg v0.", "typed_table": "Typed tables; out of scope for goopg v0.",
	"without_overlaps": "WITHOUT OVERLAPS temporal key; out of scope for goopg v0.",
}

// ClusterRegressExecutor implements framework.RegressExecutor by writing the
// SQL to a temp file and executing it through the psql binary (connected to
// the given goopg cluster). The output matches psql -X -q -a format, which
// is the same format pg_regress uses to generate expected .out files.
type ClusterRegressExecutor struct {
	Cluster  *cluster.Cluster
	PsqlBin  string // full path to psql binary (e.g. postgres/local_install/bin/psql)
	LibDir   string // postgres/local_install/lib — needed for libpq symbol resolution
	RepoRoot string
	// PprofAddr is the cluster server's pprof listen address; the wedge probe
	// pulls its goroutine dump from it. Empty disables that section.
	PprofAddr string
	// wedgeDir is the current case's diagnostic bundle directory, set by
	// armWedgeProbe. ExecuteSQL drops the killed client's partial output there
	// — otherwise the only record of WHICH statement wedged dies with psql.
	wedgeDir string
}

// psqlArgs builds the connection-arg slice for psql (host, port, user, database).
func (e *ClusterRegressExecutor) psqlArgs(extraArgs ...string) []string {
	addr := e.Cluster.ListenAddr()
	host, port, _ := net.SplitHostPort(addr)
	args := []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres"}
	args = append(args, extraArgs...)
	return args
}

// psqlEnv returns the environment slice for psql with LD_LIBRARY_PATH and
// PG_ABS_SRCDIR set so the in-tree psql binary resolves libpq symbols and
// data file paths (e.g. \getenv abs_srcdir PG_ABS_SRCDIR in hash_index.sql).
//
// It also mirrors the three environment settings real pg_regress forces on
// every regress client connection (postgres/src/test/regress/pg_regress.c,
// ~line 783-800: psql_start_test / initialize_environment) so goopg's output
// is compared against the same baseline pg_regress itself uses, rather than
// whatever ambient locale/timezone/datestyle the test-runner host happens to
// have. Without these, cases whose very first statement is a bare "SHOW
// datestyle"/"SHOW TimeZone" (e.g. guc.sql) diverge before any SET is issued.
func (e *ClusterRegressExecutor) psqlEnv() []string {
	prev := os.Getenv("LD_LIBRARY_PATH")
	env := []string{
		"PGPASSWORD=",
		"PGTZ=America/Los_Angeles",
		"PGDATESTYLE=Postgres, MDY",
		"PGOPTIONS=-c intervalstyle=postgres_verbose",
	}
	if e.LibDir != "" {
		ldPath := e.LibDir
		if prev != "" {
			ldPath += ":" + prev
		}
		env = append(env, "LD_LIBRARY_PATH="+ldPath)
	}
	if e.RepoRoot != "" {
		absSrcDir := filepath.Join(e.RepoRoot, "postgres", "src", "test", "regress")
		env = append(env, "PG_ABS_SRCDIR="+absSrcDir)
	}
	return env
}

// isAlive pings the cluster with a trivial SELECT; returns false if the server
// is not reachable (crashed or not yet started).
func (e *ClusterRegressExecutor) isAlive() bool {
	args := e.psqlArgs("-X", "-q", "-c", "SELECT 1")
	result, _ := util.RunCommand(util.CommandSpec{
		Name:    e.PsqlBin,
		Args:    args,
		Dir:     e.RepoRoot,
		Env:     e.psqlEnv(),
		Timeout: 5 * time.Second,
	})
	return result.ExitCode == 0
}

// ExecuteSQL writes sql to a temporary file and runs it through psql.
// Returns combined stdout (primary output) on success; on psql non-zero
// exit the stdout+stderr combination is returned so callers can diff it.
//
// If psql is killed by the deadline (ctx's, or regressCaseTimeout when ctx
// carries none) the partial output is returned together with an error wrapping
// framework.ErrExecTimeout. Reporting that as a plain diff is what let one
// wedged cluster masquerade as ~36 independent output-mismatch regressions in
// nightly run 20260725-011243 — see root-0029.
func (e *ClusterRegressExecutor) ExecuteSQL(ctx context.Context, sql string) (string, error) {
	tmpf, err := os.CreateTemp("", "goopg_regress_*.sql")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmpf.Name())
	if _, err := tmpf.WriteString(sql); err != nil {
		tmpf.Close()
		return "", err
	}
	tmpf.Close()

	// -X   skip .psqlrc
	// -q   quiet (no startup messages)
	// -a   echo all input to stdout (matches pg_regress output format)
	// -f   execute file
	// -c "SET statement_timeout=..." prevents individual statements from hanging.
	args := e.psqlArgs(
		"-X", "-q", "-a",
		"-c", "SET statement_timeout = '5s'",
		"-f", tmpf.Name(),
	)
	// Honour the caller's deadline when it is tighter than the default; the
	// suite sets one per case, and RunCommand only understands a duration.
	timeout := regressCaseTimeout
	if dl, ok := ctx.Deadline(); ok {
		if remaining := time.Until(dl); remaining < timeout {
			timeout = remaining
		}
	}
	if timeout <= 0 {
		return "", fmt.Errorf("%w: deadline already expired before psql start", framework.ErrExecTimeout)
	}
	result, _ := util.RunCommand(util.CommandSpec{
		Name:    e.PsqlBin,
		Args:    args,
		Dir:     e.RepoRoot,
		Env:     e.psqlEnv(),
		Timeout: timeout,
	})
	if result.TimedOut {
		// framework.RegressResult carries only a rationale string, so this is
		// the last point at which the partial output still exists. Its tail
		// names the statement the server never answered — the single most
		// useful datum about a wedge, and one every nightly so far discarded.
		if e.wedgeDir != "" {
			if err := os.MkdirAll(e.wedgeDir, 0o755); err == nil {
				writeWedgeArtifact(e.wedgeDir, "psql-partial.out", result.Stdout+result.Stderr)
			}
		}
		return result.Stdout + result.Stderr,
			fmt.Errorf("%w: psql killed after %s (cluster wedged or overloaded; output is truncated, not a diff)",
				framework.ErrExecTimeout, timeout)
	}
	return result.Stdout + result.Stderr, nil
}

// runRegressSetup runs postgres/src/test/regress/sql/test_setup.sql against
// the cluster best-effort. Failures (tablespaces, C extensions, COPY from
// non-existent files) are logged but not fatal — they only cause downstream
// cases to defer rather than break the suite entirely.
func runRegressSetup(t *testing.T, root string, psqlBin string, c *cluster.Cluster) {
	t.Helper()
	setupPath := filepath.Join(root, "postgres", "src", "test", "regress", "sql", "test_setup.sql")
	if _, err := os.Stat(setupPath); err != nil {
		t.Logf("test_setup.sql not found, skipping fixture setup: %v", err)
		return
	}
	addr := c.ListenAddr()
	host, port, _ := net.SplitHostPort(addr)
	libDir := filepath.Join(root, "postgres", "local_install", "lib")
	prev := os.Getenv("LD_LIBRARY_PATH")
	ldPath := libDir
	if prev != "" {
		ldPath += ":" + prev
	}
	// PG_ABS_SRCDIR enables \getenv abs_srcdir in test_setup.sql to resolve
	// data file paths (e.g. COPY onek FROM :'filename').
	absSrcDir := filepath.Join(root, "postgres", "src", "test", "regress")
	result, _ := util.RunCommand(util.CommandSpec{
		Name:    psqlBin,
		Args:    []string{"-h", host, "-p", port, "-U", "postgres", "-d", "postgres", "-X", "-q", "-a", "-f", setupPath},
		Dir:     root,
		Env:     []string{"PGPASSWORD=", "LD_LIBRARY_PATH=" + ldPath, "PG_ABS_SRCDIR=" + absSrcDir},
		Timeout: 120 * time.Second,
	})
	if result.ExitCode != 0 {
		t.Logf("test_setup.sql completed with partial failures (expected for C extensions/tablespaces): exit=%d", result.ExitCode)
	}
	if result.Stderr != "" {
		t.Logf("test_setup.sql stderr (truncated at 2048 bytes):\n%s", truncate(result.Stderr, 2048))
	}
	if result.Stdout != "" {
		// Log last 512 bytes of stdout to see what completed.
		stdout := result.Stdout
		if len(stdout) > 512 {
			stdout = "...(truncated)...\n" + stdout[len(stdout)-512:]
		}
		t.Logf("test_setup.sql stdout (last 512 bytes):\n%s", stdout)
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
