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
// oracle (ResolveViewQuery/pg_rewrite) is the sibling
// oracle_pgnodes_ev_action_test.go, which adds a RelationResolver shim over live
// PG catalog metadata.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/pgnodes"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// numericColSQLTypmod packs the length qualifier of a "numeric(p[,s])" column SQL
// type into PG's atttypmod for ResolveForColumnTypmod, mirroring what the executor
// writer derives from catalog.Type.Args. Any non-numeric or unqualified column type
// returns -1 (no length qualifier), matching the writer's bare-column path.
func numericColSQLTypmod(colSQL string) int32 {
	s := strings.TrimSpace(colSQL)
	if !strings.HasPrefix(s, "numeric(") || !strings.HasSuffix(s, ")") {
		return -1
	}
	inner := s[len("numeric(") : len(s)-1]
	parts := strings.Split(inner, ",")
	args := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return -1
		}
		args = append(args, n)
	}
	return pgnodes.NumericColumnTypmod(args)
}

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
	colSQL string // SQL column type, e.g. "int", "numeric", "numeric(10,2)"
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
	// Simple-form WHEN-value implicit coercion: a numeric operand with an int4
	// WHEN value — PG (no cross-type numeric=int4 operator) coerces the value via
	// int4_numeric and picks numeric_eq; the CaseTestExpr placeholder stays typed
	// numeric and un-coerced.
	{"case_simple_numeric_coerce", "numeric", pgnodes.OidNumeric, "CASE 5.0 WHEN 1 THEN 100.0 ELSE 200.0 END"},
	{"case_simple_numeric_coerce_multi", "numeric", pgnodes.OidNumeric, "CASE 3.5 WHEN 1 THEN 1.5 WHEN 2 THEN 2.5 ELSE 3.5 END"},
	// Simple-form WHEN-value NATIVE cross-type operator (sub-slice 18): an int8
	// operand with an int4 WHEN value resolves through the native int8=int4
	// operator (opno 416, int84eq) — PG's make_op leaves the value UN-coerced,
	// unlike the numeric operand above. The commutated form (int4 operand + int8
	// value) picks int4=int8 (opno 15, int48eq). Both exercise the two-phase
	// resolution: a native (operand,value) operator wins before coercion.
	{"case_simple_int8_operand_int4_when", "int8", pgnodes.OidInt8, "CASE 5000000000 WHEN 1 THEN 10000000000 ELSE 20000000000 END"},
	{"case_simple_int4_operand_int8_when", "int", pgnodes.OidInt4, "CASE 1 WHEN 5000000000 THEN 10 ELSE 20 END"},
	// Explicit integer `::type` casts (sub-slice 19): a COERCE_EXPLICIT_CAST
	// (funcformat 1) FuncExpr per pg_cast (int2(int4)=314, int8(int4)=481,
	// int4(int8)=480, int2(int8)=714), and a no-op cast to the operand's own type
	// (stored as the bare Const). Plus a simple-form CASE whose operand is an
	// explicit cast (int8(int4) placeholder) with explicit-cast results so the
	// casetype matches the column and no outer coercion wraps it.
	{"cast_int4_to_int2", "int2", pgnodes.OidInt2, "5::int2"},
	{"cast_int4_to_int8", "int8", pgnodes.OidInt8, "5::int8"},
	{"cast_int8_to_int4", "int", pgnodes.OidInt4, "9999999999::int4"},
	{"cast_int8_to_int2", "int2", pgnodes.OidInt2, "9999999999::int2"},
	{"cast_noop_int4", "int", pgnodes.OidInt4, "5::int4"},
	{"cast_noop_int8", "int8", pgnodes.OidInt8, "9999999999::int8"},
	{"case_simple_explicit_cast_operand", "int8", pgnodes.OidInt8, "CASE 5::int8 WHEN 1 THEN 10::int8 ELSE 20::int8 END"},
	// Explicit numeric↔integer `::type` casts (sub-slice 20): the funcformat-1
	// COERCE_EXPLICIT_CAST siblings of the implicit numeric-family coercions —
	// numeric_int4=1744 / numeric_int8=1779 / numeric_int2=1783 (numeric→int),
	// int4_numeric=1740 / int8_numeric=1781 (int→numeric). The operand is resolved
	// at its natural type first (decimal→numeric Const, integer→int4/int8 Const).
	{"cast_numeric_to_int4", "int", pgnodes.OidInt4, "5.5::int4"},
	{"cast_numeric_to_int8", "int8", pgnodes.OidInt8, "5.5::int8"},
	{"cast_numeric_to_int2", "int2", pgnodes.OidInt2, "5.5::int2"},
	{"cast_neg_numeric_to_int4", "int", pgnodes.OidInt4, "(-2.5)::int4"},
	{"cast_int4_to_numeric", "numeric", pgnodes.OidNumeric, "5::numeric"},
	{"cast_int8_to_numeric", "numeric", pgnodes.OidNumeric, "9999999999::numeric"},
	// Explicit float-family `::type` casts (sub-slice 21): the funcformat-1
	// COERCE_EXPLICIT_CAST arms across the binary-float boundary. int→float
	// (float4(int4)=318 / float8(int4)=316 / float4(int8)=652 / float8(int8)=482),
	// numeric→float (numeric_float4=1745 / numeric_float8=1746), and a nested
	// `(x::float8)::int4` reaching a float SOURCE arm (int4(float8)=317). 316/482/
	// 1746 are the funcformat-1 siblings of the implicit CASE →float8 coercion.
	{"cast_int4_to_float4", "float4", pgnodes.OidFloat4, "5::float4"},
	{"cast_int4_to_float8", "float8", pgnodes.OidFloat8, "5::float8"},
	{"cast_int8_to_float4", "float4", pgnodes.OidFloat4, "9999999999::float4"},
	{"cast_int8_to_float8", "float8", pgnodes.OidFloat8, "9999999999::float8"},
	{"cast_numeric_to_float4", "float4", pgnodes.OidFloat4, "5.5::float4"},
	{"cast_numeric_to_float8", "float8", pgnodes.OidFloat8, "5.5::float8"},
	{"cast_nested_float8_to_int4", "int", pgnodes.OidInt4, "(5.5::float8)::int4"},
	// Explicit typmod-qualified numeric cast `::numeric(p,s)` (sub-slice 22): PG's
	// coerce_to_target_type follows the base int→numeric cast with a length coercion
	// numeric(numeric,int4)=1703 (funcformat 1) carrying an int4 typmod Const =
	// ((p<<16)|s)+VARHDRSZ. The COLUMN typmod MUST match the cast (colSQL declares
	// numeric(p,s)) so PG stores the bare 1703 form with no RelabelType wrapper —
	// numericColSQLTypmod(colSQL) feeds ResolveForColumnTypmod the same typmod. An
	// integer operand wraps in int4_numeric (1740, funcformat 2); a decimal operand
	// is already a numeric Const.
	{"cast_int_to_numeric_10_2", "numeric(10,2)", pgnodes.OidNumeric, "5::numeric(10,2)"},
	{"cast_numeric_to_numeric_10_2", "numeric(10,2)", pgnodes.OidNumeric, "5.5::numeric(10,2)"},
	{"cast_int_to_numeric_10_0", "numeric(10,0)", pgnodes.OidNumeric, "5::numeric(10,0)"},
	// IMPLICIT numeric length coercion (sub-slice 23): when a numeric(p,s) column's
	// DEFAULT does NOT already carry the column typmod, coerce_type_typmod wraps the
	// stored default in an IMPLICIT numeric(numeric,int4)=1703 (funcformat 2) to the
	// column typmod — around a bare decimal Const, an int→numeric implicit cast, or a
	// mismatched explicit `::numeric(8,1)` cast. numericColSQLTypmod feeds the same
	// column typmod the executor writer derives, so goopg re-wraps identically.
	{"lencoerce_decimal_10_2", "numeric(10,2)", pgnodes.OidNumeric, "5.5"},
	{"lencoerce_int4_10_2", "numeric(10,2)", pgnodes.OidNumeric, "0"},
	{"lencoerce_int8_10_2", "numeric(10,2)", pgnodes.OidNumeric, "5000000000"},
	{"lencoerce_explicit_8_1_into_10_2", "numeric(10,2)", pgnodes.OidNumeric, "5.5::numeric(8,1)"},
	{"lencoerce_decimal_10_0", "numeric(10,0)", pgnodes.OidNumeric, "5.5"},
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
			node, ok := pgnodes.ResolveForColumnTypmod(expr, tc.colOid, numericColSQLTypmod(tc.colSQL))
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
