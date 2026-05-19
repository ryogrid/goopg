package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgCollationEntry mirrors one row of PG18's pg_collation (OID 3456).
// goopg stores the 8 fixed-size columns that PG reads during early
// backend startup (before varlena TOAST is available).
type pgCollationEntry struct {
	OID                 uint32
	CollName            string // name (max 64 bytes)
	CollNamespace       uint32 // 11 = pg_catalog for all BKI rows
	CollOwner           uint32 // 10 = BOOTSTRAP_SUPERUSERID for all BKI rows
	CollProvider        byte   // 'd'=default 'c'=libc 'i'=icu 'b'=builtin
	CollIsDeterministic bool   // default true for all BKI rows
	CollEncoding        int32  // -1 = all encodings; 6 = UTF8
	CollCollate         string // LC_COLLATE / locale hint (name, max 64 bytes)
}

// pgCollationColDefs returns the 8-column fixed-size schema for pg_collation
// (OID 3456) used by goopg's bootstrap writer.
func pgCollationColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "collname", Type: catalog.Type{Name: "name"}},
		{Name: "collnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "collowner", Type: catalog.Type{Name: "oid"}},
		{Name: "collprovider", Type: catalog.Type{Name: "char"}},
		{Name: "collisdeterministic", Type: catalog.Type{Name: "bool"}},
		{Name: "collencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "collcollate", Type: catalog.Type{Name: "name"}},
	}
}

// pgCollationRow encodes one pg_collation row. Field order mirrors the
// 8 fixed-size columns of FormData_pg_collation so PG's GETSTRUCT cast
// is byte-for-byte valid for these columns.
func pgCollationRow(e pgCollationEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),                      // 1 oid
		executor.NewStringDatum(e.CollName),                     // 2 collname
		executor.NewIntDatum(int64(e.CollNamespace)),            // 3 collnamespace
		executor.NewIntDatum(int64(e.CollOwner)),                // 4 collowner
		executor.NewStringDatum(string([]byte{e.CollProvider})), // 5 collprovider (char)
		executor.NewBoolDatum(e.CollIsDeterministic),            // 6 collisdeterministic
		executor.NewIntDatum(int64(e.CollEncoding)),             // 7 collencoding
		executor.NewStringDatum(e.CollCollate),                  // 8 collcollate
	}
}

// pgCollationInitialEntries returns the 7 BKI seed rows for pg_collation
// from PG18's pg_collation.dat. These are the built-in collations that
// must exist before the SQL-phase libc/ICU enumeration runs.
//
// collcollate for builtin ('b') rows holds the colllocale field value
// from pg_collation.dat; rows without a locale use "".
func pgCollationInitialEntries() []pgCollationEntry {
	return []pgCollationEntry{
		{100, "default", 11, 10, 'd', true, -1, ""},
		{950, "C", 11, 10, 'c', true, -1, "C"},
		{951, "POSIX", 11, 10, 'c', true, -1, "POSIX"},
		{962, "ucs_basic", 11, 10, 'b', true, 6, "C"},
		{963, "unicode", 11, 10, 'i', true, -1, ""},
		{811, "pg_c_utf8", 11, 10, 'b', true, 6, "C.UTF-8"},
		{6411, "pg_unicode_fast", 11, 10, 'b', true, 6, "PG_UNICODE_FAST"},
	}
}

// bootstrapPgCollationTuples writes all 7 pg_collation rows to
// base/{1,5}/3456 in the 8-column fixed-size layout.
// Returns TIDs keyed by collation OID for index seeding.
func bootstrapPgCollationTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgCollationColDefs()
	entries := pgCollationInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgCollationRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "3456", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("bootstrapPgCollationTuples: %w", err)
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}
