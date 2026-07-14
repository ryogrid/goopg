package initdb

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// registerInformationSchemaSequencesView installs the
// information_schema.sequences virtual view. Columns and values match the
// PostgreSQL information_schema.sequences view. M0097-0068.
func registerInformationSchemaSequencesView(cat *catalog.InMemory) error {
	tbl := &catalog.Table{
		Schema:  "information_schema",
		Name:    "sequences",
		Virtual: true,
		Columns: []catalog.Column{
			{Name: "sequence_catalog", Type: catalog.Type{Name: "text"}, Ordinal: 0},
			{Name: "sequence_schema", Type: catalog.Type{Name: "text"}, Ordinal: 1},
			{Name: "sequence_name", Type: catalog.Type{Name: "text"}, Ordinal: 2},
			{Name: "data_type", Type: catalog.Type{Name: "text"}, Ordinal: 3},
			{Name: "numeric_precision", Type: catalog.Type{Name: "int4"}, Ordinal: 4},
			{Name: "numeric_precision_radix", Type: catalog.Type{Name: "int4"}, Ordinal: 5},
			{Name: "numeric_scale", Type: catalog.Type{Name: "int4"}, Ordinal: 6},
			{Name: "start_value", Type: catalog.Type{Name: "text"}, Ordinal: 7},
			{Name: "minimum_value", Type: catalog.Type{Name: "text"}, Ordinal: 8},
			{Name: "maximum_value", Type: catalog.Type{Name: "text"}, Ordinal: 9},
			{Name: "increment", Type: catalog.Type{Name: "text"}, Ordinal: 10},
			{Name: "cycle_option", Type: catalog.Type{Name: "text"}, Ordinal: 11},
		},
	}
	// Fallback path only — see registerPgSequencesView's matching comment in
	// pg_sequences_view.go. internal/executor/operators.go's valuesOp.Open
	// resolves the connecting session's own dbOid directly (tbl.Name ==
	// "sequences" branch) instead of falling through to this closure.
	// M0122-0007 4e follow-up 35 (deferred by follow-up 34).
	tbl.VirtualRows = func() [][]string {
		return executor.InformationSchemaSequencesRows(catalog.DefaultDBOid)
	}
	return cat.RegisterVirtualTable(tbl)
}
