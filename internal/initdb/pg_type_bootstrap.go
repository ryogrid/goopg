package initdb

import (
	"sort"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// pgTypeColDefs returns column descriptors that mirror PG18's
// FormData_pg_type fixed-part struct byte-for-byte
// (postgres/src/include/catalog/pg_type.h). Total fixed-part size = 148
// bytes; typalign lands at offset 128 after `Form_pg_type *` cast — the
// invariant that M0106-0010 Step 3cq depends on to clear the PG-standby
// `invalid attalign value:` FATAL at `populate_compact_attribute_internal,
// tupdesc.c:105` (called from TupleDescInitEntry → SysCache TYPEOID lookup
// → memcpy(att,tp,ATTRIBUTE_FIXED_PART_SIZE)). The CATALOG_VARLEN trailer
// (typdefaultbin/typdefault/typacl) is emitted as SQL NULL via the null
// bitmap path (Step 3i); none of the three are read on early-boot paths.
func pgTypeColDefs() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},                   // 1  off 0   sz 4
		{Name: "typname", Type: catalog.Type{Name: "name"}},              // 2  off 4   sz 64
		{Name: "typnamespace", Type: catalog.Type{Name: "oid"}},          // 3  off 68  sz 4
		{Name: "typowner", Type: catalog.Type{Name: "oid"}},              // 4  off 72  sz 4
		{Name: "typlen", Type: catalog.Type{Name: "int2"}},               // 5  off 76  sz 2
		{Name: "typbyval", Type: catalog.Type{Name: "bool"}},             // 6  off 78  sz 1
		{Name: "typtype", Type: catalog.Type{Name: "char"}},              // 7  off 79  sz 1
		{Name: "typcategory", Type: catalog.Type{Name: "char"}},          // 8  off 80  sz 1
		{Name: "typispreferred", Type: catalog.Type{Name: "bool"}},       // 9  off 81  sz 1
		{Name: "typisdefined", Type: catalog.Type{Name: "bool"}},         // 10 off 82  sz 1
		{Name: "typdelim", Type: catalog.Type{Name: "char"}},             // 11 off 83  sz 1
		{Name: "typrelid", Type: catalog.Type{Name: "oid"}},              // 12 off 84  sz 4
		{Name: "typsubscript", Type: catalog.Type{Name: "regproc"}},      // 13 off 88  sz 4
		{Name: "typelem", Type: catalog.Type{Name: "oid"}},               // 14 off 92  sz 4
		{Name: "typarray", Type: catalog.Type{Name: "oid"}},              // 15 off 96  sz 4
		{Name: "typinput", Type: catalog.Type{Name: "regproc"}},          // 16 off 100 sz 4
		{Name: "typoutput", Type: catalog.Type{Name: "regproc"}},         // 17 off 104 sz 4
		{Name: "typreceive", Type: catalog.Type{Name: "regproc"}},        // 18 off 108 sz 4
		{Name: "typsend", Type: catalog.Type{Name: "regproc"}},           // 19 off 112 sz 4
		{Name: "typmodin", Type: catalog.Type{Name: "regproc"}},          // 20 off 116 sz 4
		{Name: "typmodout", Type: catalog.Type{Name: "regproc"}},         // 21 off 120 sz 4
		{Name: "typanalyze", Type: catalog.Type{Name: "regproc"}},        // 22 off 124 sz 4
		{Name: "typalign", Type: catalog.Type{Name: "char"}},             // 23 off 128 sz 1  ← target
		{Name: "typstorage", Type: catalog.Type{Name: "char"}},           // 24 off 129 sz 1
		{Name: "typnotnull", Type: catalog.Type{Name: "bool"}},           // 25 off 130 sz 1
		{Name: "typbasetype", Type: catalog.Type{Name: "oid"}},           // 26 off 132 sz 4
		{Name: "typtypmod", Type: catalog.Type{Name: "int4"}},            // 27 off 136 sz 4
		{Name: "typndims", Type: catalog.Type{Name: "int4"}},             // 28 off 140 sz 4
		{Name: "typcollation", Type: catalog.Type{Name: "oid"}},          // 29 off 144 sz 4
		{Name: "typdefaultbin", Type: catalog.Type{Name: "pg_node_tree"}}, // 30 NULL (varlen)
		{Name: "typdefault", Type: catalog.Type{Name: "text"}},           // 31 NULL (varlen)
		{Name: "typacl", Type: catalog.Type{Name: "aclitem[]"}},          // 32 NULL (varlen)
	}
}

