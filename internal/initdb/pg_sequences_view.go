package initdb

import (
	"fmt"
	"sort"

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
			base := []string{
				seq.Schema,
				seq.Name,
				"", // sequenceowner — not tracked
				seq.DataType,
				fmt.Sprintf("%d", seq.Start),
				fmt.Sprintf("%d", seq.Min),
				fmt.Sprintf("%d", seq.Max),
				fmt.Sprintf("%d", seq.Increment),
				boolText(seq.Cycle),
				"1", // cache_size — not tracked, default 1
			}
			if seq.Called {
				// Append last_value; omitting it leaves last_value as NULL.
				base = append(base, fmt.Sprintf("%d", seq.LastValue))
			}
			rows[i] = base
		}
		return rows
	}
	return cat.RegisterVirtualTable(tbl)
}
