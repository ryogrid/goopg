// User-defined routine introspection: `pg_catalog.pg_proc` (M0015
// Stage A step 2). One row per registered routine. Empty when the
// catalog's routine registry holds none — callers can SELECT against
// the view in a fresh data dir without surfacing a missing-table
// error.
//
// See docs/design/0015-0002-pg-proc-catalog-and-routine-registry.md.

package initdb

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
)

// registerPgProcView installs `pg_catalog.pg_proc` backed by the
// supplied catalog's routine registry.
//
// Columns mirror the upstream pg_proc shape used by `\df` and
// pg_dump:
//
//   - oid: routine OID (text — pg_class etc. also use text OIDs).
//   - proname: routine name (unqualified).
//   - pronamespace: schema name.
//   - prolang: language name (lower-cased).
//   - prorettype: return-type name.
//   - proargtypes: comma-separated argument-type names. Empty for
//     zero-arg routines.
//   - prosrc: function body source — verbatim bytes between the
//     dollar-quote delimiters.
//
// Stage A step 2 doesn't yet emit upstream's `oidvector` /
// numerical-type-OID columns; once the type system grows numeric
// OIDs we can swap the type-name columns to match exactly. The
// goopg analyzer / `\df` text introspection works with the
// type-name surface today.
func registerPgProcView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema: "pg_catalog",
		Name:   "pg_proc",
		Columns: []catalog.Column{
			{Name: "oid", Type: catalog.Type{Name: "text"}},
			{Name: "proname", Type: catalog.Type{Name: "text"}},
			{Name: "pronamespace", Type: catalog.Type{Name: "text"}},
			{Name: "prolang", Type: catalog.Type{Name: "text"}},
			{Name: "prorettype", Type: catalog.Type{Name: "text"}},
			{Name: "proargtypes", Type: catalog.Type{Name: "text"}},
			{Name: "prosrc", Type: catalog.Type{Name: "text"}},
		},
		Virtual: true,
	}
	tbl.VirtualRows = func() [][]string {
		routines := cat.Routines().List()
		rows := make([][]string, 0, len(routines))
		for _, r := range routines {
			argTypes := make([]string, len(r.ArgTypes))
			for i, t := range r.ArgTypes {
				argTypes[i] = t.Name
			}
			rows = append(rows, []string{
				fmt.Sprintf("%d", r.OID),
				r.Name,
				r.Schema,
				r.Language,
				r.ReturnType.Name,
				strings.Join(argTypes, ","),
				r.Body,
			})
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