// pgTypeEntry captures the load-bearing fixed-part fields of a PG18
// pg_type row.  Field values are sourced from
// `postgres/src/include/catalog/pg_type.dat`.
type pgTypeEntry struct {
	OID      uint32
	Name     string
	Len      int16
	ByVal    bool
	Type     byte // 'b'=base, 'c'=composite, 'p'=pseudo, 'd'=domain, 'e'=enum, 'r'=range
	Category byte // typcategory; capital letter per pg_type.h TYPCATEGORY_*
	Align    byte // 'c'/'s'/'i'/'d'
	Storage  byte // 'p'/'e'/'x'/'m'
}

// pgTypeCanonical returns the PG18-canonical metadata for an OID.
// Sourced verbatim from `postgres/src/include/catalog/pg_type.dat`.
// Any OID that any nailedRel attribute references must be present here;
// the regression pin TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs
// enforces that invariant so a future nailedAttr referencing a new OID
// cannot regress the FATAL silently.
func pgTypeCanonical(oid uint32) (pgTypeEntry, bool) {
	switch oid {
	case 16:
		return pgTypeEntry{16, "bool", 1, true, 'b', 'B', 'c', 'p'}, true
	case 17:
		return pgTypeEntry{17, "bytea", -1, false, 'b', 'U', 'i', 'x'}, true
	case 18:
		return pgTypeEntry{18, "char", 1, true, 'b', 'Z', 'c', 'p'}, true
	case 19:
		return pgTypeEntry{19, "name", 64, false, 'b', 'S', 'c', 'p'}, true
	case 20:
		return pgTypeEntry{20, "int8", 8, true, 'b', 'N', 'd', 'p'}, true
	case 21:
		return pgTypeEntry{21, "int2", 2, true, 'b', 'N', 's', 'p'}, true
	case 22:
		return pgTypeEntry{22, "int2vector", -1, false, 'b', 'A', 'i', 'p'}, true
	case 23:
		return pgTypeEntry{23, "int4", 4, true, 'b', 'N', 'i', 'p'}, true
	case 24:
		return pgTypeEntry{24, "regproc", 4, true, 'b', 'N', 'i', 'p'}, true
	case 25:
		return pgTypeEntry{25, "text", -1, false, 'b', 'S', 'i', 'x'}, true
	case 26:
		return pgTypeEntry{26, "oid", 4, true, 'b', 'N', 'i', 'p'}, true
	case 27:
		return pgTypeEntry{27, "tid", 6, false, 'b', 'U', 's', 'p'}, true
	case 28:
		return pgTypeEntry{28, "xid", 4, true, 'b', 'U', 'i', 'p'}, true
	case 29:
		return pgTypeEntry{29, "cid", 4, true, 'b', 'U', 'i', 'p'}, true
	case 30:
		return pgTypeEntry{30, "oidvector", -1, false, 'b', 'A', 'i', 'p'}, true
	case 194:
		return pgTypeEntry{194, "pg_node_tree", -1, false, 'b', 'S', 'i', 'x'}, true
	case 269:
		return pgTypeEntry{269, "table_am_handler", 4, true, 'p', 'P', 'i', 'p'}, true
	case 325:
		return pgTypeEntry{325, "index_am_handler", 4, true, 'p', 'P', 'i', 'p'}, true
	case 700:
		return pgTypeEntry{700, "float4", 4, true, 'b', 'N', 'i', 'p'}, true
	case 701:
		return pgTypeEntry{701, "float8", 8, true, 'b', 'N', 'd', 'p'}, true
	case 1002:
		return pgTypeEntry{1002, "_char", -1, false, 'b', 'A', 'i', 'x'}, true
	case 1021:
		return pgTypeEntry{1021, "_float4", -1, false, 'b', 'A', 'i', 'x'}, true
	case 1009:
		return pgTypeEntry{1009, "_text", -1, false, 'b', 'A', 'i', 'x'}, true
	case 1028:
		return pgTypeEntry{1028, "_oid", -1, false, 'b', 'A', 'i', 'x'}, true
	case 1033:
		return pgTypeEntry{1033, "aclitem", 12, false, 'b', 'U', 'i', 'p'}, true
	case 1034:
		return pgTypeEntry{1034, "_aclitem", -1, false, 'b', 'A', 'i', 'x'}, true
	case 1042:
		return pgTypeEntry{1042, "bpchar", -1, false, 'b', 'S', 'i', 'x'}, true
	case 1043:
		return pgTypeEntry{1043, "varchar", -1, false, 'b', 'S', 'i', 'x'}, true
	case 1184:
		return pgTypeEntry{1184, "timestamptz", 8, true, 'b', 'D', 'd', 'p'}, true
	case 1185:
		return pgTypeEntry{1185, "_timestamptz", -1, false, 'b', 'A', 'd', 'x'}, true
	case 2277:
		return pgTypeEntry{2277, "anyarray", -1, false, 'p', 'P', 'i', 'x'}, true
	case 2281:
		return pgTypeEntry{2281, "internal", 4, true, 'p', 'P', 'i', 'p'}, true
	case 3220:
		return pgTypeEntry{3220, "pg_lsn", 8, true, 'b', 'U', 'd', 'p'}, true
	case 3361:
		return pgTypeEntry{3361, "pg_ndistinct", -1, false, 'b', 'Z', 'i', 'x'}, true
	case 3402:
		return pgTypeEntry{3402, "pg_dependencies", -1, false, 'b', 'Z', 'i', 'x'}, true
	case 5017:
		return pgTypeEntry{5017, "pg_mcv_list", -1, false, 'b', 'Z', 'i', 'x'}, true
	case 10028:
		// rowtype array for pg_statistic — typtype='c' carries no special
		// meaning for the standby's TupleDescInitEntry path; the load-
		// bearing fields are typalign='d' + typstorage='x'.
		return pgTypeEntry{10028, "_pg_statistic", -1, false, 'c', 'A', 'd', 'x'}, true
	}
	return pgTypeEntry{}, false
}

