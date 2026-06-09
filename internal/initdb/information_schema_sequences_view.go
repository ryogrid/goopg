package initdb

import (
	"fmt"
	"sort"

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
	tbl.VirtualRows = func() [][]string {
		infos := executor.AllSequenceInfos()
		sort.Slice(infos, func(i, j int) bool {
			if infos[i].Schema != infos[j].Schema {
				return infos[i].Schema < infos[j].Schema
			}
			return infos[i].Name < infos[j].Name
		})
		rows := make([][]string, len(infos))
		for i, seq := range infos {
			dt, prec := seqDataTypePrecision(seq.DataType)
			cycleOpt := "NO"
			if seq.Cycle {
				cycleOpt = "YES"
			}
			rows[i] = []string{
				"postgres", // sequence_catalog = current database
				seq.Schema,
				seq.Name,
				dt,
				fmt.Sprintf("%d", prec),
				"2", // numeric_precision_radix — always binary
				"0", // numeric_scale — always 0 for integer types
				fmt.Sprintf("%d", seq.Start),
				fmt.Sprintf("%d", seq.Min),
				fmt.Sprintf("%d", seq.Max),
				fmt.Sprintf("%d", seq.Increment),
				cycleOpt,
			}
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}

// seqDataTypePrecision maps a sequence data type name to the canonical
// PostgreSQL type name and its numeric_precision value.
func seqDataTypePrecision(dt string) (typeName string, precision int) {
	switch dt {
	case "smallint", "int2":
		return "smallint", 16
	case "integer", "int4", "int":
		return "integer", 32
	default: // bigint / int8 (default)
		return "bigint", 64
	}
}
