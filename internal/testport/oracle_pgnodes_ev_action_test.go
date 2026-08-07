package testport

// M0123-S4 — byte-diff oracle gate for the canonical pg_node_tree serializer,
// VIEW ev_action path (the query-tree analogue of oracle_pgnodes_adbin_test.go).
//
// The per-view golden tests in internal/pgnodes (resolver_query_test.go,
// view_bool_null_test.go, …) each hard-code a `golden` ev_action string a
// developer captured by hand from a live PostgreSQL 18.3 server. Those are fast
// unit gates but carry the same two gaps the adbin oracle closes:
//
//  1. Transcription risk — a hand-copied ev_action golden could silently drift
//     from what PG18 actually stores in pg_rewrite (a fat-fingered byte, a stale
//     capture). This test re-derives the oracle LIVE: it CREATE VIEWs the same
//     definition on a real PG18, reads back pg_rewrite.ev_action, and diffs it
//     against goopg's ResolveViewQuery→OutRuleAction for the identical SELECT.
//  2. Coverage drift — a future query-tree shape added to the resolver is
//     validated here the moment a case is appended, with no separate capture.
//
// The one piece the adbin path does not need is a RelationResolver over live PG
// catalog metadata: ResolveViewQuery must build each Var's varno/varattno/
// vartype/varcollid and the RTE relid from the base relation, so this test
// implements pgnodes.RelationResolver by querying the SAME live cluster's
// pg_class/pg_attribute. Because the relid and column OIDs are read live, goopg's
// emitted bytes and PG's ev_action reference the identical relid — no golden
// bakes in a fixed 16384, and the comparison is robust to catalog OID drift.
//
// Gated exactly like the adbin oracle: skipped in -short, when
// GOOPG_SKIP_PGNODES_ORACLE is set, and when the upstream PG binaries are absent.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// liveRelationResolver implements pgnodes.RelationResolver against a running
// pgcluster: it reads the base relation's real relid/relkind and its full
// user-column list (name, attnum, atttypid, atttypmod, attcollation) straight
// from the live catalog, so the Query goopg resolves references exactly the OIDs
// PG18 recorded in pg_rewrite.ev_action for the same view.
type liveRelationResolver struct {
	t *testing.T
	c *pgcluster.Cluster
}

func (r liveRelationResolver) LookupRelation(schema, name string) (*pgnodes.RelationInfo, bool) {
	rel := name
	if schema != "" {
		rel = schema + "." + name
	}
	quoted := "'" + strings.ReplaceAll(rel, "'", "''") + "'"

	meta := r.c.QueryScalar(r.t, fmt.Sprintf(
		"SELECT c.oid::text || '|' || c.relkind::text FROM pg_class c WHERE c.oid = %s::regclass", quoted))
	if meta == "" {
		return nil, false
	}
	mp := strings.SplitN(meta, "|", 2)
	if len(mp) != 2 || mp[1] == "" {
		r.t.Fatalf("liveRelationResolver: malformed pg_class meta %q for %s", meta, rel)
	}
	relOid, err := strconv.ParseUint(mp[0], 10, 32)
	if err != nil {
		r.t.Fatalf("liveRelationResolver: parse relid %q: %v", mp[0], err)
	}

	// One column per row, flattened to a single scalar via string_agg so the
	// existing QueryScalar helper suffices (no multi-row driver needed).
	colsStr := r.c.QueryScalar(r.t, fmt.Sprintf(
		"SELECT string_agg("+
			"a.attname::text || ':' || a.attnum::text || ':' || a.atttypid::text || ':' || a.atttypmod::text || ':' || a.attcollation::text, "+
			"',' ORDER BY a.attnum) "+
			"FROM pg_attribute a "+
			"WHERE a.attrelid = %s::regclass AND a.attnum > 0 AND NOT a.attisdropped", quoted))
	if colsStr == "" {
		r.t.Fatalf("liveRelationResolver: no user columns for %s", rel)
	}
	var cols []pgnodes.ColumnInfo
	for _, cs := range strings.Split(colsStr, ",") {
		f := strings.Split(cs, ":")
		if len(f) != 5 {
			r.t.Fatalf("liveRelationResolver: malformed column tuple %q for %s", cs, rel)
		}
		attno, _ := strconv.ParseInt(f[1], 10, 32)
		typOid, _ := strconv.ParseUint(f[2], 10, 32)
		typmod, _ := strconv.ParseInt(f[3], 10, 32)
		coll, _ := strconv.ParseUint(f[4], 10, 32)
		cols = append(cols, pgnodes.ColumnInfo{
			Name:      f[0],
			Attno:     int32(attno),
			TypeOID:   uint32(typOid),
			Typmod:    int32(typmod),
			Collation: uint32(coll),
		})
	}
	return &pgnodes.RelationInfo{
		Relid:   uint32(relOid),
		Relname: name,
		Relkind: mp[1][0],
		Columns: cols,
	}, true
}

