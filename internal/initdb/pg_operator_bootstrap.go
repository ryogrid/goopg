package initdb

import (
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// operatorEntry holds one row of pg_operator as seeded during bootstrap.
// Field names match the pg_operator.h column names used in initdb.
type operatorEntry struct {
	OID        uint32
	Name       string // oprname
	Namespace  uint32 // oprnamespace
	Owner      uint32 // oprowner
	Kind       byte   // oprkind: 'b'=binary, 'l'=left-unary
	CanMerge   bool   // oprcanmerge
	CanHash    bool   // oprcanhash
	LeftType   uint32 // oprleft
	RightType  uint32 // oprright
	ResultType uint32 // oprresult
	Commutator uint32 // oprcom
	Negator    uint32 // oprnegate
	Code       uint32 // oprcode (pg_proc OID)
	Restrict   uint32 // oprrest (pg_proc OID)
	Join       uint32 // oprjoin (pg_proc OID)
}

// pgOperatorInitialEntries returns all pg_operator rows seeded during
// bootstrap.  It delegates to the generated pgOperatorAllEntries table.
func pgOperatorInitialEntries() []operatorEntry {
	return pgOperatorAllEntries()
}

// pgOperatorColDefs returns the 15-column PG18 FormData_pg_operator layout.
// Source: postgres/src/include/catalog/pg_operator.h.
func pgOperatorColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},             // 1
		{Name: "oprname", Type: catalog.Type{Name: "name"}},        // 2
		{Name: "oprnamespace", Type: catalog.Type{Name: "oid"}},    // 3
		{Name: "oprowner", Type: catalog.Type{Name: "oid"}},        // 4
		{Name: "oprkind", Type: catalog.Type{Name: "char"}},        // 5
		{Name: "oprcanmerge", Type: catalog.Type{Name: "bool"}},    // 6
		{Name: "oprcanhash", Type: catalog.Type{Name: "bool"}},     // 7
		{Name: "oprleft", Type: catalog.Type{Name: "oid"}},         // 8
		{Name: "oprright", Type: catalog.Type{Name: "oid"}},        // 9
		{Name: "oprresult", Type: catalog.Type{Name: "oid"}},       // 10
		{Name: "oprcom", Type: catalog.Type{Name: "oid"}},          // 11
		{Name: "oprnegate", Type: catalog.Type{Name: "oid"}},       // 12
		{Name: "oprcode", Type: catalog.Type{Name: "regproc"}},     // 13
		{Name: "oprrest", Type: catalog.Type{Name: "regproc"}},     // 14
		{Name: "oprjoin", Type: catalog.Type{Name: "regproc"}},     // 15
	}
}

// pgOperatorRow materialises one operatorEntry as the 15-column row
// matching pgOperatorColDefs().
func pgOperatorRow(e operatorEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),                       // 1 oid
		executor.NewStringDatum(e.Name),                          // 2 oprname
		executor.NewIntDatum(int64(e.Namespace)),                 // 3 oprnamespace
		executor.NewIntDatum(int64(e.Owner)),                     // 4 oprowner
		executor.NewStringDatum(string([]byte{e.Kind})),          // 5 oprkind
		executor.NewBoolDatum(e.CanMerge),                        // 6 oprcanmerge
		executor.NewBoolDatum(e.CanHash),                         // 7 oprcanhash
		executor.NewIntDatum(int64(e.LeftType)),                  // 8 oprleft
		executor.NewIntDatum(int64(e.RightType)),                 // 9 oprright
		executor.NewIntDatum(int64(e.ResultType)),                // 10 oprresult
		executor.NewIntDatum(int64(e.Commutator)),                // 11 oprcom
		executor.NewIntDatum(int64(e.Negator)),                   // 12 oprnegate
		executor.NewIntDatum(int64(e.Code)),                      // 13 oprcode
		executor.NewIntDatum(int64(e.Restrict)),                  // 14 oprrest
		executor.NewIntDatum(int64(e.Join)),                      // 15 oprjoin
	}
}

// bootstrapPgOperatorTuples overwrites base/{1,5}/2617 with PG-canonical
// FormData_pg_operator heap tuples for all 799 pg_operator.dat rows.
// Returns a map from operator OID to heap TID for index seeding.
// Called from Init() after bootstrapPgProcOidIndex.
func bootstrapPgOperatorTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgOperatorColDefs()
	entries := pgOperatorInitialEntries()

	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgOperatorRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2617", cols, rows)
	if err != nil {
		return nil, err
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}
