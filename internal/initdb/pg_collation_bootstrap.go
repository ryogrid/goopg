package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgCollationEntry mirrors one row of PG18's pg_collation (OID 3456).
// M0133-S4 widened this from the 8-column bootstrap-survival schema to PG18's
// full 12 columns: collcollate becomes text, and collctype/colllocale/
// collicurules/collversion are appended (all text, all BKI_DEFAULT(_null_)).
// The field values mirror the runtime catalog's PGCollationRowsForDBOid
// (internal/catalog/catalog.go) — sibling paths must agree.
type pgCollationEntry struct {
	OID                 uint32
	CollName            string // name (max 64 bytes)
	CollNamespace       uint32 // 11 = pg_catalog for all BKI rows
	CollOwner           uint32 // 10 = BOOTSTRAP_SUPERUSERID for all BKI rows
	CollProvider        byte   // 'd'=default 'c'=libc 'i'=icu 'b'=builtin
	CollIsDeterministic bool   // default true for all BKI rows
	CollEncoding        int32  // -1 = all encodings; 6 = UTF8
	CollCollate         string // text; LC_COLLATE for libc, NULL ("") otherwise
	CollCtype           string // text; LC_CTYPE for libc, NULL ("") otherwise
	CollLocale          string // text; locale ID for builtin/icu, NULL ("") otherwise
	CollIcuRules        string // text; ICU collation rules, NULL ("") for all BKI rows
	CollVersion         string // text; provider-dependent version, NULL ("") unless '1'
}

// pgCollationColDefs returns the 12-column schema for pg_collation (OID 3456)
// used by goopg's bootstrap writer. collcollate..collversion are varlena text
// and nullable (BKI_DEFAULT(_null_)); an empty string encodes as SQL NULL.
func pgCollationColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "collname", Type: catalog.Type{Name: "name"}},
		{Name: "collnamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "collowner", Type: catalog.Type{Name: "oid"}},
		{Name: "collprovider", Type: catalog.Type{Name: "char"}},
		{Name: "collisdeterministic", Type: catalog.Type{Name: "bool"}},
		{Name: "collencoding", Type: catalog.Type{Name: "int4"}},
		{Name: "collcollate", Type: catalog.Type{Name: "text"}},
		{Name: "collctype", Type: catalog.Type{Name: "text"}},
		{Name: "colllocale", Type: catalog.Type{Name: "text"}},
		{Name: "collicurules", Type: catalog.Type{Name: "text"}},
		{Name: "collversion", Type: catalog.Type{Name: "text"}},
	}
}

// pgCollationRow encodes one pg_collation row. Field order mirrors the
// 12 columns of FormData_pg_collation so PG's heap_deformtuple casts the
// GETSTRUCT result correctly; the trailing text columns carry NullDatum
// (rather than "") when their entry field is empty, matching BKI_DEFAULT(_null_).
func pgCollationRow(e pgCollationEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),                      // 1 oid
		executor.NewStringDatum(e.CollName),                     // 2 collname
		executor.NewIntDatum(int64(e.CollNamespace)),            // 3 collnamespace
		executor.NewIntDatum(int64(e.CollOwner)),                // 4 collowner
		executor.NewStringDatum(string([]byte{e.CollProvider})), // 5 collprovider (char)
		executor.NewBoolDatum(e.CollIsDeterministic),            // 6 collisdeterministic
		executor.NewIntDatum(int64(e.CollEncoding)),             // 7 collencoding
		pgCollationTextDatum(e.CollCollate),                     // 8 collcollate (text)
		pgCollationTextDatum(e.CollCtype),                       // 9 collctype (text)
		pgCollationTextDatum(e.CollLocale),                      // 10 colllocale (text)
		pgCollationTextDatum(e.CollIcuRules),                    // 11 collicurules (text)
		pgCollationTextDatum(e.CollVersion),                     // 12 collversion (text)
	}
}

// pgCollationTextDatum maps a collation text column value to its datum: "" is
// SQL NULL (BKI_DEFAULT(_null_)), anything else is a text datum.
func pgCollationTextDatum(s string) executor.Datum {
	if s == "" {
		return executor.NullDatum
	}
	return executor.NewStringDatum(s)
}

// pgCollationInitialEntries returns the 7 BKI seed rows for pg_collation
// from PG18's pg_collation.dat. These are the built-in collations that
// must exist before the SQL-phase libc/ICU enumeration runs.
//
// Per-provider NULLs match PG18: libc carries collcollate/collctype and
// leaves colllocale NULL; builtin/icu carry colllocale and leave
// collcollate/collctype NULL. collicurules is NULL for every BKI row;
// collversion is "1" only for the builtin provider rows that declare one.
func pgCollationInitialEntries() []pgCollationEntry {
	return []pgCollationEntry{
		{100, "default", 11, 10, 'd', true, -1, "", "", "", "", ""},
		{950, "C", 11, 10, 'c', true, -1, "C", "C", "", "", ""},
		{951, "POSIX", 11, 10, 'c', true, -1, "POSIX", "POSIX", "", "", ""},
		{962, "ucs_basic", 11, 10, 'b', true, 6, "", "", "C", "", "1"},
		{963, "unicode", 11, 10, 'i', true, -1, "", "", "und", "", ""},
		{811, "pg_c_utf8", 11, 10, 'b', true, 6, "", "", "C.UTF-8", "", "1"},
		{6411, "pg_unicode_fast", 11, 10, 'b', true, 6, "", "", "PG_UNICODE_FAST", "", "1"},
	}
}

// bootstrapPgCollationTuples writes all 7 pg_collation rows to
// base/{1,5}/3456 in the 12-column PG18 layout.
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
