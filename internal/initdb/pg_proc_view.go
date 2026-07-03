// User-defined routine introspection: `pg_catalog.pg_proc` (M0015
// Stage A step 2). One row per registered routine plus selected built-in
// stubs needed for btree_index parity (abs variants, RI_FKey_* triggers).
// proargtypes is stored as space-separated OIDs (oidvector semantics),
// pronamespace as an OID integer (11=pg_catalog, 2200=public).
//
// See docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md.

package initdb

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// typeNameToOIDStr maps a Go/SQL type name to its PostgreSQL OID string.
func typeNameToOIDStr(typName string) string {
	switch strings.ToLower(typName) {
	case "bool", "boolean":
		return "16"
	case "bytea":
		return "17"
	case "char", "\"char\"":
		return "18"
	case "name":
		return "19"
	case "int8", "bigint":
		return "20"
	case "int2", "smallint":
		return "21"
	case "int4", "int", "integer":
		return "23"
	case "text":
		return "25"
	case "oid":
		return "26"
	case "float4", "real":
		return "700"
	case "float8", "float", "double precision":
		return "701"
	case "varchar", "character varying":
		return "1043"
	case "date":
		return "1082"
	case "time", "time without time zone":
		return "1083"
	case "timestamp", "timestamp without time zone":
		return "1114"
	case "timestamptz", "timestamp with time zone":
		return "1184"
	case "interval":
		return "1186"
	case "timetz", "time with time zone":
		return "1266"
	case "numeric", "decimal":
		return "1700"
	case "uuid":
		return "2950"
	case "void":
		return "2278"
	case "trigger":
		return "2279"
	case "internal":
		return "2281"
	case "index_am_handler":
		// Pseudo-type for the result of an index AM handler function
		// (CREATE ACCESS METHOD ... TYPE INDEX HANDLER ...). DU-002
		// (M0119-0004).
		return "325"
	case "table_am_handler":
		// Pseudo-type for the result of a table AM handler function
		// (CREATE ACCESS METHOD ... TYPE TABLE HANDLER ...). DU-002
		// (M0119-0004).
		return "269"
	case "record":
		// Pseudo-type representing any composite type. A SQL/plpgsql function
		// declared `RETURNS record` stores this name on prorettype; pg_dump's
		// dumpFunc renders it via format_type(2249) → "record". Without this
		// case prorettype resolved to 0 and the dump emitted `RETURNS -`.
		return "2249"
	case "pg_lsn":
		return "3220"
	// Array types (common ones)
	case "bool[]", "boolean[]":
		return "1000"
	case "bytea[]":
		return "1001"
	case "char[]", "\"char\"[]":
		return "1002"
	case "name[]":
		return "1003"
	case "int8[]", "bigint[]":
		return "1016"
	case "int2[]", "smallint[]":
		return "1005"
	case "int4[]", "int[]", "integer[]":
		return "1007"
	case "text[]":
		return "1009"
	case "oid[]":
		return "1028"
	case "float4[]", "real[]":
		return "1021"
	case "float8[]", "float[]", "double precision[]":
		return "1022"
	case "varchar[]", "character varying[]":
		return "1015"
	case "date[]":
		return "1182"
	case "timestamp[]", "timestamp without time zone[]":
		return "1115"
	case "timestamptz[]", "timestamp with time zone[]":
		return "1185"
	case "interval[]":
		return "1187"
	case "numeric[]", "decimal[]":
		return "1231"
	case "uuid[]":
		return "2951"
	case "record[]":
		// _record (array of records). Same pseudo-type family as record (2249);
		// included for symmetry with the array branches above so a function
		// declared `RETURNS record[]` resolves to the array OID rather than 0.
		return "2287"
	default:
		return "0"
	}
}

// langNameToOIDStr maps a procedural-language NAME to its pg_language OID string.
// goopg's pg_proc view stores prolang as oid (matching PG's catalog) so pg_dump's
// dumpFunc join `pg_language l ON l.oid = p.prolang` resolves. The 3 built-in
// languages (internal/c/sql) live in pg_language (see catalog.go); their OIDs come
// from postgres/src/include/catalog/pg_language.dat. A user routine recorded with a
// language not yet present in pg_language (e.g. plpgsql, which goopg does not install
// by default and which has a dynamic OID in PG) returns "0" (InvalidOid): a numeric
// value safe to materialise into the oid column, though dumpFunc's join will not
// resolve for such a function until the PL is added to pg_language (tracked
// separately). M0110-0001 (DU-002 slice 42).
func langNameToOIDStr(lang string) string {
	switch strings.ToLower(lang) {
	case "internal":
		return "12"
	case "c":
		return "13"
	case "sql":
		return "14"
	case "plpgsql":
		// OID 13627 — matches a stock PG 18.3 initdb and the plpgsql row added to
		// the pg_language virtual view (catalog.go). Lets dumpFunc's join resolve
		// lanname='plpgsql' so a LANGUAGE plpgsql function round-trips. DU-002 slice 163.
		return "13627"
	default:
		return "0"
	}
}

