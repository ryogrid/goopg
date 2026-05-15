package main

import (
	"encoding/csv"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
)

// excludedCases maps test name → human-readable exclusion rationale.
// M0097-0002: ~102 tests reclassified as excluded by policy.
// Categories and criteria documented in .ralph/fix_plan.md M0097-0002.
var excludedCases = map[string]string{
	// ── Geometric types ─────────────────────────────────────────────────
	"box":      "Geometric type; out of scope for goopg v0.",
	"circle":   "Geometric type; out of scope for goopg v0.",
	"geometry": "Geometric type; out of scope for goopg v0.",
	"line":     "Geometric type; out of scope for goopg v0.",
	"lseg":     "Geometric type; out of scope for goopg v0.",
	"path":     "Geometric type; out of scope for goopg v0.",
	"point":    "Geometric type; out of scope for goopg v0.",
	"polygon":  "Geometric type; out of scope for goopg v0.",

	// ── Full-text search ─────────────────────────────────────────────────
	"tsdicts": "Full-text search; out of scope for goopg v0.",
	"tsearch": "Full-text search; out of scope for goopg v0.",
	"tsrf":    "Full-text search set-returning functions; out of scope for goopg v0.",
	"tstypes": "Full-text search types; out of scope for goopg v0.",

	// ── Advanced AM / exotic index ────────────────────────────────────────
	"amutils":             "Advanced access-method utilities; out of scope for goopg v0.",
	"brin":                "BRIN index type; out of scope for goopg v0.",
	"brin_bloom":          "BRIN bloom index; out of scope for goopg v0.",
	"brin_multi":          "BRIN multi-range index; out of scope for goopg v0.",
	"create_am":           "Custom access-method DDL; out of scope for goopg v0.",
	"create_index_spgist": "SP-GiST index DDL; out of scope for goopg v0.",
	"gin":                 "GIN index type; out of scope for goopg v0.",
	"gist":                "GiST index type; out of scope for goopg v0.",
	"spgist":              "SP-GiST index type; out of scope for goopg v0.",

	// ── Collation / encoding ──────────────────────────────────────────────
	"collate":                 "Collation semantics; out of scope for goopg v0.",
	"collate.icu.utf8":        "ICU collation; out of scope for goopg v0.",
	"collate.linux.utf8":      "Linux UTF-8 collation; out of scope for goopg v0.",
	"collate.utf8":            "UTF-8 collation; out of scope for goopg v0.",
	"collate.windows.win1252": "Windows collation; out of scope for goopg v0.",
	"copyencoding":            "COPY encoding options; out of scope for goopg v0.",
	"encoding":                "Multi-byte encoding; out of scope for goopg v0.",
	"euc_kr":                  "EUC-KR encoding; out of scope for goopg v0.",
	"unicode":                 "Unicode normalization; out of scope for goopg v0.",

	// ── External / infra features ─────────────────────────────────────────
	"async":          "LISTEN/NOTIFY async messaging; out of scope for goopg v0.",
	"compression":    "TOAST compression options; out of scope for goopg v0.",
	"foreign_data":   "Foreign Data Wrappers; out of scope for goopg v0.",
	"indirect_toast": "Indirect TOAST; out of scope for goopg v0.",
	"largeobject":    "Large objects (lo_*); out of scope for goopg v0.",
	"maintain_every": "Maintenance / autovacuum scheduling; out of scope for goopg v0.",
	"numa":           "NUMA-aware memory; out of scope for goopg v0.",
	"object_address": "pg_get_object_address; out of scope for goopg v0.",
	"tablesample":    "TABLESAMPLE clause; out of scope for goopg v0.",
	"tablespace":     "Tablespace DDL; not implemented in goopg v0.",

	// ── XML / advanced JSON ───────────────────────────────────────────────
	"json_encoding":      "JSON encoding edge cases; out of scope for goopg v0.",
	"jsonpath_encoding":  "JSONPath encoding; out of scope for goopg v0.",
	"sqljson":            "SQL/JSON features; out of scope for goopg v0.",
	"sqljson_jsontable":  "JSON_TABLE; out of scope for goopg v0.",
	"sqljson_queryfuncs": "SQL/JSON query functions; out of scope for goopg v0.",
	"xml":                "XML type and functions; out of scope for goopg v0.",
	"xmlmap":             "XML mapping; out of scope for goopg v0.",

	// ── Security & roles ─────────────────────────────────────────────────
	"create_role":    "Role DDL (authz); out of scope for goopg v0.",
	"init_privs":     "Initial privileges; out of scope for goopg v0.",
	"password":       "Password authentication; out of scope for goopg v0.",
	"privileges":     "GRANT / REVOKE; out of scope for goopg v0.",
	"roleattributes": "Role attributes; out of scope for goopg v0.",
	"rowsecurity":    "Row-level security; out of scope for goopg v0.",
	"security_label": "Security labels; out of scope for goopg v0.",

	// ── Parallel ─────────────────────────────────────────────────────────
	"select_parallel": "Parallel query execution; out of scope for goopg v0.",
	"vacuum_parallel": "Parallel VACUUM; out of scope for goopg v0.",
	"write_parallel":  "Parallel write; out of scope for goopg v0.",

	// ── Event triggers ───────────────────────────────────────────────────
	"event_trigger":       "Event triggers; out of scope for goopg v0.",
	"event_trigger_login": "Login event triggers; out of scope for goopg v0.",

	// ── psql client ──────────────────────────────────────────────────────
	"psql":          "psql client meta-commands; out of scope for goopg v0.",
	"psql_crosstab": "psql crosstab; out of scope for goopg v0.",
	"psql_pipeline": "psql pipelining; out of scope for goopg v0.",

	// ── Network types ────────────────────────────────────────────────────
	"inet":     "Network address type (inet/cidr); out of scope for goopg v0.",
	"macaddr":  "MAC address type; out of scope for goopg v0.",
	"macaddr8": "MAC address 8-byte type; out of scope for goopg v0.",

	// ── Catalog sanity ───────────────────────────────────────────────────
	"misc_sanity":  "Catalog sanity checks (require full pg_catalog); excluded.",
	"oidjoins":     "OID referential integrity catalog check; excluded.",
	"opr_sanity":   "Operator catalog sanity; excluded.",
	"sanity_check": "General catalog sanity; excluded.",
	"type_sanity":  "Type catalog sanity; excluded.",

	// ── Complex AM / type extensions ─────────────────────────────────────
	"alter_generic":    "Generic ALTER (non-table DDL); out of scope for goopg v0.",
	"alter_operator":   "ALTER OPERATOR; out of scope for goopg v0.",
	"create_aggregate": "CREATE AGGREGATE; out of scope for goopg v0.",
	"create_cast":      "CREATE CAST; out of scope for goopg v0.",
	"create_misc":      "Miscellaneous CREATE forms (collation, conversion, etc.); excluded.",
	"create_operator":  "CREATE OPERATOR; out of scope for goopg v0.",
	"create_type":      "CREATE TYPE (composite/base/range/domain); out of scope for goopg v0.",
	"drop_operator":    "DROP OPERATOR; out of scope for goopg v0.",
	"polymorphism":     "Polymorphic functions; out of scope for goopg v0.",
	"regproc":          "regproc / regclass / regtype OID types; out of scope for goopg v0.",

	// ── Replication catalog ──────────────────────────────────────────────
	"publication":      "Logical replication publication catalog; deferred to M0094.",
	"replica_identity": "Replica identity DDL; deferred to M0094.",
	"subscription":     "Logical replication subscription catalog; deferred to M0094.",

	// ── C-language functions ─────────────────────────────────────────────
	"create_function_c": "CREATE FUNCTION LANGUAGE C (requires shared library); excluded.",

	// ── Misc out-of-scope ────────────────────────────────────────────────
	"bit":       "Bit-string type; out of scope for goopg v0.",
	"bitmapops": "Bitmap index operations; out of scope for goopg v0.",
	"combocid":  "Combo CID test (MVCC internals); out of scope for goopg v0.",

	"conversion":       "CONVERT encoding functions; out of scope for goopg v0.",
	"create_schema":    "CREATE SCHEMA; out of scope for goopg v0.",
	"database":         "CREATE/DROP DATABASE catalog; out of scope for goopg v0.",
	"dependency":       "Object dependency tracking (pg_depend); out of scope for goopg v0.",
	"hash_func":        "Hash function catalog; out of scope for goopg v0.",
	"infinite_recurse": "Stack-depth exceeded error; out of scope for goopg v0.",
	"memoize":          "Memoize node (parameterised plan caching); out of scope for goopg v0.",
	"money":            "Money type; out of scope for goopg v0.",
	"namespace":        "pg_namespace catalog; out of scope for goopg v0.",
	"predicate":        "Predicate locking / serializable; out of scope for goopg v0.",
	"reloptions":       "Table/index reloptions; out of scope for goopg v0.",
	"stats":            "Extended statistics (CREATE STATISTICS); out of scope for goopg v0.",
	"stats_ext":        "Extended statistics expressions; out of scope for goopg v0.",
	"stats_import":     "pg_restore statistics; out of scope for goopg v0.",
	"typed_table":      "Typed tables (OF type); out of scope for goopg v0.",
	"without_overlaps": "WITHOUT OVERLAPS temporal key; out of scope for goopg v0.",
}

