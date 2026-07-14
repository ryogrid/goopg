package initdb

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// registerPgSequencesView installs the pg_catalog.pg_sequences virtual view.
// Columns match the PostgreSQL pg_sequences system catalog view. M0097-0024.
func registerPgSequencesView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema:  "pg_catalog",
		Name:    "pg_sequences",
		Virtual: true,
		Columns: []catalog.Column{
			{Name: "schemaname", Type: catalog.Type{Name: "text"}, Ordinal: 0},
			{Name: "sequencename", Type: catalog.Type{Name: "text"}, Ordinal: 1},
			{Name: "sequenceowner", Type: catalog.Type{Name: "text"}, Ordinal: 2},
			{Name: "data_type", Type: catalog.Type{Name: "text"}, Ordinal: 3},
			{Name: "start_value", Type: catalog.Type{Name: "int8"}, Ordinal: 4},
			{Name: "min_value", Type: catalog.Type{Name: "int8"}, Ordinal: 5},
			{Name: "max_value", Type: catalog.Type{Name: "int8"}, Ordinal: 6},
			{Name: "increment_by", Type: catalog.Type{Name: "int8"}, Ordinal: 7},
			{Name: "cycle", Type: catalog.Type{Name: "bool"}, Ordinal: 8},
			{Name: "cache_size", Type: catalog.Type{Name: "int8"}, Ordinal: 9},
			{Name: "last_value", Type: catalog.Type{Name: "int8"}, Ordinal: 10},
		},
	}
	// Fallback path only: behaves exactly as before, always DefaultDBOid.
	// The connecting session's own dbOid is resolved directly by
	// internal/executor/operators.go's valuesOp.Open (tbl.Name ==
	// "pg_sequences" branch), which calls executor.PGSequencesRows with the
	// connection's own dbOid instead of falling through to this closure —
	// mirrors the pg_stat_slru/pg_stat_io direct-call pattern (no
	// catalog.InMemory indirection needed since sequence state already
	// lives in this package). M0122-0007 4e follow-up 35 (deferred by
	// follow-up 34).
	tbl.VirtualRows = func() [][]string {
		return executor.PGSequencesRows(catalog.DefaultDBOid)
	}
	return cat.RegisterVirtualTable(tbl)
}
