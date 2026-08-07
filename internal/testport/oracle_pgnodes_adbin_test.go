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
// colSQLTypmod packs the length qualifier of a typed column SQL into PG's
// atttypmod for ResolveForColumnTypmod, mirroring what the executor writer derives
// from catalog.Type.Name + Type.Args. Any non-length-qualified or unmodeled type
// returns -1 (no length qualifier).
func colSQLTypmod(colSQL string) int32 {
	s := strings.TrimSpace(colSQL)
	if len(s) == 0 {
		return -1
	}
	// Split type name from parenthesized args.
	paren := strings.IndexByte(s, '(')
	if paren < 0 || !strings.HasSuffix(s, ")") {
		return -1
	}
	typeName := s[:paren]
	inner := s[paren+1 : len(s)-1]
	parts := strings.Split(inner, ",")
	args := make([]int64, 0, len(parts))
	for _, p := range parts {
		n, err := strconv.ParseInt(strings.TrimSpace(p), 10, 64)
		if err != nil {
			return -1
		}
		args = append(args, n)
	}
	return pgnodes.ColumnTypmod(typeName, args)
}

// numericColSQLTypmod is retained for backward-compat readability in the test body.
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
	// Sub-slice 24: a BARE numeric column whose default carries a typmod is re-labelled
	// back to typmod -1 via an implicit RelabelType (coerce_type_typmod's no-op branch),
	// not a numeric() length coercion.
	{"relabel_bare_explicit_8_1", "numeric", pgnodes.OidNumeric, "5.5::numeric(8,1)"},
	{"relabel_bare_int4_explicit_8_1", "numeric", pgnodes.OidNumeric, "5::numeric(8,1)"},
	// Sub-slice 25: an EXPLICIT `(inner)::numeric` cast of a typmod'd numeric operand
	// collapses to an EXPLICIT RelabelType (relabelformat 1) stripping the typmod to -1
	// (coerce_type_typmod's no-op branch, COERCE_EXPLICIT_CAST). pg_get_expr renders the
	// visible `::numeric` syntax, unlike the implicit relabelformat-2 form above.
	{"explicit_relabel_bare_8_1", "numeric", pgnodes.OidNumeric, "(5.5::numeric(8,1))::numeric"},
	{"explicit_relabel_bare_int4_8_1", "numeric", pgnodes.OidNumeric, "(5::numeric(8,1))::numeric"},
	// Sub-slice 26: a `date` column DEFAULT literal is folded to a by-value DateADT
	// Const (int32 days-since-2000) at parse time — date_in is TimeZone-independent,
	// so a plain ISO date literal always folds; a pre-2000 date sign-extends.
	{"date_post_2000", "date", pgnodes.OidDate, "'2024-03-15'"},
	{"date_epoch", "date", pgnodes.OidDate, "'2000-01-01'"},
	{"date_pre_epoch", "date", pgnodes.OidDate, "'1999-12-31'"},
	// Sub-slice 27: an explicit `::date` / `::timestamptz` cast of a bare string
	// literal folds to the SAME by-value Const at parse time (coerce_type →
	// stringTypeToConst); the `::type` supplies the target type but adds no cast
	// node, so the adbin is byte-identical to the bare-literal column-context fold.
	{"date_cast", "date", pgnodes.OidDate, "'2024-03-15'::date"},
	{"timestamptz_cast", "timestamptz", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00+00'::timestamptz"},
	// Sub-slice 28: an unknown-type STRING literal coerced to bool/int2/int4/int8 —
	// by an explicit `::type` cast or a typed column context — folds at parse time to
	// a by-value Const via the type input function (int4in/int8in/int2in/boolin), with
	// NO cast node, byte-identical to the analogous integer/bool datum. A bare integer
	// literal is int4-typed and would instead carry an int4→int2 cast FuncExpr, so only
	// the string-literal form folds (covered in string_cast_test.go).
	{"str_cast_int4", "int", pgnodes.OidInt4, "'123'::int4"},
	{"str_cast_int4_neg", "int", pgnodes.OidInt4, "'-5'::int4"},
	{"str_cast_int8", "int8", pgnodes.OidInt8, "'123'::int8"},
	{"str_cast_int2", "int2", pgnodes.OidInt2, "'12'::int2"},
	{"str_cast_bool_true", "bool", pgnodes.OidBool, "'t'::bool"},
	{"str_cast_bool_false", "bool", pgnodes.OidBool, "'false'::bool"},
	{"str_col_int4", "int", pgnodes.OidInt4, "'123'"},
	{"str_col_bool_yes", "bool", pgnodes.OidBool, "'yes'"},
	// Sub-slice 29: an unknown-type STRING literal coerced to text/numeric folds at
	// parse time via the input function (textin / numeric_in) to a by-value Const with
	// NO cast node — byte-identical to the same literal in that type's column context.
	// text is a verbatim byte copy (no trimming); numeric_in preserves the display
	// scale, so `'5.50'` keeps dscale 2 and a leading sign folds into the value. This
	// closes the explicit-`::text`/`::numeric`-cast asymmetry (previously SQL text) and
	// the bare numeric-column string DEFAULT.
	{"str_cast_text", "text", pgnodes.OidText, "'foo'::text"},
	{"str_cast_numeric", "numeric", pgnodes.OidNumeric, "'5.5'::numeric"},
	{"str_cast_numeric_scale", "numeric", pgnodes.OidNumeric, "'5.50'::numeric"},
	{"str_cast_numeric_neg", "numeric", pgnodes.OidNumeric, "'-2.5'::numeric"},
	{"str_col_numeric", "numeric", pgnodes.OidNumeric, "'5.5'"},
	// Sub-slice 29b: the NaN / ±Infinity specials fold to a digitless NUMERIC_SPECIAL
	// varlena (make_result for const_nan / const_pinf / const_ninf) — byte-identical
	// to the same string in a numeric column context.
	{"str_cast_numeric_nan", "numeric", pgnodes.OidNumeric, "'NaN'::numeric"},
	{"str_cast_numeric_inf", "numeric", pgnodes.OidNumeric, "'Infinity'::numeric"},
	{"str_cast_numeric_neg_inf", "numeric", pgnodes.OidNumeric, "'-Infinity'::numeric"},
	// Sub-slice 29c: an unknown-type STRING literal coerced to oid / float4 / float8 —
	// by an explicit `::type` cast or a typed column context — folds at parse time to a
	// by-value Const via the type input function (oidin / float4in / float8in), with NO
	// cast node. oid zero-extends into the datum word; float bits reinterpret as
	// int32/int64 (float4 sign-extends). Both PG's strtod/strtof and Go's ParseFloat are
	// correctly rounded, so the folded bits are identical.
	{"str_cast_oid", "oid", pgnodes.OidOid, "'5'::oid"},
	{"str_col_oid", "oid", pgnodes.OidOid, "'42'"},
	{"str_cast_float8", "float8", pgnodes.OidFloat8, "'5'::float8"},
	{"str_cast_float8_decimal", "float8", pgnodes.OidFloat8, "'5.5'::float8"},
	{"str_cast_float8_neg", "float8", pgnodes.OidFloat8, "'-2.5'::float8"},
	{"str_cast_float8_sci", "float8", pgnodes.OidFloat8, "'1.5e10'::float8"},
	{"str_col_float8", "float8", pgnodes.OidFloat8, "'5.5'"},
	{"str_cast_float4", "float4", pgnodes.OidFloat4, "'5'::float4"},
	{"str_cast_float4_decimal", "float4", pgnodes.OidFloat4, "'5.5'::float4"},
	{"str_cast_float4_neg", "float4", pgnodes.OidFloat4, "'-2.5'::float4"},
	// Sub-slice 34: a string literal in a varchar(N)/bpchar(N) column context
	// folds to a varchar/bpchar Const (consttypmod -1), then coerce_type_typmod
	// wraps it in an IMPLICIT varchar/bpchar(varchar/bpchar,int4,bool) FuncExpr
	// (funcformat 2) carrying the packed column typmod (maxlen + VARHDRSZ).
	{"varchar10_hello", "varchar(10)", pgnodes.OidVarchar, "'hello'"},
	{"varchar20_world", "varchar(20)", pgnodes.OidVarchar, "'world'"},
	{"varchar5_empty", "varchar(5)", pgnodes.OidVarchar, "''"},
	{"bpchar5_abc", "char(5)", pgnodes.OidBpchar, "'abc'"},
	{"bpchar10_xyz", "character(10)", pgnodes.OidBpchar, "'xyz'"},
	{"bpchar3_empty", "bpchar(3)", pgnodes.OidBpchar, "''"},
	// Sub-slice 34: a string literal in a timestamp(N) column context
	// folds to a timestamp Const (consttypmod -1), then coerce_type_typmod
	// wraps it in an IMPLICIT timestamp(timestamp,int4) FuncExpr (funcid 1961).
	{"ts0_2024_01_15", "timestamp(0)", pgnodes.OidTimestamp, "'2024-01-15 10:30:00'"},
	{"ts3_2024_01_15_123456", "timestamp(3)", pgnodes.OidTimestamp, "'2024-01-15 10:30:00.123456'"},
	{"ts6_2024_01_15_123456", "timestamp(6)", pgnodes.OidTimestamp, "'2024-01-15 10:30:00.123456'"},
	{"ts0_truncate", "timestamp(0)", pgnodes.OidTimestamp, "'2024-01-15 10:30:00.123456'"},
	{"ts0_epoch", "timestamp(0)", pgnodes.OidTimestamp, "'epoch'"},
	// Sub-slice 36: a string literal in a bit(N) column context folds to
	// a bit Const (consttypmod -1), then coerce_type_typmod wraps it in an
	// IMPLICIT bit(bit,int4,bool) FuncExpr (funcid 1685, funcformat 2).
	{"bit4_1010", "bit(4)", pgnodes.OidBit, "'1010'"},
	{"bit8_11110000", "bit(8)", pgnodes.OidBit, "'11110000'"},
	{"bit1_single", "bit(1)", pgnodes.OidBit, "'1'"},
	// Sub-slice 36: varbit(N) length coercion (funcid 1687, funcformat 2).
	{"varbit6_111000", "bit varying(6)", pgnodes.OidVarBit, "'111000'"},
	{"varbit8_10101010", "varbit(8)", pgnodes.OidVarBit, "'10101010'"},
	// varbit WITHOUT a length qualifier stores a bare Const.
	{"varbit_bare_10101", "bit varying", pgnodes.OidVarBit, "'10101'"},
	// Sub-slice 37: timestamptz(N) length coercion (funcid 1967, funcformat 2).
	// PG wraps the timestamptz Const in an IMPLICIT timestamptz(timestamptz,int4)
	// FuncExpr when the column has a precision qualifier (0-6).
	{"tstz3_2024_frac", "timestamptz(3)", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00.123456+00'"},
	{"tstz6_full", "timestamptz(6)", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00.123456+00'"},
	{"tstz0_truncate", "timestamptz(0)", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00.123456+00'"},
	{"tstz0_epoch", "timestamptz(0)", pgnodes.OidTimestamptz, "'epoch'"},
	// timestamptz WITHOUT a precision qualifier stores a bare Const.
	{"tstz_bare", "timestamptz", pgnodes.OidTimestamptz, "'2024-01-15 10:30:00+00'"},

	// Sub-slice 38: time(N) length coercion (funcid 1968, funcformat 2).
	// PG wraps the time Const in an IMPLICIT time(time,int4) FuncExpr when
	// the column has a precision qualifier (0-6).
	{"t0_midnight", "time(0)", pgnodes.OidTime, "'00:00:00'"},
	{"t3_frac", "time(3)", pgnodes.OidTime, "'10:30:00.123456'"},
	{"t6_full", "time(6)", pgnodes.OidTime, "'10:30:00.123456'"},
	{"t0_trunc", "time(0)", pgnodes.OidTime, "'10:30:00.123456'"},
	// time WITHOUT a precision qualifier stores a bare Const.
	{"t_bare", "time", pgnodes.OidTime, "'10:30:00'"},

	// Sub-slice 39: timetz(N) length coercion (funcid 1969, funcformat 2).
	// PG wraps the timetz Const in an IMPLICIT timetz(timetz,int4) FuncExpr when
	// the column has a precision qualifier (0-6).
	{"tz0_midnight_utc", "timetz(0)", pgnodes.OidTimeTZ, "'00:00:00+00:00'"},
	{"tz0_pos_offset", "timetz(0)", pgnodes.OidTimeTZ, "'10:30:00+05:30'"},
	{"tz3_frac", "timetz(3)", pgnodes.OidTimeTZ, "'10:30:00.123+05:30'"},
	{"tz6_full", "timetz(6)", pgnodes.OidTimeTZ, "'10:30:00.123456+05:30'"},
	{"tz0_trunc", "timetz(0)", pgnodes.OidTimeTZ, "'10:30:00.123456+05:30'"},
	{"tz0_zulu", "timetz(0)", pgnodes.OidTimeTZ, "'10:30:00Z'"},
	{"tz3_neg_offset", "timetz(3)", pgnodes.OidTimeTZ, "'10:30:00.100-03:00'"},
	// timetz WITHOUT a precision qualifier stores a bare Const.
	{"tz_bare", "timetz", pgnodes.OidTimeTZ, "'10:30:00+05:30'"},

	// Sub-slice 40: broader date input forms — infinity, BC years.
	{"date_infinity", "date", pgnodes.OidDate, "'infinity'"},
	{"date_neg_infinity", "date", pgnodes.OidDate, "'-infinity'"},
	{"date_0001_01_01_bc", "date", pgnodes.OidDate, "'0001-01-01 BC'"},
	{"date_0044_03_15_bc", "date", pgnodes.OidDate, "'0044-03-15 BC'"},
	{"ts_infinity", "timestamp", pgnodes.OidTimestamp, "'infinity'"},
	{"ts_neg_infinity", "timestamp", pgnodes.OidTimestamp, "'-infinity'"},
	{"ts_0001_01_01_bc", "timestamp", pgnodes.OidTimestamp, "'0001-01-01 00:00:00 BC'"},
	{"ts_0044_03_15_bc", "timestamp", pgnodes.OidTimestamp, "'0044-03-15 00:00:00 BC'"},
	{"tstz_infinity", "timestamptz", pgnodes.OidTimestamptz, "'infinity'"},
	{"tstz_neg_infinity", "timestamptz", pgnodes.OidTimestamptz, "'-infinity'"},
	{"tstz_0001_01_01_bc", "timestamptz", pgnodes.OidTimestamptz, "'0001-01-01 00:00:00+00 BC'"},
	{"tstz_0044_03_15_bc", "timestamptz", pgnodes.OidTimestamptz, "'0044-03-15 00:00:00+00 BC'"},
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
			node, ok := pgnodes.ResolveForColumnTypmod(expr, tc.colOid, colSQLTypmod(tc.colSQL))
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