type specMeta struct {
	status    string
	rationale string
}

// loadCSV parses the inventory CSV and returns a map of item_path → specMeta
// for rows where suite_id == "regress-sql".
func loadCSV(csvPath string) (map[string]specMeta, error) {
	f, err := os.Open(csvPath)
	if err != nil {
		return nil, fmt.Errorf("open csv %q: %w", csvPath, err)
	}
	defer f.Close()

	r := csv.NewReader(f)
	header, err := r.Read()
	if err != nil {
		return nil, fmt.Errorf("read csv header: %w", err)
	}

	colIndex := func(name string) int {
		for i, h := range header {
			if h == name {
				return i
			}
		}
		return -1
	}

	suiteIdx := colIndex("suite_id")
	itemPathIdx := colIndex("item_path")
	statusIdx := colIndex("status")
	rationaleIdx := colIndex("rationale")

	if itemPathIdx < 0 || statusIdx < 0 {
		return nil, fmt.Errorf("csv missing required columns (item_path, status)")
	}

	out := make(map[string]specMeta)
	for {
		rec, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read csv row: %w", err)
		}
		if suiteIdx >= 0 && suiteIdx < len(rec) && rec[suiteIdx] != "regress-sql" {
			continue
		}
		if itemPathIdx >= len(rec) {
			continue
		}
		meta := specMeta{status: rec[statusIdx]}
		if rationaleIdx >= 0 && rationaleIdx < len(rec) {
			meta.rationale = rec[rationaleIdx]
		}
		out[rec[itemPathIdx]] = meta
	}
	return out, nil
}

