package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgRangeEntry mirrors one row of PG18's pg_range (OID 3541).
// pg_range has no 'oid' system column; rngtypid is the primary key.
// 0 is the sentinel for optional columns (rngcollation, rngcanonical,
// rngsubdiff) that have no meaningful value for a given type.
type pgRangeEntry struct {
	RngTypID      uint32 // attnum 1 — OID of the range type (PK)
	RngSubtype    uint32 // attnum 2 — OID of the element type
	RngMultiTypID uint32 // attnum 3 — OID of the multirange type
	RngCollation  uint32 // attnum 4 — OID of collation (0 = none)
	RngSubOpc     uint32 // attnum 5 — OID of subtype's btree opclass
	RngCanonical  uint32 // attnum 6 — OID of canonical fn (0 = none)
	RngSubDiff    uint32 // attnum 7 — OID of subdiff fn (0 = none)
}

func pgRangeColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "rngtypid", Type: catalog.Type{Name: "oid"}},
		{Name: "rngsubtype", Type: catalog.Type{Name: "oid"}},
		{Name: "rngmultitypid", Type: catalog.Type{Name: "oid"}},
		{Name: "rngcollation", Type: catalog.Type{Name: "oid"}},
		{Name: "rngsubopc", Type: catalog.Type{Name: "oid"}},
		{Name: "rngcanonical", Type: catalog.Type{Name: "regproc"}},
		{Name: "rngsubdiff", Type: catalog.Type{Name: "regproc"}},
	}
}

func pgRangeRow(e pgRangeEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.RngTypID)),      // 1 rngtypid
		executor.NewIntDatum(int64(e.RngSubtype)),    // 2 rngsubtype
		executor.NewIntDatum(int64(e.RngMultiTypID)), // 3 rngmultitypid
		executor.NewIntDatum(int64(e.RngCollation)),  // 4 rngcollation
		executor.NewIntDatum(int64(e.RngSubOpc)),     // 5 rngsubopc
		executor.NewIntDatum(int64(e.RngCanonical)),  // 6 rngcanonical
		executor.NewIntDatum(int64(e.RngSubDiff)),    // 7 rngsubdiff
	}
}

// pgRangeInitialEntries returns the 6 built-in range rows from PG18's
// pg_range.dat (src/include/catalog/pg_range.dat).
// OIDs sourced from pg_type.dat, pg_proc.dat, and pg_opclass.dat.
func pgRangeInitialEntries() []pgRangeEntry {
	return []pgRangeEntry{
		// int4range(3904): subtype=int4(23), multi=int4multirange(4451),
		//   opc=btree/int4_ops(1978), canonical=int4range_canonical(3914),
		//   subdiff=int4range_subdiff(3922)
		{3904, 23, 4451, 0, 1978, 3914, 3922},
		// numrange(3906): subtype=numeric(1700), multi=nummultirange(4532),
		//   opc=btree/numeric_ops(3125), no canonical, subdiff=numrange_subdiff(3924)
		{3906, 1700, 4532, 0, 3125, 0, 3924},
		// tsrange(3908): subtype=timestamp(1114), multi=tsmultirange(4533),
		//   opc=btree/timestamp_ops(3128), no canonical, subdiff=tsrange_subdiff(3929)
		{3908, 1114, 4533, 0, 3128, 0, 3929},
		// tstzrange(3910): subtype=timestamptz(1184), multi=tstzmultirange(4534),
		//   opc=btree/timestamptz_ops(3127), no canonical, subdiff=tstzrange_subdiff(3930)
		{3910, 1184, 4534, 0, 3127, 0, 3930},
		// daterange(3912): subtype=date(1082), multi=datemultirange(4535),
		//   opc=btree/date_ops(3122), canonical=daterange_canonical(3915),
		//   subdiff=daterange_subdiff(3925)
		{3912, 1082, 4535, 0, 3122, 3915, 3925},
		// int8range(3926): subtype=int8(20), multi=int8multirange(4536),
		//   opc=btree/int8_ops(3124), canonical=int8range_canonical(3928),
		//   subdiff=int8range_subdiff(3923)
		{3926, 20, 4536, 0, 3124, 3928, 3923},
	}
}

// bootstrapPgRangeTuples writes all 6 pg_range rows to base/{1,5}/3541.
// Returns TIDs keyed by rngtypid for index seeding.
func bootstrapPgRangeTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgRangeColDefs()
	entries := pgRangeInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgRangeRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "3541", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("bootstrapPgRangeTuples: %w", err)
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.RngTypID] = rawTIDs[i]
	}
	return tidMap, nil
}