// builtinProcRow holds the fixed values for a built-in pg_proc row.
// proargtypes is a space-separated list of OID strings (oidvector format).
// retType is the return type OID string.
// namespace is the pronamespace OID string (11=pg_catalog).
type builtinProcRow struct {
	oid       uint32
	name      string
	namespace string
	lang      string // "12" = internal
	retType   string // OID string
	argTypes  string // space-separated OID strings, empty for zero-arg
	src       string
}

// builtinProcs is the subset of pg_proc built-in rows needed for parity.
// Includes abs() variants (required for btree_index RowCompare tests) and
// RI_FKey_* trigger functions (required for btree_index LIKE/ILIKE tests).
var builtinProcs = []builtinProcRow{
	// abs variants (sorted by proargtypes OID for determinism)
	{oid: 1395, name: "abs", namespace: "11", lang: "12", retType: "20", argTypes: "20", src: "int8abs"},         // abs(int8)
	{oid: 1396, name: "abs", namespace: "11", lang: "12", retType: "21", argTypes: "21", src: "int2abs"},         // abs(int2)
	{oid: 1397, name: "abs", namespace: "11", lang: "12", retType: "23", argTypes: "23", src: "int4abs"},         // abs(int4)
	{oid: 1398, name: "abs", namespace: "11", lang: "12", retType: "700", argTypes: "700", src: "float4abs"},     // abs(float4)
	{oid: 1705, name: "abs", namespace: "11", lang: "12", retType: "701", argTypes: "701", src: "float8abs"},     // abs(float8)
	{oid: 1704, name: "abs", namespace: "11", lang: "12", retType: "1700", argTypes: "1700", src: "numeric_abs"}, // abs(numeric)
	// RI_FKey trigger functions (no args, return trigger)
	{oid: 1654, name: "RI_FKey_cascade_del", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_cascade_del"},
	{oid: 1655, name: "RI_FKey_cascade_upd", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_cascade_upd"},
	{oid: 1656, name: "RI_FKey_noaction_del", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_noaction_del"},
	{oid: 1657, name: "RI_FKey_noaction_upd", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_noaction_upd"},
	{oid: 1658, name: "RI_FKey_restrict_del", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_restrict_del"},
	{oid: 1659, name: "RI_FKey_restrict_upd", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_restrict_upd"},
	{oid: 1660, name: "RI_FKey_setdefault_del", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_setdefault_del"},
	{oid: 1661, name: "RI_FKey_setdefault_upd", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_setdefault_upd"},
	{oid: 1662, name: "RI_FKey_setnull_del", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_setnull_del"},
	{oid: 1663, name: "RI_FKey_setnull_upd", namespace: "11", lang: "12", retType: "2279", argTypes: "", src: "RI_FKey_setnull_upd"},
}

// registerPgProcView installs `pg_catalog.pg_proc` backed by the
// supplied catalog's routine registry plus selected built-in stubs.
//
// Columns mirror the upstream pg_proc shape:
//
//   - oid: routine OID (text — pg_class etc. also use text OIDs).
//   - proname: routine name (unqualified).
//   - pronamespace: schema OID — 11 for pg_catalog, 2200 for public.
//   - prolang: language OID (oid "12" for internal, "14" for SQL, "13" for C).
//     Typed oid (NOT text) to match PG's pg_proc.prolang and the physical
//     pg_proc catalog: pg_dump's dumpFunc joins `pg_language l ON l.oid =
//     p.prolang`, and an oid=text comparison silently yields 0 rows.
//   - prorettype: return-type OID (text).
//   - proargtypes: space-separated arg-type OIDs (oidvector format).
//   - pronargs: number of input arguments (int2 = len(proargtypes)).
//   - proacl: access privileges (aclitem[]); always NULL — goopg tracks no
//     per-routine grants, so pg_dump treats every routine as default-privileged.
//   - proowner: owning role OID; always 10 (bootstrap superuser).
//   - prosrc: function body source.
//   - probin: on-disk binary path for C-language functions; always NULL
//     (goopg has no C functions). dumpFunc reads it to emit `AS '<probin>'`.
//   - proconfig: per-function GUC SET clauses (text[]); always NULL — goopg
//     tracks no per-function SET, so dumpFunc emits no `SET ...` lines.
//   - procost: planner's estimated per-row execution cost (float4). Mirrors
//     PG's CREATE FUNCTION default — 1 for internal/C-language functions,
//     100 for all others. goopg stores no explicit cost, so it derives this
//     from the routine's language.
//   - prorows: planner's estimated result-row count for set-returning
//     functions (float4). Mirrors PG's CREATE FUNCTION default — 1000 for
//     set-returning functions, 0 for everything else. dumpFunc reads it to
//     emit `ROWS <n>` for SRFs.
//   - protrftypes: OID array (oidvector) of argument types whose transforms
//     the function uses; always NULL — goopg supports no transforms, so
//     dumpFunc emits no `TRANSFORM FOR TYPE ...` clause.
//   - proparallel: parallel-safety marker (char) — 's' safe / 'r' restricted /
//     'u' unsafe. Defaults to 'u' (unsafe), matching PG's CREATE FUNCTION
//     default; a CREATE FUNCTION … PARALLEL SAFE/RESTRICTED records 's'/'r'.
//     dumpFunc emits `PARALLEL SAFE`/`PARALLEL RESTRICTED` whenever the marker
//     != 'u' (the default, which it suppresses). DU-002 slice 150.
//   - prosupport: the function's planner support function. Typed regproc (as in
//     PG's pg_proc), so InvalidOid renders as the text '-', NOT '0'. pg_dump's
//     getFuncs selects `prosupport` raw and dumpFunc emits `SUPPORT <val>`
//     whenever `strcmp(prosupport, "-") != 0` — an `oid`-typed '0' cell made it
//     emit the invalid `SUPPORT 0` clause (a restore error: SUPPORT wants a
//     function name). Always '-' — goopg has no planner support functions, so
//     dumpFunc emits no `SUPPORT ...` clause. DU-002 slice 148.
//
// pronargs/proacl/proowner were added for pg_dump's getFuncs SELECT (M0110-0001
// DU-002 slice 7), which projects `p.pronargs, …, p.proacl, …, p.proowner`.
func registerPgProcView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_proc",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "proname", Type: catalog.Type{Name: "text"}},
			{Name: "pronamespace", Type: catalog.Type{Name: "oid"}},
			{Name: "prolang", Type: catalog.Type{Name: "oid"}},
			{Name: "prorettype", Type: catalog.Type{Name: "oid"}},
			{Name: "proargtypes", Type: catalog.Type{Name: "oidvector"}},
			{Name: "pronargs", Type: catalog.Type{Name: "int2"}},
			{Name: "proacl", Type: catalog.Type{Name: "aclitem[]"}},
			{Name: "proowner", Type: catalog.Type{Name: "oid"}},
			{Name: "prosrc", Type: catalog.Type{Name: "text"}},
			{Name: "provolatile", Type: catalog.Type{Name: "text"}},
			{Name: "prosecdef", Type: catalog.Type{Name: "bool"}},
			{Name: "proleakproof", Type: catalog.Type{Name: "bool"}},
			{Name: "proisstrict", Type: catalog.Type{Name: "bool"}},
			{Name: "prokind", Type: catalog.Type{Name: "text"}},
			{Name: "proretset", Type: catalog.Type{Name: "bool"}},
			{Name: "probin", Type: catalog.Type{Name: "text"}},
			{Name: "proconfig", Type: catalog.Type{Name: "text[]"}},
			{Name: "procost", Type: catalog.Type{Name: "float4"}},
			{Name: "prorows", Type: catalog.Type{Name: "float4"}},
			{Name: "protrftypes", Type: catalog.Type{Name: "oidvector"}},
			{Name: "proparallel", Type: catalog.Type{Name: "char"}},
			{Name: "prosupport", Type: catalog.Type{Name: "regproc"}},
		},
		// The fixed pg_proc relation OID (1255). Without it the table's `tableoid`
		// system column resolves to 0, so pg_dump's getFuncs records each
		// function's catalogId.tableoid as 0 and cannot match the
		// pg_description.classoid=1255 rows that COMMENT ON FUNCTION writes — the
		// function comment is silently dropped from the dump. DU-002 slice 147.
		OID:     catalog.ProcedureRelationId,
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		// Start with built-in stubs.
		rows := make([][]string, 0, len(builtinProcs)+16)
		for _, b := range builtinProcs {
			rows = append(rows, []string{
				fmt.Sprintf("%d", b.oid),
				b.name,
				b.namespace,
				b.lang,
				b.retType,
				b.argTypes,
				fmt.Sprintf("%d", len(strings.Fields(b.argTypes))), // pronargs
				"",   // proacl: NULL (default privileges)
				"10", // proowner: bootstrap superuser
				b.src,
				"v", // provolatile: volatile
				"f", // prosecdef
				"f", // proleakproof
				"f", // proisstrict
				"f", // prokind: function
				"f", // proretset: built-in stubs (abs/RI_FKey) are not SRFs
				"",  // probin: NULL (internal funcs have no on-disk binary path)
				"",  // proconfig: NULL (no per-function GUC SET clauses)
				"1", // procost: internal-language default (DEFAULT_FUNCTION_COST)
				"0", // prorows: built-in stubs (abs/RI_FKey) are not SRFs
				"",  // protrftypes: NULL (goopg supports no transforms)
				"u", // proparallel: unsafe (PG CREATE FUNCTION default)
				"-", // prosupport: regproc InvalidOid renders '-' (no support function)
			})
		}
		// Append user-defined routines.
		for _, r := range cat.Routines().List() {
			// Convert arg type names to space-separated OID strings.
			argOIDs := make([]string, len(r.ArgTypes))
			for i, t := range r.ArgTypes {
				argOIDs[i] = typeNameToOIDStr(t.Name)
			}
			// pronamespace: use schema OID. public=2200, pg_catalog=11.
			// For user-created schemas, look up the OID from the catalog.
			ns := "2200" // default to public
			switch strings.ToLower(r.Schema) {
			case "pg_catalog":
				ns = "11"
			case "public", "":
				ns = "2200"
			default:
				// Look up user-created schema OID via catalog.
				if schOID := cat.SchemaOID(r.Schema); schOID != 0 {
					ns = fmt.Sprintf("%d", schOID)
				}
			}
			volatile := r.Volatile
			if volatile == "" {
				volatile = "v"
			}
			secdef := "f"
			if r.SecurityDefiner {
				secdef = "t"
			}
			leakproof := "f"
			if r.Leakproof {
				leakproof = "t"
			}
			strict := "f"
			if r.Strict {
				strict = "t"
			}
			prokind := "f" // function
			if r.IsProcedure {
				prokind = "p" // procedure
			}
			retset := "f"
			if r.ReturnsSet {
				retset = "t"
			}
			// procost: PG's CREATE FUNCTION default — 1 for internal/C-language
			// functions, 100 for all others (DEFAULT_FUNCTION_COST vs the
			// higher cost charged to interpreted languages).
			procost := "100"
			switch strings.ToLower(r.Language) {
			case "internal", "c":
				procost = "1"
			}
			// An explicit COST n on CREATE FUNCTION overrides the
			// language-derived default; previously the parser discarded it, so
			// dumpFunc never saw the non-default cost (silent divergence).
			if r.Cost != "" {
				procost = r.Cost
			}
			// prorows: PG's CREATE FUNCTION default — 1000 estimated result
			// rows for set-returning functions, 0 for everything else. An
			// explicit ROWS n (set-returning functions only) overrides it.
			prorows := "0"
			if r.ReturnsSet {
				prorows = "1000"
			}
			if r.Rows != "" {
				prorows = r.Rows
			}
			// proparallel: PG's CREATE FUNCTION default is 'u' (unsafe);
			// PARALLEL SAFE/RESTRICTED record 's'/'r'. dumpFunc emits
			// `PARALLEL SAFE`/`PARALLEL RESTRICTED` when the marker != 'u'.
			parallel := r.Parallel
			if parallel == "" {
				parallel = "u"
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", r.OID),
				r.Name,
				ns,
				langNameToOIDStr(r.Language), // prolang: oid (matches PG; resolves dumpFunc join)
				typeNameToOIDStr(r.ReturnType.Name),
				strings.Join(argOIDs, " "),
				fmt.Sprintf("%d", len(r.ArgTypes)), // pronargs
				cat.ProcACLText(r.OID),             // proacl: materialized from the GRANT store (NULL until first GRANT). DU-002 slice 345.
				"10",                               // proowner: bootstrap superuser
				r.Body,
				volatile,
				secdef,
				leakproof,
				strict,
				prokind,
				retset,
				"",       // probin: NULL (goopg has no C-language functions)
				"",       // proconfig: NULL (goopg tracks no per-function GUC SET)
				procost,  // procost: language-derived (1 internal/C, else 100)
				prorows,  // prorows: 1000 for SRFs, 0 otherwise
				"",       // protrftypes: NULL (goopg supports no transforms)
				parallel, // proparallel: 'u' unsafe (default), 's' safe, 'r' restricted
				"-",      // prosupport: regproc InvalidOid renders '-' (no support function)
			})
		}
		// Append the hand-curated built-in pg_proc table (catalog.BuiltinProcs),
		// after user-defined routines so existing rows[len(builtinProcs):] slices
		// in this package's tests still land on the same user-routine rows. This
		// lets pg_dump's own catalog queries — e.g. getFuncs()'s pg_transform/
		// pg_cast EXISTS-join, which findFuncByOid then resolves client-side into
		// a namespace-qualified function signature — see functions like
		// int4recv/prsd_lextype that a CREATE CAST/CONVERSION/TRANSFORM WITH
		// FUNCTION clause references. Field defaults (provolatile aside, which the
		// table carries explicitly) follow pg_proc.h's BKI_DEFAULT: proisstrict=t,
		// prokind=f, proretset=f, proparallel=s, procost=1, prorows=0.
		for _, b := range catalog.BuiltinProcs() {
			argOIDs := make([]string, len(b.ArgTypes))
			for i, t := range b.ArgTypes {
				argOIDs[i] = typeNameToOIDStr(t)
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", b.OID),
				b.Name,
				fmt.Sprintf("%d", b.Namespace),
				"12", // prolang: internal
				typeNameToOIDStr(b.RetType),
				strings.Join(argOIDs, " "),
				fmt.Sprintf("%d", len(b.ArgTypes)), // pronargs
				"",                                 // proacl: NULL (default privileges)
				"10",                               // proowner: bootstrap superuser
				b.Name,
				b.Volatile,
				"f", // prosecdef
				"f", // proleakproof
				"t", // proisstrict: BKI_DEFAULT(t)
				"f", // prokind: function
				"f", // proretset
				"",  // probin: NULL (internal funcs have no on-disk binary path)
				"",  // proconfig: NULL (no per-function GUC SET clauses)
				"1", // procost: internal-language default
				"0", // prorows
				"",  // protrftypes: NULL
				"s", // proparallel: BKI_DEFAULT(s)
				"-", // prosupport: regproc InvalidOid renders '-' (no support function)
			})
		}
		// Append user-defined aggregates (CREATE AGGREGATE) as prokind='a' pg_proc
		// rows, so pg_dump's getAggregates() (`pg_proc p WHERE p.prokind = 'a'`)
		// discovers them. The matching pg_aggregate row is a live heap write
		// (syncAggregateToCatalogHeap, executor/operators_ddl.go) rather than a
		// VirtualRows entry — pg_aggregate is heap-backed (161 BKI rows written at
		// initdb), unlike pg_proc which is entirely virtual. DU-002 slice 405.
		for _, agg := range cat.ListUserAggregates() {
			argOIDs := make([]string, len(agg.ArgTypes))
			for i, t := range agg.ArgTypes {
				argOIDs[i] = typeNameToOIDStr(t)
			}
			// prorettype: PG's DefineAggregate uses the finalfunc's return type
			// when one is given, else the state type (transtype).
			rettype := agg.SType
			if _, ft := executor.ResolveAggFuncOIDAndRetType(cat, agg.FinalFunc); ft != "" {
				rettype = ft
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", agg.OID),
				agg.Name,
				fmt.Sprintf("%d", agg.NamespaceOIDOrDefault()), // pronamespace: schema OID (DU-002 slice 405 resume point (a))
				"12",   // prolang: internal
				typeNameToOIDStr(rettype),
				strings.Join(argOIDs, " "),
				fmt.Sprintf("%d", len(agg.ArgTypes)),    // pronargs
				"",                                      // proacl: NULL (default privileges)
				fmt.Sprintf("%d", agg.OwnerOrDefault()), // proowner: ALTER AGGREGATE ... OWNER TO, else bootstrap superuser
				"aggregate_dummy",                       // prosrc: PG's real aggregate stub name
				"i",                                     // provolatile
				"f",                                     // prosecdef
				"f",                                     // proleakproof
				"f",                                     // proisstrict: NULL handling lives in the transfn, not the wrapper
				"a",                                     // prokind: aggregate
				"f",                                     // proretset
				"",                                      // probin: NULL
				"",                                      // proconfig: NULL
				"1",                                     // procost: internal-language default
				"0",                                     // prorows
				"",                                      // protrftypes: NULL
				"u",                                     // proparallel: PG CREATE AGGREGATE default (unsafe)
				"-",                                     // prosupport
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