func main() {
	var (
		repoRoot = flag.String("repo-root", ".", "path to repository root")
		outPath  = flag.String("out", "docs/test-port/upstream-regress-coverage.md", "output markdown path")
		csvPath  = flag.String("csv", "docs/test-port/postgres-oracle-target-inventory.csv", "path to inventory CSV")
	)
	flag.Parse()

	root, err := filepath.Abs(*repoRoot)
	if err != nil {
		fail("resolve repo root", err)
	}
	cases, err := framework.DiscoverRegressCases(root)
	if err != nil {
		fail("discover regress cases", err)
	}

	inventory, err := loadCSV(*csvPath)
	if err != nil {
		fail("load inventory csv", err)
	}

	if err := os.MkdirAll(filepath.Dir(*outPath), 0o755); err != nil {
		fail("mkdir output dir", err)
	}
	f, err := os.Create(*outPath)
	if err != nil {
		fail("create output", err)
	}
	defer f.Close()

	excludedCount := 0
	passCount := 0
	for _, c := range cases {
		if meta, ok := inventory[c.SQLPath]; ok {
			switch meta.status {
			case "excluded":
				excludedCount++
			case "pass":
				passCount++
			}
		} else if _, ok := excludedCases[c.Name]; ok {
			excludedCount++
		}
	}

	fmt.Fprintln(f, "# Upstream pg_regress Coverage Classification")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "Generated by `go run ./cmd/gen-regress-coverage --repo-root . --out docs/test-port/upstream-regress-coverage.md`.")
	fmt.Fprintf(f, "Generated at: %s\n\n", time.Now().Format(time.RFC3339))
	fmt.Fprintf(f, "Total discovered SQL cases: **%d** (%d excluded, %d pass, %d not-tried/failed)\n\n",
		len(cases), excludedCount, passCount, len(cases)-excludedCount-passCount)
	fmt.Fprintln(f, "Status policy for this phase:")
	fmt.Fprintln(f, "- `not-tried`: case has not yet been executed against goopg.")
	fmt.Fprintln(f, "- `failed`: case has been run but output does not match expected.")
	fmt.Fprintln(f, "- `pass`: case output matches pg_regress expected file.")
	fmt.Fprintln(f, "- `excluded`: permanently out of scope for goopg v0 (see rationale).")
	fmt.Fprintln(f)
	fmt.Fprintln(f, "| name | sql_path | expected_path | status | rationale |")
	fmt.Fprintln(f, "| ---- | -------- | ------------- | ------ | --------- |")
	for _, c := range cases {
		status := "not-tried"
		rationale := "Migration target; execution parity not yet attempted."
		if meta, ok := inventory[c.SQLPath]; ok {
			if meta.status != "" {
				status = meta.status
			}
			if meta.rationale != "" {
				rationale = meta.rationale
			}
		} else if excl, ok := excludedCases[c.Name]; ok {
			status = "excluded"
			rationale = excl
		} else if strings.TrimSpace(c.ExpectedPath) == "" {
			rationale = "No matching expected file found; needs expectation mapping."
		}
		fmt.Fprintf(f, "| %s | `%s` | `%s` | %s | %s |\n", c.Name, c.SQLPath, c.ExpectedPath, status, rationale)
	}
}

func fail(where string, err error) {
	fmt.Fprintf(os.Stderr, "gen-regress-coverage: %s: %v\n", where, err)
	os.Exit(1)
}
