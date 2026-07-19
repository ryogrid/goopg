package testport

// M0123-S4 — byte-diff oracle gate for the canonical pg_node_tree serializer.
//
// The per-datum golden tests in internal/pgnodes (bool_null_test.go,
// numeric_test.go, case_test.go, …) each hard-code a `want` adbin string that a
// developer captured by hand from a live PostgreSQL 18.3 server. That makes them
// fast unit gates but leaves two gaps this test closes:
//
//  1. Transcription risk — a hand-copied golden could silently drift from what
//     PG18 actually emits (a fat-fingered byte, a stale capture after a PG
//     point-release). This test re-derives the oracle LIVE: it CREATE TABLEs the
//     same DEFAULT on a real PG18, reads back pg_attrdef.adbin, and diffs it
//     against goopg's ResolveForColumn→Out for the identical expression.
//  2. Coverage drift — a future datum/expression type added to the resolver is
//     automatically validated here the moment a case is appended, without a
//     separate hand-capture step.
//
// This is the S4 "byte-diff oracle" deliverable (fix_plan.md M0123-S4): goopg's
// emitted adbin `==` real-PG18's for the identical DDL, with `:location`
// normalized. It is heavy (spins up a real PG18 via initdb + pg_ctl) so it is
// gated exactly like the other heterogeneous E2E tests: skipped in `-short` and
// when GOOPG_SKIP_PGNODES_ORACLE is set, and skipped entirely when the upstream
// PG binaries are absent.
//
// Scope of THIS slice: column DEFAULTs (the ResolveForColumn/adbin path), which
// carries the bulk of S4's datum + scalar-expression work. The view ev_action
// oracle (ResolveViewQuery/pg_rewrite) needs a RelationResolver shim over live
// PG catalog metadata and is deferred (see .ralph/deferral_ledger.md).

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// oracleLocationRe matches every `:location <N>` token (N may be negative). PG18
// writes catalog pg_node_tree with location fields already normalized to -1
// (outNode's write_location_fields=false storage mode), so for pg_attrdef.adbin
// this replacement is a no-op belt — but it keeps the comparison correct if a
// future case (or the view ev_action path) surfaces a node PG stores with a real
// source offset. Normalizing PG's side (goopg's Out always emits -1) is enough.
var oracleLocationRe = regexp.MustCompile(`:location -?\d+`)

func normalizeOracleLocations(s string) string {
	return oracleLocationRe.ReplaceAllString(s, ":location -1")
}

// adbinOracleCase is one (column type, DEFAULT expression) probe. colOid is the
// pgnodes OID passed to ResolveForColumn — it must equal the OID of colSQL so the
// exact-type-match guard (and any implicit coercion PG applies at store time)
// lines up with what PG18 records in pg_attrdef.adbin.
type adbinOracleCase struct {
	name   string
	colSQL string // SQL column type, e.g. "int", "numeric", "timestamptz"
	colOid uint32 // matching pgnodes OID for ResolveForColumn
	def    string // DEFAULT expression text (parsed by BOTH PG and goopg)
}

