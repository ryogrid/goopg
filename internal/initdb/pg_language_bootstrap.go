package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgLanguageEntry mirrors one row of PG18's pg_language (OID 2612).
// goopg stores the 7 fixed-size columns that match pgLanguageAttrs() in
// relcache_init.go (oid, lanname, lanowner, lanispl, lanpltrusted,
// lanplcallfoid, laninline). lanvalidator (attnum 8) and lanacl (varlena)
// are excluded to match the 7-column nailed-rel schema.
type pgLanguageEntry struct {
	OID          uint32
	LanName      string // name (max 64 bytes)
	LanOwner     uint32 // 10 = BOOTSTRAP_SUPERUSERID for all BKI rows
	LanIspl      bool   // is procedural language?
	LanPltrusted bool   // is PL trusted?
	LanPlcallfoid uint32 // oid of call handler (0 for built-in languages)
	LanInline    uint32 // oid of inline handler (0 for most; 2511 for sql)
}

// pgLanguageColDefs returns the 7 fixed-length columns of pg_language
// matching pgLanguageAttrs() in relcache_init.go and the physical heap
// layout that PG18 reads via GETSTRUCT.
func pgLanguageColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "lanname", Type: catalog.Type{Name: "name"}},
		{Name: "lanowner", Type: catalog.Type{Name: "oid"}},
		{Name: "lanispl", Type: catalog.Type{Name: "bool"}},
		{Name: "lanpltrusted", Type: catalog.Type{Name: "bool"}},
		{Name: "lanplcallfoid", Type: catalog.Type{Name: "oid"}},
		{Name: "laninline", Type: catalog.Type{Name: "oid"}},
	}
}

// pgLanguageRow converts a pgLanguageEntry into an executor.Row using the
// column order defined by pgLanguageColDefs().
func pgLanguageRow(e pgLanguageEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),            // 1 oid
		executor.NewStringDatum(e.LanName),            // 2 lanname
		executor.NewIntDatum(int64(e.LanOwner)),       // 3 lanowner
		executor.NewBoolDatum(e.LanIspl),              // 4 lanispl
		executor.NewBoolDatum(e.LanPltrusted),         // 5 lanpltrusted
		executor.NewIntDatum(int64(e.LanPlcallfoid)),  // 6 lanplcallfoid
		executor.NewIntDatum(int64(e.LanInline)),      // 7 laninline
	}
}

// pgLanguageInitialEntries returns the 3 BKI-derived rows for pg_language:
// internal (OID 12), c (OID 13), sql (OID 14).
// Source: postgres/src/include/catalog/pg_language.dat
func pgLanguageInitialEntries() []pgLanguageEntry {
	const pguid = 10 // BOOTSTRAP_SUPERUSERID
	return []pgLanguageEntry{
		// oid=12: built-in internal language (used by internal functions)
		{12, "internal", pguid, false, false, 0, 0},
		// oid=13: dynamically-loaded C functions
		{13, "c", pguid, false, false, 0, 0},
		// oid=14: SQL function language (trusted; laninline=2511=inline_sql_handler)
		{14, "sql", pguid, false, true, 0, 2511},
	}
}

// bootstrapPgLanguageTuples writes the 3 pg_language rows to
// base/{1,5}/2612 and returns a map from row OID to heapTID so
// the caller can build pg_language_oid_index (2682) and
// pg_language_name_index (2681).
func bootstrapPgLanguageTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgLanguageColDefs()
	entries := pgLanguageInitialEntries()
	rows := make([]executor.Row, len(entries))
	for i, e := range entries {
		rows[i] = pgLanguageRow(e)
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "2612", cols, rows)
	if err != nil {
		return nil, fmt.Errorf("bootstrapPgLanguageTuples: %w", err)
	}
	tidMap := make(map[uint32]heapTID, len(entries))
	for i, e := range entries {
		tidMap[e.OID] = rawTIDs[i]
	}
	return tidMap, nil
}
