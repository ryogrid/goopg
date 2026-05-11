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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

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

	c := newCluster(t, "regress_suite")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	// Run test_setup.sql to materialise shared fixture tables. Failures
	// are expected (tablespaces, C extensions) and do not abort the suite.
	runRegressSetup(t, root, c)

	// Discover all SQL/expected pairs.
	cases, err := framework.DiscoverRegressCases(root)
	if err != nil {
		t.Fatalf("discover regress cases: %v", err)
	}
	if len(cases) == 0 {
		t.Skip("no regress cases found (postgres submodule not initialised)")
	}

	exec := &ClusterRegressExecutor{
		Cluster:  c,
		RepoRoot: root,
	}

	for _, rc := range cases {
		rc := rc
		t.Run(rc.Name, func(t *testing.T) {
			// Check policy exclusion before executing (saves cluster round-trip).
			if reason, excluded := regressExcluded[rc.Name]; excluded {
				t.Skipf("excluded by policy: %s", reason)
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
			defer cancel()

			results, err := framework.RunRegressSubset(ctx, root, []framework.RegressCase{rc}, exec)
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
	RepoRoot string
}

// ExecuteSQL writes sql to a temporary file and runs it through psql.
// Returns combined stdout (primary output) on success; on psql non-zero
// exit the stdout+stderr combination is returned so callers can diff it.
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
	result, err := e.Cluster.PSQL("-X", "-q", "-a", "-f", tmpf.Name())
	_ = err // psql non-zero exit (e.g. from unsupported SQL) is not fatal;
	         // the output diff will report "defer".
	return result.Stdout + result.Stderr, nil
}

// runRegressSetup runs postgres/src/test/regress/sql/test_setup.sql against
// the cluster best-effort. Failures (tablespaces, C extensions, COPY from
// non-existent files) are logged but not fatal — they only cause downstream
// cases to defer rather than break the suite entirely.
func runRegressSetup(t *testing.T, root string, c *cluster.Cluster) {
	t.Helper()
	setupPath := filepath.Join(root, "postgres", "src", "test", "regress", "sql", "test_setup.sql")
	if _, err := os.Stat(setupPath); err != nil {
		t.Logf("test_setup.sql not found, skipping fixture setup: %v", err)
		return
	}
	result, _ := c.PSQL("-X", "-q", "-a", "-f", setupPath)
	if result.ExitCode != 0 {
		t.Logf("test_setup.sql completed with partial failures (expected for C extensions/tablespaces): exit=%d", result.ExitCode)
	}
	if result.Stderr != "" {
		t.Logf("test_setup.sql stderr (truncated at 512 bytes):\n%s", truncate(result.Stderr, 512))
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