// adbinOracleCases spans every datum + scalar-expression family the S4 resolver
// emits canonically. Each case is drawn from an existing internal/pgnodes golden
// test, so ResolveForColumn is known to accept it — the value added here is that
// the `want` string comes from a LIVE PG18 rather than a hand-copied constant.
var adbinOracleCases = []adbinOracleCase{
	// Bare Const leaves (int4/int8 by magnitude, folded negative, text, numeric).
	{"int4_const", "int", pgnodes.OidInt4, "5"},
	{"int4_neg", "int", pgnodes.OidInt4, "-1"},
	{"int8_const", "int8", pgnodes.OidInt8, "5000000000"},
	{"text_literal", "text", pgnodes.OidText, "'hi'"},
	{"numeric_decimal", "numeric", pgnodes.OidNumeric, "100.50"},
	{"numeric_sci", "numeric", pgnodes.OidNumeric, "1E-10"},
	{"numeric_neg", "numeric", pgnodes.OidNumeric, "-2.5"},
	// int4→numeric implicit cast FuncExpr (funcid 1740) stored, NOT const-folded.
	{"numeric_int_cast", "numeric", pgnodes.OidNumeric, "12345"},
	// FuncExpr (built-in, not folded).
	{"func_upper", "text", pgnodes.OidText, "upper('x')"},
	// timestamptz literal → Const (timestamptz_in at store time).
	{"timestamptz_lit", "timestamptz", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00+00'"},
	// BoolExpr / NullTest / OpExpr scalar nodes.
	{"bool_true", "bool", pgnodes.OidBool, "true"},
	{"is_null", "bool", pgnodes.OidBool, "1 IS NULL"},
	{"is_not_null", "bool", pgnodes.OidBool, "1 IS NOT NULL"},
	{"bool_and", "bool", pgnodes.OidBool, "true AND false"},
	{"bool_not", "bool", pgnodes.OidBool, "NOT true"},
	{"bool_and_flatten", "bool", pgnodes.OidBool, "(1 < 2) AND (3 > 2) AND (5 = 5)"},
	// BooleanTest (IS TRUE / IS UNKNOWN).
	{"is_true", "bool", pgnodes.OidBool, "true IS TRUE"},
	{"is_unknown", "bool", pgnodes.OidBool, "(1=1) IS UNKNOWN"},
	// DistinctExpr (IS [NOT] DISTINCT FROM) over int and text operands.
	{"distinct_int", "bool", pgnodes.OidBool, "1 IS DISTINCT FROM 2"},
	{"distinct_text", "bool", pgnodes.OidBool, "'x' IS DISTINCT FROM 'y'"},
	// CaseExpr: searched + simple form, same-type and cross-type (int→numeric,
	// int4→int8) coercion of the result arms.
	{"case_searched_int", "int", pgnodes.OidInt4, "CASE WHEN true THEN 1 ELSE 2 END"},
	{"case_simple_int", "int", pgnodes.OidInt4, "CASE 1 WHEN 1 THEN 10 ELSE 20 END"},
	{"case_bool", "bool", pgnodes.OidBool, "CASE WHEN (1<2) THEN true ELSE false END"},
	{"case_numeric_cast", "numeric", pgnodes.OidNumeric, "CASE WHEN true THEN 1 ELSE 2.5 END"},
	{"case_int8_widen", "int8", pgnodes.OidInt8, "CASE WHEN true THEN 1 ELSE 5000000000 END"},
}

// TestOraclePgnodesAdbinBytesMatchPG is the M0123-S4 byte-diff oracle: for each
// canonical DEFAULT case it stores the expression on a live PG18, reads back
// pg_attrdef.adbin, and asserts goopg's ResolveForColumn→Out reproduces the exact
// bytes (locations normalized). A goopg SQL-text fallback (ok==false) on a case
// PG stores canonically is itself a failure — the whole premise is that these
// cases are canonical on both sides.
func TestOraclePgnodesAdbinBytesMatchPG(t *testing.T) {
	if testing.Short() || os.Getenv("GOOPG_SKIP_PGNODES_ORACLE") != "" {
		t.Skip("skipping pgnodes adbin byte-diff oracle (short mode or GOOPG_SKIP_PGNODES_ORACLE set)")
	}
	repo := repoRoot(t)
	binDir := filepath.Join(repo, "postgres", "local_install", "bin")
	pgcluster.Available(t, binDir)

	c, err := pgcluster.New("pgnodes-adbin-oracle", pgcluster.Options{RepoRoot: repo})
	if err != nil {
		t.Fatalf("pgcluster.New: %v", err)
	}
	if err := c.Start(); err != nil {
		t.Fatalf("pgcluster.Start: %v", err)
	}
	t.Cleanup(func() { _ = c.Stop() })

	for _, tc := range adbinOracleCases {
		t.Run(tc.name, func(t *testing.T) {
			tbl := "orc_" + tc.name
			c.Exec(t, fmt.Sprintf("DROP TABLE IF EXISTS %s", tbl))
			// Wrap the expr in parens so multi-word forms (CASE …, a IS NULL)
			// parse as a single DEFAULT expression; the parens are syntactic and
			// never appear in the stored adbin.
			c.Exec(t, fmt.Sprintf("CREATE TABLE %s (v %s DEFAULT (%s))", tbl, tc.colSQL, tc.def))

			pgAdbin := c.QueryScalar(t, fmt.Sprintf(
				"SELECT ad.adbin::text FROM pg_attrdef ad "+
					"JOIN pg_class r ON r.oid = ad.adrelid "+
					"WHERE r.relname = '%s'", tbl))
			if pgAdbin == "" {
				t.Fatalf("PG18 stored no pg_attrdef row for %s DEFAULT (%s)", tc.colSQL, tc.def)
			}
			want := normalizeOracleLocations(pgAdbin)

			expr, err := parser.ParseExpr(tc.def)
			if err != nil {
				t.Fatalf("goopg parser.ParseExpr(%q): %v", tc.def, err)
			}
			node, ok := pgnodes.ResolveForColumn(expr, tc.colOid)
			if !ok {
				t.Fatalf("goopg ResolveForColumn(%q, oid=%d) degraded to SQL text, but PG18 stored a canonical adbin:\n  PG18: %s",
					tc.def, tc.colOid, want)
			}
			got := pgnodes.Out(node)
			if got != want {
				t.Fatalf("adbin byte mismatch for %s DEFAULT (%s):\n  goopg: %s\n  PG18:  %s", tc.colSQL, tc.def, got, want)
			}
		})
	}
}
