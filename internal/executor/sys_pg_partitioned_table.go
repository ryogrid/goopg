package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgPartitionedTableRelOID is pg_partitioned_table's OID (and its
// relfilenode in base/<db>/).
const pgPartitionedTableRelOID = 3350

// PGPartitionedTableColumnsPG18 mirrors FormData_pg_partitioned_table
// (partrelid, partstrat, partnatts, partdefid, partattrs, partclass,
// partcollation, partexprs). Exported for the initdb heap reload.
func PGPartitionedTableColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "partrelid", Type: catalog.Type{Name: "oid"}, Ordinal: 0},
		{Name: "partstrat", Type: catalog.Type{Name: "char"}, Ordinal: 1},
		{Name: "partnatts", Type: catalog.Type{Name: "int2"}, Ordinal: 2},
		{Name: "partdefid", Type: catalog.Type{Name: "oid"}, Ordinal: 3},
		{Name: "partattrs", Type: catalog.Type{Name: "int2vector"}, Ordinal: 4},
		{Name: "partclass", Type: catalog.Type{Name: "oidvector"}, Ordinal: 5},
		{Name: "partcollation", Type: catalog.Type{Name: "oidvector"}, Ordinal: 6},
		{Name: "partexprs", Type: catalog.Type{Name: "pg_node_tree"}, Ordinal: 7},
	}
}

// writePartitionedTableRow journals partitioned tbl's pg_partitioned_table
// row (StorePartitionKey, heap.c). partstrat / partnatts / partattrs are
// PG's (0 for an expression key); partclass / partcollation are written as
// zeros, and partexprs holds the key's PartitionKeyDef text — goopg stores
// pg_node_tree columns as text (relpartbound, prqual), and the reload
// re-parses it (ParsePartitionKeyDef). Before this the partition key was not
// persisted at all, and a partitioned table did not survive a restart.
// M0149-0061k.
func writePartitionedTableRow(ctx *Context, tbl *catalog.Table) error {
	natts := len(tbl.PartitionKey)
	if n := len(tbl.PartitionKeyExprs); n > natts {
		natts = n
	}
	attnums := make([]int16, natts)
	for i := 0; i < natts && i < len(tbl.PartitionKey); i++ {
		if i < len(tbl.PartitionKeyExprs) && tbl.PartitionKeyExprs[i] != nil {
			continue
		}
		for _, c := range tbl.Columns {
			if !c.Dropped && strings.EqualFold(c.Name, tbl.PartitionKey[i]) {
				attnums[i] = int16(c.Ordinal + 1)
				break
			}
		}
	}
	strat := "r"
	switch strings.ToUpper(tbl.PartitionMethod) {
	case "LIST":
		strat = "l"
	case "HASH":
		strat = "h"
	}
	zeros := make([]uint32, natts)
	row := Row{
		NewIntDatum(int64(tbl.OID)),
		NewStringDatum(strat),
		NewIntDatum(int64(natts)),
		NewIntDatum(0), // partdefid: the default partition is re-found from the children
		NewBytesDatum(pgInt2VectorBytes(attnums)),
		NewBytesDatum(pgOIDVectorBytes(zeros)),
		NewBytesDatum(pgOIDVectorBytes(zeros)),
		NewStringDatum(PartitionKeyDef(tbl)),
	}
	rel := storage.RelFileNode{DBOid: tableCatalogHeapDBOid(ctx), RelOid: pgPartitionedTableRelOID, Fork: storage.MainFork}
	_, err := writeHeapRowCanonical(ctx, rel, PGPartitionedTableColumnsPG18(), row)
	return err
}