// evActionOracleCase is one (view name, SELECT body) probe. Every case selects
// from the shared bench_log(client int, src text) base table so a single CREATE
// TABLE seeds the whole matrix; sel is the SELECT text CREATE VIEW wraps and that
// goopg's ResolveViewQuery consumes.
type evActionOracleCase struct {
	name string
	sel  string
}

// evActionOracleCases mirror the internal/pgnodes view goldens (resolver_query_
// test.go v/v2, view_bool_null_test.go v3–v13) so ResolveViewQuery is known to
// accept them — the value added is that `want` comes from a LIVE PG18 rather than
// a hand-copied constant. Together they exercise every canonical query-qual shape
// S4 emits: OpExpr, computed FuncExpr targets, BoolExpr AND/OR/NOT, NullTest,
// BooleanTest, CaseExpr (searched + simple), and DistinctExpr (+ its NullTest
// rewrite against a NULL operand).
var evActionOracleCases = []evActionOracleCase{
	{"v_opexpr_where", "SELECT client, src FROM bench_log WHERE client > 0"},
	{"v2_funcexpr_target", "SELECT upper(src) AS us FROM bench_log"},
	{"v3_and_nulltest", "SELECT client, src FROM bench_log WHERE src IS NOT NULL AND client > 0"},
	{"v4_or_not", "SELECT client, src FROM bench_log WHERE NOT (client > 0) OR src IS NULL"},
	{"v5_booleantest_istrue", "SELECT client, src FROM bench_log WHERE (client > 0) IS TRUE"},
	{"v6_booleantest_isnotfalse", "SELECT client, src FROM bench_log WHERE (client > 0) IS NOT FALSE"},
	{"v7_caseexpr_else", "SELECT client, src FROM bench_log WHERE CASE WHEN client > 0 THEN true ELSE false END"},
	{"v8_caseexpr_no_else", "SELECT client, src FROM bench_log WHERE CASE WHEN src IS NULL THEN false WHEN client > 0 THEN true END"},
	{"v9_distinctexpr", "SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM 5"},
	{"v10_distinctexpr_not", "SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM 5"},
	{"v11_distinct_from_null", "SELECT client, src FROM bench_log WHERE client IS DISTINCT FROM NULL"},
	{"v12_not_distinct_from_null", "SELECT client, src FROM bench_log WHERE client IS NOT DISTINCT FROM NULL"},
	{"v13_simple_case_var_operand", "SELECT client, src FROM bench_log WHERE CASE client WHEN 5 THEN true ELSE false END"},
	// v14–v18: operator-driven implicit coercion (M0123-S4 REMAINING #1).
	// A string literal on one side of a comparison coerces to the typed column's
	// type via foldStringLiteralConst (PG's unknown-type resolution at parse time).
	{"v14_timestamptz_coerce", "SELECT client, src FROM bench_log WHERE taken > '2024-01-01 00:00:00+00'"},
	{"v15_int2_coerce", "SELECT client, src FROM bench_log WHERE priority = '5'"},
	{"v16_numeric_coerce", "SELECT client, src FROM bench_log WHERE score > '3.14'"},
	{"v17_date_coerce", "SELECT client, src FROM bench_log WHERE registered = '2024-03-15'"},
	{"v18_timestamptz_coerce_left", "SELECT client, src FROM bench_log WHERE '2024-01-01 00:00:00+00' < taken"},
}

