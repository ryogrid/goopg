package initdb

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgLanguageEntry mirrors one row of PG18's pg_language (OID 2612).
//
// M0131-S9.3f completed the row: it used to stop at laninline (7 columns),
// leaving lanvalidator (attnum 8) and lanacl (attnum 9) off BOTH the heap and
// pgLanguageAttrs(). That was a self-consistent truncation, not a corruption —
// but it is not what PG18 has, and a hosted PG evaluating a view that JOINS
// pg_language resolves every column of the join's inputs through
// SearchSysCache2(ATTNUM) (expandRTE → get_rte_attribute_is_dropped,
// parse_relation.c:3414), not just the selected ones. `pg_seclabels`' language
// branch is the corpus's first such view and it failed with "cache lookup
// failed for attribute 8 of relation 2612".
//
// lanvalidator carries the real BKI values (fmgr_{internal,c,sql}_validator =
// 2246/2247/2248, postgres/src/include/catalog/pg_language.dat); lanacl is
// varlena and NULL for every bootstrap row, as it is upstream.
type pgLanguageEntry struct {
	OID           uint32
	LanName       string // name (max 64 bytes)
	LanOwner      uint32 // 10 = BOOTSTRAP_SUPERUSERID for all BKI rows
	LanIspl       bool   // is procedural language?
	LanPltrusted  bool   // is PL trusted?
	LanPlcallfoid uint32 // oid of call handler (0 for built-in languages)
	LanInline     uint32 // oid of inline handler (0 for most; 2511 for sql)
	LanValidator  uint32 // oid of validator (2246/2247/2248 for the BKI rows)
}

// pgLanguageColDefs returns the 9 columns of pg_language matching
// pgLanguageAttrs() in relcache_init.go and the physical heap layout that PG18
// reads via GETSTRUCT. The first 8 are fixed-length; lanacl is the CATALOG_VARLEN
// tail and is NULL in every bootstrap row.
func pgLanguageColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "lanname", Type: catalog.Type{Name: "name"}},
		{Name: "lanowner", Type: catalog.Type{Name: "oid"}},
		{Name: "lanispl", Type: catalog.Type{Name: "bool"}},
		{Name: "lanpltrusted", Type: catalog.Type{Name: "bool"}},
		{Name: "lanplcallfoid", Type: catalog.Type{Name: "oid"}},
		{Name: "laninline", Type: catalog.Type{Name: "oid"}},
		{Name: "lanvalidator", Type: catalog.Type{Name: "oid"}},
		{Name: "lanacl", Type: catalog.Type{Name: "aclitem", IsArray: true}},
	}
}

// pgLanguageRow converts a pgLanguageEntry into an executor.Row using the
// column order defined by pgLanguageColDefs().
func pgLanguageRow(e pgLanguageEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),           // 1 oid
		executor.NewStringDatum(e.LanName),           // 2 lanname
		executor.NewIntDatum(int64(e.LanOwner)),      // 3 lanowner
		executor.NewBoolDatum(e.LanIspl),             // 4 lanispl
		executor.NewBoolDatum(e.LanPltrusted),        // 5 lanpltrusted
		executor.NewIntDatum(int64(e.LanPlcallfoid)), // 6 lanplcallfoid
		executor.NewIntDatum(int64(e.LanInline)),     // 7 laninline
		executor.NewIntDatum(int64(e.LanValidator)),  // 8 lanvalidator
		executor.NullDatum,                           // 9 lanacl (NULL upstream too)
	}
}

// pgLanguageInitialEntries returns the 3 BKI-derived rows for pg_language:
// internal (OID 12), c (OID 13), sql (OID 14).
// Source: postgres/src/include/catalog/pg_language.dat
func pgLanguageInitialEntries() []pgLanguageEntry {
	const pguid = 10 // BOOTSTRAP_SUPERUSERID
	return []pgLanguageEntry{
		// oid=12: built-in internal language (used by internal functions);
		// lanvalidator=2246=fmgr_internal_validator
		{12, "internal", pguid, false, false, 0, 0, 2246},
		// oid=13: dynamically-loaded C functions; lanvalidator=2247=fmgr_c_validator
		{13, "c", pguid, false, false, 0, 0, 2247},
		// oid=14: SQL function language (trusted; laninline=2511=inline_sql_handler,
		// lanvalidator=2248=fmgr_sql_validator)
		{14, "sql", pguid, false, true, 0, 2511, 2248},
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