// pgTypeOIDsUsedByNailedAttrs returns the deduplicated, sorted set of
// type OIDs referenced by any column of any nailed relation. This is the
// minimum set PG18 will SysCache-look-up during early standby boot —
// each one must have a PG-canonical heap row written by
// bootstrapPgTypeTuples or `TupleDescInitEntry` reads garbage from the
// v0-encoded fallback and FATALs at tupdesc.c:105.
func pgTypeOIDsUsedByNailedAttrs() []uint32 {
	seen := make(map[uint32]struct{})
	allRels := append([]nailedRel{}, nailedSharedRels...)
	allRels = append(allRels, nailedLocalRels...)
	for _, rel := range allRels {
		for _, a := range pgAttrEntriesForRel(rel) {
			if a.TypeOID == 0 {
				continue
			}
			seen[a.TypeOID] = struct{}{}
		}
	}
	oids := make([]uint32, 0, len(seen))
	for oid := range seen {
		oids = append(oids, oid)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })
	return oids
}

// pgTypeInitialEntries returns one pgTypeEntry per OID referenced by
// any nailedAttr. Entries are sorted ascending by OID.
func pgTypeInitialEntries() []pgTypeEntry {
	oids := pgTypeOIDsUsedByNailedAttrs()
	entries := make([]pgTypeEntry, 0, len(oids))
	for _, oid := range oids {
		if e, ok := pgTypeCanonical(oid); ok {
			entries = append(entries, e)
		}
	}
	return entries
}