// TestOraclePgnodesEvActionBytesMatchPG is the M0123-S4 byte-diff oracle for the
// view path: for each canonical view it CREATE VIEWs the definition on a live
// PG18, reads back pg_rewrite.ev_action, and asserts goopg's ResolveViewQuery→
// OutRuleAction reproduces the exact bytes (`:location` normalized on PG's side;
// goopg always emits -1). A goopg ErrUnsupported degradation on a case PG stores
// canonically is itself a failure — the premise is that these views are canonical
// on both sides.
func TestOraclePgnodesEvActionBytesMatchPG(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_PGNODES_ORACLE") != "" {
		t.Skip("skipping pgnodes ev_action byte-diff oracle (short mode or GOOPG_SKIP_PGNODES_ORACLE set)")
	}
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	c, err := pgcluster.New("pgnodes-evaction-oracle", pgcluster.Options{RepoRoot: repo})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("pgcluster.Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	// Shared base relation for every view case.
	c.Exec(t, "DROP TABLE IF EXISTS bench_log CASCADE")
	c.Exec(t, "CREATE TABLE bench_log(client int, src text, taken timestamptz, priority int2, score numeric, registered date)")

	resolver := liveRelationResolver{t: t, c: c}

	for _, tc := range evActionOracleCases {
		t.Run(tc.name, func(t *testing.T) {
			view := "orv_" + tc.name
			c.Exec(t, fmt.Sprintf("DROP VIEW IF EXISTS %s", view))
			c.Exec(t, fmt.Sprintf("CREATE VIEW %s AS %s", view, tc.sel))

			pgEvAction := c.QueryScalar(t, fmt.Sprintf(
				"SELECT r.ev_action::text FROM pg_rewrite r "+
					"JOIN pg_class c ON c.oid = r.ev_class "+
					"WHERE c.relname = '%s'", view))
			if pgEvAction == "" {
				t.Fatalf("PG18 stored no pg_rewrite row for view %s (%s)", view, tc.sel)
			}
			want := normalizeOracleLocations(pgEvAction)

			sel := parseSelectForOracle(t, tc.sel)
			q, err := pgnodes.ResolveViewQuery(sel, resolver)
			if err != nil {
				t.Fatalf("goopg ResolveViewQuery(%q) degraded (%v), but PG18 stored a canonical ev_action:\n  PG18: %s",
					tc.sel, err, want)
			}
			got := pgnodes.OutRuleAction([]pgnodes.Node{q})
			if got != want {
				t.Fatalf("ev_action byte mismatch for view %s (%s):\n  goopg: %s\n  PG18:  %s", view, tc.sel, got, want)
			}
		})
	}
}

// parseSelectForOracle parses one SELECT statement into the *parser.SelectStmt
// ResolveViewQuery consumes, failing the test on any parse error or unexpected
// statement shape.
func parseSelectForOracle(t *testing.T, sqlText string) *parser.SelectStmt {
	t.Helper()
	stmts, err := parser.Parse(sqlText)
	if err != nil {
		t.Fatalf("goopg parser.Parse(%q): %v", sqlText, err)
	}
	if len(stmts) != 1 {
		t.Fatalf("goopg parser.Parse(%q): got %d stmts, want 1", sqlText, len(stmts))
	}
	sel, ok := stmts[0].(*parser.SelectStmt)
	if !ok {
		t.Fatalf("goopg parser.Parse(%q): stmt type %T, want *SelectStmt", sqlText, stmts[0])
	}
	return sel
}
