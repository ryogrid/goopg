package catalog

import "strings"

// BuiltinSRFColumns are the result columns of the built-in table functions
// planned as optimizer.BuiltinSRFScan, shared by the planner's binding and
// the analyzer's FROM-clause column list so the two cannot drift apart. A
// nil Names means one column named after the range alias (a SETOF scalar).
// Column names and types follow pg_proc (proargnames / proallargtypes OUT
// entries). M0149-0016c.
type BuiltinSRFColumns struct {
	Names []string
	Types []string
}

var builtinSRFColumns = map[string]BuiltinSRFColumns{
	// genfile.c pg_ls_dir: SETOF text.
	"pg_ls_dir": {Types: []string{"text"}},
	// genfile.c pg_stat_file: record (size, access, modification, change,
	// creation, isdir).
	"pg_stat_file": {
		Names: []string{"size", "access", "modification", "change", "creation", "isdir"},
		Types: []string{"int8", "timestamptz", "timestamptz", "timestamptz", "timestamptz", "bool"},
	},
}

// LookupBuiltinSRFColumns returns name's result columns, if it is planned
// as a BuiltinSRFScan.
func LookupBuiltinSRFColumns(name string) (BuiltinSRFColumns, bool) {
	c, ok := builtinSRFColumns[strings.ToLower(name)]
	return c, ok
}

// Columns builds the range's columns for alias with the given column
// aliases (a shorter alias list renames a prefix, as in PG).
func (s BuiltinSRFColumns) Columns(alias string, colAliases []string) []Column {
	names := s.Names
	if names == nil {
		names = []string{alias}
	}
	cols := make([]Column, len(names))
	for i, n := range names {
		if i < len(colAliases) && colAliases[i] != "" {
			n = colAliases[i]
		}
		cols[i] = Column{Name: n, Type: Type{Name: s.Types[i]}, Ordinal: i}
	}
	return cols
}
