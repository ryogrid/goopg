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
	case "pg_lsn":
		return "3220"
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
	{oid: 1395, name: "abs", namespace: "11", lang: "12", retType: "20", argTypes: "20", src: "int8abs"},       // abs(int8)
	{oid: 1396, name: "abs", namespace: "11", lang: "12", retType: "21", argTypes: "21", src: "int2abs"},       // abs(int2)
	{oid: 1397, name: "abs", namespace: "11", lang: "12", retType: "23", argTypes: "23", src: "int4abs"},       // abs(int4)
	{oid: 1398, name: "abs", namespace: "11", lang: "12", retType: "700", argTypes: "700", src: "float4abs"},   // abs(float4)
	{oid: 1705, name: "abs", namespace: "11", lang: "12", retType: "701", argTypes: "701", src: "float8abs"},   // abs(float8)
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
//   - prolang: language OID (text "12" for internal, "14" for SQL, "13" for C).
//   - prorettype: return-type OID (text).
//   - proargtypes: space-separated arg-type OIDs (oidvector format).
//   - prosrc: function body source.
func registerPgProcView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_proc",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "oid"}},
			{Name: "proname", Type: catalog.Type{Name: "text"}},
			{Name: "pronamespace", Type: catalog.Type{Name: "oid"}},
			{Name: "prolang", Type: catalog.Type{Name: "text"}},
			{Name: "prorettype", Type: catalog.Type{Name: "oid"}},
			{Name: "proargtypes", Type: catalog.Type{Name: "oidvector"}},
			{Name: "prosrc", Type: catalog.Type{Name: "text"}},
			{Name: "provolatile", Type: catalog.Type{Name: "text"}},
			{Name: "prosecdef", Type: catalog.Type{Name: "bool"}},
			{Name: "proleakproof", Type: catalog.Type{Name: "bool"}},
			{Name: "proisstrict", Type: catalog.Type{Name: "bool"}},
			{Name: "prokind", Type: catalog.Type{Name: "text"}},
		},
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
				b.src,
				"v",    // provolatile: volatile
				"f",    // prosecdef
				"f",    // proleakproof
				"f",    // proisstrict
				"f",    // prokind: function
			})
		}
		// Append user-defined routines.
		for _, r := range cat.Routines().List() {
			// Convert arg type names to space-separated OID strings.
			argOIDs := make([]string, len(r.ArgTypes))
			for i, t := range r.ArgTypes {
				argOIDs[i] = typeNameToOIDStr(t.Name)
			}
			// pronamespace: 2200 for public schema, 11 for pg_catalog.
			ns := "2200"
			if strings.EqualFold(r.Schema, "pg_catalog") {
				ns = "11"
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
			rows = append(rows, []string{
				fmt.Sprintf("%d", r.OID),
				r.Name,
				ns,
				r.Language,
				typeNameToOIDStr(r.ReturnType.Name),
				strings.Join(argOIDs, " "),
				r.Body,
				volatile,
				secdef,
				leakproof,
				strict,
				prokind,
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