// pgTypeRow encodes one pgTypeEntry into a 32-column executor.Row matching
// pgTypeColDefs(). All optional regproc fields and the three CATALOG_VARLEN
// columns are zero/NULL — only the fixed fields PG18 actually reads on the
// early-boot TupleDescInitEntry path are populated.
func pgTypeRow(e pgTypeEntry) executor.Row {
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),         // 1 oid
		executor.NewStringDatum(e.Name),            // 2 typname
		executor.NewIntDatum(11),                   // 3 typnamespace = pg_catalog
		executor.NewIntDatum(10),                   // 4 typowner = BOOTSTRAP_SUPERUSERID
		executor.NewIntDatum(int64(e.Len)),         // 5 typlen
		executor.NewBoolDatum(e.ByVal),             // 6 typbyval
		executor.NewStringDatum(string(e.Type)),    // 7 typtype
		executor.NewStringDatum(string(e.Category)), // 8 typcategory
		executor.NewBoolDatum(false),               // 9 typispreferred
		executor.NewBoolDatum(true),                // 10 typisdefined
		executor.NewStringDatum(","),               // 11 typdelim
		executor.NewIntDatum(0),                    // 12 typrelid
		executor.NewIntDatum(0),                    // 13 typsubscript
		executor.NewIntDatum(0),                    // 14 typelem
		executor.NewIntDatum(0),                    // 15 typarray
		executor.NewIntDatum(0),                    // 16 typinput
		executor.NewIntDatum(0),                    // 17 typoutput
		executor.NewIntDatum(0),                    // 18 typreceive
		executor.NewIntDatum(0),                    // 19 typsend
		executor.NewIntDatum(0),                    // 20 typmodin
		executor.NewIntDatum(0),                    // 21 typmodout
		executor.NewIntDatum(0),                    // 22 typanalyze
		executor.NewStringDatum(string(e.Align)),   // 23 typalign  ← load-bearing
		executor.NewStringDatum(string(e.Storage)), // 24 typstorage
		executor.NewBoolDatum(false),               // 25 typnotnull
		executor.NewIntDatum(0),                    // 26 typbasetype
		executor.NewIntDatum(-1),                   // 27 typtypmod
		executor.NewIntDatum(0),                    // 28 typndims
		executor.NewIntDatum(0),                    // 29 typcollation
		executor.NullDatum,                         // 30 typdefaultbin
		executor.NullDatum,                         // 31 typdefault
		executor.NullDatum,                         // 32 typacl
	}
}

// bootstrapPgTypeTuples overwrites base/{1,5}/1247 with PG-canonical
// FormData_pg_type heap tuples for every TypeOID referenced by any
// nailedRel attribute.  Called from Init() AFTER bootstrapSystemCatalogs
// (which writes goopg-v0-encoded pg_type rows via extendWithRows) so the
// PG-canonical layout overwrites the v0 layout.
//
// PG18's `StartupXLOG` (xlog.c:5633) unconditionally invokes
// `RelationCacheInitFileRemove()` at WAL recovery start, wiping every
// `pg_internal.init` copied by `copyInitFiles()`. So every backend
// rebuilds tupledesc from the heap; `TupleDescInitEntry`
// (tupdesc.c:902) reads `typeForm->typalign` via SysCache lookup of
// pg_type by OID. If that byte isn't one of 'c'/'s'/'i'/'d', the next
// `populate_compact_attribute_internal` call FATALs at tupdesc.c:105
// (`invalid attalign value:`). Writing the canonical heap here makes
// the SysCache lookup return a proper Form_pg_type pointer with
// typalign at offset 128.
func bootstrapPgTypeTuples(dataDir string) error {
	cols := pgTypeColDefs()
	entries := pgTypeInitialEntries()
	rows := make([]executor.Row, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, pgTypeRow(e))
	}
	_, err := writeMultiPageHeapRows(dataDir, "1247", cols, rows)
	return err
}
