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
		{Name: "oid", Type: catalog.Type{Name: "oid"}},                    // 1  off 0   sz 4
		{Name: "typname", Type: catalog.Type{Name: "name"}},               // 2  off 4   sz 64
		{Name: "typnamespace", Type: catalog.Type{Name: "oid"}},           // 3  off 68  sz 4
		{Name: "typowner", Type: catalog.Type{Name: "oid"}},               // 4  off 72  sz 4
		{Name: "typlen", Type: catalog.Type{Name: "int2"}},                // 5  off 76  sz 2
		{Name: "typbyval", Type: catalog.Type{Name: "bool"}},              // 6  off 78  sz 1
		{Name: "typtype", Type: catalog.Type{Name: "char"}},               // 7  off 79  sz 1
		{Name: "typcategory", Type: catalog.Type{Name: "char"}},           // 8  off 80  sz 1
		{Name: "typispreferred", Type: catalog.Type{Name: "bool"}},        // 9  off 81  sz 1
		{Name: "typisdefined", Type: catalog.Type{Name: "bool"}},          // 10 off 82  sz 1
		{Name: "typdelim", Type: catalog.Type{Name: "char"}},              // 11 off 83  sz 1
		{Name: "typrelid", Type: catalog.Type{Name: "oid"}},               // 12 off 84  sz 4
		{Name: "typsubscript", Type: catalog.Type{Name: "regproc"}},       // 13 off 88  sz 4
		{Name: "typelem", Type: catalog.Type{Name: "oid"}},                // 14 off 92  sz 4
		{Name: "typarray", Type: catalog.Type{Name: "oid"}},               // 15 off 96  sz 4
		{Name: "typinput", Type: catalog.Type{Name: "regproc"}},           // 16 off 100 sz 4
		{Name: "typoutput", Type: catalog.Type{Name: "regproc"}},          // 17 off 104 sz 4
		{Name: "typreceive", Type: catalog.Type{Name: "regproc"}},         // 18 off 108 sz 4
		{Name: "typsend", Type: catalog.Type{Name: "regproc"}},            // 19 off 112 sz 4
		{Name: "typmodin", Type: catalog.Type{Name: "regproc"}},           // 20 off 116 sz 4
		{Name: "typmodout", Type: catalog.Type{Name: "regproc"}},          // 21 off 120 sz 4
		{Name: "typanalyze", Type: catalog.Type{Name: "regproc"}},         // 22 off 124 sz 4
		{Name: "typalign", Type: catalog.Type{Name: "char"}},              // 23 off 128 sz 1  ← target
		{Name: "typstorage", Type: catalog.Type{Name: "char"}},            // 24 off 129 sz 1
		{Name: "typnotnull", Type: catalog.Type{Name: "bool"}},            // 25 off 130 sz 1
		{Name: "typbasetype", Type: catalog.Type{Name: "oid"}},            // 26 off 132 sz 4
		{Name: "typtypmod", Type: catalog.Type{Name: "int4"}},             // 27 off 136 sz 4
		{Name: "typndims", Type: catalog.Type{Name: "int4"}},              // 28 off 140 sz 4
		{Name: "typcollation", Type: catalog.Type{Name: "oid"}},           // 29 off 144 sz 4
		{Name: "typdefaultbin", Type: catalog.Type{Name: "pg_node_tree"}}, // 30 NULL (varlen)
		{Name: "typdefault", Type: catalog.Type{Name: "text"}},            // 31 NULL (varlen)
		{Name: "typacl", Type: catalog.Type{Name: "aclitem[]"}},           // 32 NULL (varlen)
	}
}

// pgTypeEntry captures the load-bearing fixed-part fields of a PG18
// pg_type row.  Field values are sourced from
// `postgres/src/include/catalog/pg_type.dat` (with regproc OIDs cross-
// referenced via `postgres/src/include/catalog/pg_proc.dat`).
type pgTypeEntry struct {
	OID      uint32
	Name     string
	Len      int16
	ByVal    bool
	Type     byte // 'b'=base, 'c'=composite, 'p'=pseudo, 'd'=domain, 'e'=enum, 'r'=range
	Category byte // typcategory; capital letter per pg_type.h TYPCATEGORY_*
	Align    byte // 'c'/'s'/'i'/'d'
	Storage  byte // 'p'/'e'/'x'/'m'
	// I/O regproc OIDs (pg_proc.oid). 0 = no function (legitimate for
	// pseudo types and aclitem). PG18's getTypeOutputInfo
	// (lsyscache.c:3063) ERRORs with `no output function available for
	// type ...` when typoutput == 0, which is the immediate FATAL the
	// M0106-0010 Step 3da fix closes for int4 (OID 23).
	Input   uint32 // typinput
	Output  uint32 // typoutput
	Receive uint32 // typreceive
	Send    uint32 // typsend
}

// pgTypeCanonical returns the PG18-canonical metadata for an OID.
// Sourced verbatim from `postgres/src/include/catalog/pg_type.dat`.
// Any OID that any nailedRel attribute references must be present here;
// the regression pin TestPgTypeInitialEntriesCoverNailedAttrTypeOIDs
// enforces that invariant so a future nailedAttr referencing a new OID
// cannot regress the FATAL silently.
func pgTypeCanonical(oid uint32) (pgTypeEntry, bool) {
	// Array types share the generic array_in/out/recv/send quad
	// (pg_proc.dat OIDs 750/751/2400/2401).
	const (
		arrayIn   uint32 = 750
		arrayOut  uint32 = 751
		arrayRecv uint32 = 2400
		arraySend uint32 = 2401
	)
	switch oid {
	case 16:
		return pgTypeEntry{16, "bool", 1, true, 'b', 'B', 'c', 'p', 1242, 1243, 2436, 2437}, true
	case 17:
		return pgTypeEntry{17, "bytea", -1, false, 'b', 'U', 'i', 'x', 1244, 31, 2412, 2413}, true
	case 18:
		return pgTypeEntry{18, "char", 1, true, 'b', 'Z', 'c', 'p', 1245, 33, 2434, 2435}, true
	case 19:
		return pgTypeEntry{19, "name", 64, false, 'b', 'S', 'c', 'p', 34, 35, 2422, 2423}, true
	case 20:
		return pgTypeEntry{20, "int8", 8, true, 'b', 'N', 'd', 'p', 460, 461, 2408, 2409}, true
	case 21:
		return pgTypeEntry{21, "int2", 2, true, 'b', 'N', 's', 'p', 38, 39, 2404, 2405}, true
	case 22:
		return pgTypeEntry{22, "int2vector", -1, false, 'b', 'A', 'i', 'p', 40, 41, 2410, 2411}, true
	case 23:
		return pgTypeEntry{23, "int4", 4, true, 'b', 'N', 'i', 'p', 42, 43, 2406, 2407}, true
	case 24:
		return pgTypeEntry{24, "regproc", 4, true, 'b', 'N', 'i', 'p', 44, 45, 2444, 2445}, true
	case 25:
		return pgTypeEntry{25, "text", -1, false, 'b', 'S', 'i', 'x', 46, 47, 2414, 2415}, true
	case 26:
		return pgTypeEntry{26, "oid", 4, true, 'b', 'N', 'i', 'p', 1798, 1799, 2418, 2419}, true
	case 27:
		return pgTypeEntry{27, "tid", 6, false, 'b', 'U', 's', 'p', 48, 49, 2438, 2439}, true
	case 28:
		return pgTypeEntry{28, "xid", 4, true, 'b', 'U', 'i', 'p', 50, 51, 2440, 2441}, true
	case 29:
		return pgTypeEntry{29, "cid", 4, true, 'b', 'U', 'i', 'p', 52, 53, 2442, 2443}, true
	case 30:
		return pgTypeEntry{30, "oidvector", -1, false, 'b', 'A', 'i', 'p', 54, 55, 2420, 2421}, true
	case 194:
		return pgTypeEntry{194, "pg_node_tree", -1, false, 'b', 'S', 'i', 'x', 195, 196, 197, 198}, true
	case 269:
		// pseudo type: no recv/send.
		return pgTypeEntry{269, "table_am_handler", 4, true, 'p', 'P', 'i', 'p', 267, 268, 0, 0}, true
	case 325:
		// pseudo type: no recv/send.
		return pgTypeEntry{325, "index_am_handler", 4, true, 'p', 'P', 'i', 'p', 326, 327, 0, 0}, true
	case 700:
		return pgTypeEntry{700, "float4", 4, true, 'b', 'N', 'i', 'p', 200, 201, 2424, 2425}, true
	case 701:
		return pgTypeEntry{701, "float8", 8, true, 'b', 'N', 'd', 'p', 214, 215, 2426, 2427}, true
	case 869:
		// inet: variable-length IP-address type; typcategory='I', typstorage='m'.
		// inet_in=910, inet_out=911, inet_recv=2496, inet_send=2497.
		return pgTypeEntry{869, "inet", -1, false, 'b', 'I', 'i', 'm', 910, 911, 2496, 2497}, true
	case 1000:
		// _bool: the array of bool (16). M0131-S20.2b —
		// pg_stats_ext.most_common_val_nulls / most_common_base_freqs are the
		// first nailed-view columns of a boolean array, and capture guard #5's
		// sibling (the atttypid check) refused pg_stats_ext until the canonical
		// table carried it. typalign 'i', not bool's own 'c' (Catalog.pm:469).
		return pgTypeEntry{1000, "_bool", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1002:
		return pgTypeEntry{1002, "_char", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1003:
		// _name: the array of name (19). M0131-S9.2d — pg_policies.roles is
		// `name[]`, the first nailed-view column of this type, and capture
		// guard #5 refused the view until the canonical table carried it.
		// typalign is 'i', NOT name's own 'c': Catalog.pm:469 gives an array
		// type 'd' when its element is 'd' and 'i' otherwise.
		return pgTypeEntry{1003, "_name", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1005:
		// _int2: the array of int2 (21). Referenced by pg_constraint's conkey/
		// confkey/confdelsetcols (M0133-S1) — the first nailed-attr columns of
		// this type. Was absent from the hand-written switch (it is in
		// pg_type.dat, so the heap already had it via pgTypeAllEntries); the
		// nailed-attr coverage guard demanded a canonical case.
		return pgTypeEntry{1005, "_int2", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1021:
		return pgTypeEntry{1021, "_float4", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1022:
		// _float8: the array of float8 (701). M0131-S20.2b —
		// pg_stats.most_common_freqs / correlation's siblings and
		// pg_stats_ext.most_common_freqs. typalign is 'd' here, NOT the 'i'
		// every other array above carries: Catalog.pm:469 gives an array type
		// 'd' when its element is 'd', and float8's typalign IS 'd' (measured
		// on the oracle, pg_type oid 1022). Getting this wrong misaligns every
		// element a hosted PG deforms out of the column.
		return pgTypeEntry{1022, "_float8", -1, false, 'b', 'A', 'd', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1007:
		return pgTypeEntry{1007, "_int4", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1009:
		return pgTypeEntry{1009, "_text", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 2206:
		// regtype: the OID-alias type, pg_type.dat:389-392. M0131-S9.2d —
		// pg_sequences.data_type is `regtype`, the first nailed-view column
		// of the SCALAR type; its array (2211) was already canonical below.
		// typstorage 'p' (fixed 4-byte by-value), regtypein/out 2220/2221,
		// regtyperecv/send 2454/2455 (pg_proc.dat:7421-7426, 8297-8302).
		return pgTypeEntry{2206, "regtype", 4, true, 'b', 'N', 'i', 'p', 2220, 2221, 2454, 2455}, true
	case 2211:
		// _regtype: the array of regtype (2206), pg_type.dat:389.
		return pgTypeEntry{2211, "_regtype", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1028:
		return pgTypeEntry{1028, "_oid", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1033:
		// aclitem has only in/out (no binary recv/send in upstream).
		return pgTypeEntry{1033, "aclitem", 12, false, 'b', 'U', 'i', 'p', 1031, 1032, 0, 0}, true
	case 1034:
		return pgTypeEntry{1034, "_aclitem", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1042:
		return pgTypeEntry{1042, "bpchar", -1, false, 'b', 'S', 'i', 'x', 1044, 1045, 2430, 2431}, true
	case 1043:
		return pgTypeEntry{1043, "varchar", -1, false, 'b', 'S', 'i', 'x', 1046, 1047, 2432, 2433}, true
	case 1184:
		return pgTypeEntry{1184, "timestamptz", 8, true, 'b', 'D', 'd', 'p', 1150, 1151, 2476, 2477}, true
	case 1185:
		return pgTypeEntry{1185, "_timestamptz", -1, false, 'b', 'A', 'd', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1186:
		// interval: 16-byte fixed time interval; typcategory='T', typalign='d'.
		// interval_in=1160, interval_out=1161, interval_recv=2478, interval_send=2479.
		return pgTypeEntry{1186, "interval", 16, false, 'b', 'T', 'd', 'p', 1160, 1161, 2478, 2479}, true
	case 1700:
		// numeric: variable-length arbitrary precision; typcategory='N',
		// typstorage='m' (main — never toasted out of line by choice).
		// numeric_in=1701, numeric_out=1702, numeric_recv=2460,
		// numeric_send=2461 (pg_type.dat:348-354).
		return pgTypeEntry{1700, "numeric", -1, false, 'b', 'N', 'i', 'm', 1701, 1702, 2460, 2461}, true
	case 2277:
		return pgTypeEntry{2277, "anyarray", -1, false, 'p', 'P', 'i', 'x', 2296, 2297, 2502, 2503}, true
	case 2281:
		// pseudo type: no recv/send.
		return pgTypeEntry{2281, "internal", 4, true, 'p', 'P', 'i', 'p', 2304, 2305, 0, 0}, true
	case 3220:
		return pgTypeEntry{3220, "pg_lsn", 8, true, 'b', 'U', 'd', 'p', 3229, 3230, 3238, 3239}, true
	case 3361:
		return pgTypeEntry{3361, "pg_ndistinct", -1, false, 'b', 'Z', 'i', 'x', 3355, 3356, 3357, 3358}, true
	case 3402:
		return pgTypeEntry{3402, "pg_dependencies", -1, false, 'b', 'Z', 'i', 'x', 3404, 3405, 3406, 3407}, true
	case 5017:
		return pgTypeEntry{5017, "pg_mcv_list", -1, false, 'b', 'Z', 'i', 'x', 5018, 5019, 5020, 5021}, true
	case 10028:
		// rowtype array for pg_statistic. Uses the generic array I/O quad
		// like every other array type.
		//
		// M0131-S9.3g corrected typtype 'c' → 'b'. The original comment here
		// said "typtype='c' carries no special meaning for the standby's
		// TupleDescInitEntry path; the load-bearing fields are typalign='d' +
		// typstorage='x'" — true of that path, false of every other. An ARRAY
		// of a composite is a BASE type upstream ('b', verified against a
		// fresh PG 18.3's own pg_type), and PG treats typtype='c' as a promise
		// that typrelid is valid: insert_rel_type_cache_if_needed() asserts
		// exactly that (typcache.c:3082). goopg's row promised a composite
		// with typrelid = 0, so the first planner path that type-cached 10028
		// — add_paths_to_joinrel on pg_stats_ext_exprs — killed the backend.
		return pgTypeEntry{10028, "_pg_statistic", -1, false, 'b', 'A', 'd', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 10029:
		// M0131-S9.3g: pg_statistic's own COMPOSITE rowtype — the element
		// type 10028 above has pointed its typelem here since S9.3c, but no
		// row described it, so a hosted PG evaluating pg_stats_ext_exprs
		// (whose `unnest(sd.stxdexpr)` yields pg_statistic values) died with
		// `type with OID 10029 does not exist` and then tripped
		// Assert("OidIsValid(typentry->typrelid)") (typcache.c:3082) during
		// abort. Values verbatim from a fresh PG 18.3's pg_type: composite
		// rowtypes carry the record I/O quad (record_in/out/recv/send =
		// 2290/2291/2402/2403), typcategory 'C', typalign 'd',
		// typstorage 'x', typrelid = 2619 (pgTypeRelidOverlay) and typarray =
		// 10028 (pgTypeElemArrayOverlay). The OID is initdb-assigned upstream,
		// not a BKI_ROWTYPE_OID — pg_statistic.h declares none — so it is
		// pinned here for the same reason S8a pins the view OIDs: goopg
		// adopts upstream's assignment rather than minting its own.
		return pgTypeEntry{10029, "pg_statistic", -1, false, 'c', 'C', 'd', 'x', 2290, 2291, 2402, 2403}, true
	// ---- information_schema domains + array peers (M0133-S1) ----
	//
	// These OIDs are initdb-assigned by information_schema.sql AFTER bootstrap,
	// so they come from the same post-bootstrap counter that M0131-S9 pinned for
	// the 80 system_views.sql views (FirstUnpinnedObjectId = 12000). They are
	// NOT in pg_type.dat; every value below was measured against a fresh PG 18.3
	// (M0133-S1's oracle recipe), not read from a .dat file. The five domains
	// carry PG's domain I/O pair (domain_in = 2597, domain_recv = 2598) as
	// typinput/typreceive and their BASE type's output/send (a domain renders
	// through its base's typoutput — getTypeOutputInfo reads the DOMAIN row).
	// typtype/typcategory/typalign/typstorage follow the base type; the array
	// peers are the generic array I/O quad with typalign inherited from their
	// element (Catalog.pm:469 — 'd' only when the element is 'd', hence
	// _time_stamp's 'd').
	case 13286:
		return pgTypeEntry{13286, "_cardinal_number", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 13287:
		return pgTypeEntry{13287, "cardinal_number", 4, true, 'd', 'N', 'i', 'p', 2597, 43, 2598, 2407}, true
	case 13289:
		return pgTypeEntry{13289, "_character_data", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 13290:
		return pgTypeEntry{13290, "character_data", -1, false, 'd', 'S', 'i', 'x', 2597, 1047, 2598, 2433}, true
	case 13291:
		return pgTypeEntry{13291, "_sql_identifier", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 13292:
		return pgTypeEntry{13292, "sql_identifier", 64, false, 'd', 'S', 'c', 'p', 2597, 35, 2598, 2423}, true
	case 13297:
		return pgTypeEntry{13297, "_time_stamp", -1, false, 'b', 'A', 'd', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 13298:
		return pgTypeEntry{13298, "time_stamp", 8, true, 'd', 'D', 'd', 'p', 2597, 1151, 2598, 2477}, true
	case 13299:
		return pgTypeEntry{13299, "_yes_or_no", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 13300:
		return pgTypeEntry{13300, "yes_or_no", -1, false, 'd', 'S', 'i', 'x', 2597, 1047, 2598, 2433}, true
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

// pgTypeCollationForOID returns the PG18-canonical pg_type.typcollation for a
// built-in type OID, sourced from `src/include/catalog/pg_type.dat`. It MUST
// agree with the attcollation that the runtime virtual pg_attribute path reports
// for the same type (executor.userTypeAttrsForOID.TypCollation): pg_dump's
// getTableAttrs emits a column COLLATE clause precisely when
// `a.attcollation <> t.typcollation`, so any divergence makes pg_dump spuriously
// emit (or drop) `COLLATE pg_catalog."default"` on every collatable column. The
// heap previously hardcoded 0 here while pg_attribute reported 100, which —
// once pg_collation was populated so findCollationByOid(100) resolved (DU-002
// slice 187) — surfaced as a spurious `COLLATE pg_catalog."default"` on every
// text/varchar/bpchar column in pg_dump output. DU-002 slice 188.
//
// DU-002 slice 189 extends the same fix to the ARRAY types of the collatable
// scalars: a PG array inherits its element's typcollation, so _name (1003) is
// 'C' (950) and _bpchar (1014) / _varchar (1015) are 'default' (100), exactly as
// userTypeAttrsForOID reports for those array OIDs. Without these rows the heap
// reported 0 while pg_attribute reported 100/950, so a `varchar[]`/`bpchar[]`/
// `name[]` column round-tripped through pg_dump with a spurious COLLATE clause.
//
// Only collatable built-in types present in the nailed pg_type heap are listed;
// every other type is non-collatable (InvalidOid/0). default=100, C=950 match
// pg_collation_d.h DEFAULT_COLLATION_OID / C_COLLATION_OID.
func pgTypeCollationForOID(oid uint32) int64 {
	switch oid {
	case 19: // name      -- typcollation => 'C'
		return 950
	case 25: // text      -- typcollation => 'default'
		return 100
	case 1042: // bpchar  -- typcollation => 'default'
		return 100
	case 1043: // varchar -- typcollation => 'default'
		return 100
	case 1009: // _text   -- text array inherits the element's 'default' collation
		return 100
	case 1003: // _name   -- name array inherits the element's 'C' collation
		return 950
	case 1014: // _bpchar -- bpchar array inherits the element's 'default' collation
		return 100
	case 1015: // _varchar -- varchar array inherits the element's 'default' collation
		return 100
	// information_schema domains declared COLLATE "C" and their array peers
	// (M0133-S1): character_data, sql_identifier and yes_or_no are collatable,
	// and an array inherits its element's collation. cardinal_number and
	// time_stamp are not collatable (0, the default).
	case 13289, 13290: // _character_data, character_data
		return 950
	case 13291, 13292: // _sql_identifier, sql_identifier
		return 950
	case 13299, 13300: // _yes_or_no, yes_or_no
		return 950
	default:
		return 0
	}
}

// pgTypeElemArrayOverlay carries the {typelem, typarray, typsubscript} triple
// for the OIDs that pgTypeCanonical() supplies but pg_type.dat does not — the
// goopg-specific catalog rowtype arrays. Everything else comes from
// pgTypeGeneratedElemArraySubscript (generated by cmd/gen-pg-type-data straight
// out of pg_type.dat); TestPgTypeElemArrayCoversSeededEntries keeps the union
// exhaustive over the bootstrap entry set.
var pgTypeElemArrayOverlay = map[uint32][3]uint32{
	// _pg_statistic: element is the pg_statistic composite rowtype, which
	// initdb assigns 10029 in the same catalog order upstream's genbki does.
	10028: {10029, 0, 6179},
	// pg_statistic's rowtype (M0131-S9.3g): the reverse edge of the row
	// above — typelem 0 (a composite is not an array), typarray 10028,
	// typsubscript 0. Both directions verified against a fresh PG 18.3.
	10029: {0, 10028, 0},
	// information_schema domains + array peers (M0133-S1). Array peers point
	// typelem at their domain and carry array_subscript_handler (6179); the
	// domains point typarray back at the peer. All measured against a fresh
	// PG 18.3, not read from pg_type.dat (these OIDs are initdb-assigned by
	// information_schema.sql).
	13286: {13287, 0, 6179}, // _cardinal_number
	13287: {0, 13286, 0},    // cardinal_number
	13289: {13290, 0, 6179}, // _character_data
	13290: {0, 13289, 0},    // character_data
	13291: {13292, 0, 6179}, // _sql_identifier
	13292: {0, 13291, 0},    // sql_identifier
	13297: {13298, 0, 6179}, // _time_stamp
	13298: {0, 13297, 0},    // time_stamp
	13299: {13300, 0, 6179}, // _yes_or_no
	13300: {0, 13299, 0},    // yes_or_no
}

// pgTypeRelidOverlay carries typrelid for the bootstrapped pg_type rows that
// describe a COMPOSITE rowtype of an on-disk catalog. Every other row goopg
// seeds is a base/pseudo/array type, whose typrelid is legitimately 0 — which
// is why pgTypeRow hardcoded the column until M0131-S9.3g.
//
// typrelid is not decorative here: PG's lookup_type_cache() asserts
// `OidIsValid(typentry->typrelid)` for a typtype='c' entry (typcache.c:3082)
// and dereferences it to build the record tupledesc, so a composite row with
// typrelid = 0 is worse than no row at all — it crashes the backend instead of
// raising an error.
// The four BKI_ROWTYPE_OID rowtypes below come out of pg_type.dat via
// cmd/gen-pg-type-data and had carried typrelid = 0 since M0106 — a latent
// instance of the same crash, waiting for any query that type-caches
// pg_class/pg_type/pg_proc/pg_attribute as a VALUE rather than reading the
// catalog. TestPgTypeCompositeRowsCarryTyprelid keeps the two halves paired.
var pgTypeRelidOverlay = map[uint32]uint32{
	71:    1247, // pg_type
	75:    1249, // pg_attribute
	81:    1255, // pg_proc
	83:    1259, // pg_class
	10029: 2619, // pg_statistic — nailedLocalRels{2619}.RelType must agree
}

// pgTypeNamespaceOverlay carries typnamespace for the bootstrapped pg_type rows
// that live OUTSIDE pg_catalog. Every other seeded row is pg_catalog (11),
// which pgTypeRow still hardcodes; the information_schema domains and their
// array peers (M0133-S1) are the first rows in namespace 13273.
var pgTypeNamespaceOverlay = map[uint32]uint32{
	13286: 13273, // _cardinal_number
	13287: 13273, // cardinal_number
	13289: 13273, // _character_data
	13290: 13273, // character_data
	13291: 13273, // _sql_identifier
	13292: 13273, // sql_identifier
	13297: 13273, // _time_stamp
	13298: 13273, // time_stamp
	13299: 13273, // _yes_or_no
	13300: 13273, // yes_or_no
}

// pgTypeBaseTypeOverlay carries typbasetype for the information_schema DOMAIN
// rows (M0133-S1). Every other row is a base/pseudo/array/composite type whose
// typbasetype is legitimately 0 (still hardcoded in pgTypeRow). A domain's
// typbasetype is the OID of the base type it is declared over.
var pgTypeBaseTypeOverlay = map[uint32]uint32{
	13287: 23,   // cardinal_number over int4
	13290: 1043, // character_data over varchar
	13292: 19,   // sql_identifier over name
	13298: 1184, // time_stamp over timestamptz
	13300: 1043, // yes_or_no over varchar(3)
}

// pgTypeTypModOverlay carries typtypmod for the information_schema domains whose
// base type carries a length/precision (M0133-S1). Every other row keeps
// pgTypeRow's hardcoded -1 (default typmod). time_stamp is timestamp(2),
// yes_or_no is varchar(3) → VARHDRSZ + 3 = 7.
var pgTypeTypModOverlay = map[uint32]int64{
	13298: 2, // time_stamp = timestamp(2) with time zone
	13300: 7, // yes_or_no = varchar(3)
}

// pgTypeElemArraySubscriptForOID returns the PG18-canonical
// {typelem, typarray, typsubscript} triple for a bootstrapped pg_type row.
//
// All three columns were hardcoded 0 for every row until M0131-S9.3c, and that
// one defect produced three distinct hosted-PG failures:
//
//   - typarray = 0 makes get_array_type() fail, so a view whose target list
//     holds an `ARRAY(SELECT …)` dies with `could not find array type for data
//     type oid` (pg_group's grolist, ceiling #3).
//   - typelem = 0 makes get_element_type() fail, so ExecInitExprRec's
//     T_ArrayCoerceExpr arm (execExpr.c:1684-1688) dies with `target type is
//     not an array` (pg_publication_tables, ceiling #5).
//   - typsubscript = 0 makes IsTrueArrayType() false even when typelem is
//     right, so `x = ANY(ARRAY[…])` — which is what the parser builds for any
//     `IN (…)` list once typarray resolves — dies with `op ANY/ALL (array)
//     requires array on right side` in make_scalar_array_op
//     (parse_oper.c:800). Populating typelem/typarray WITHOUT typsubscript is
//     therefore a regression, not a partial fix: it moves IN-lists off the
//     OR-chain fallback and onto a ScalarArrayOpExpr that then cannot resolve.
//
// Unknown OIDs report 0/0/0, which is what every row carried before; the
// coverage guard keeps that fallback unreachable for the bootstrap set.
func pgTypeElemArraySubscriptForOID(oid uint32) (typelem, typarray, typsubscript int64) {
	t, ok := pgTypeGeneratedElemArraySubscript[oid]
	if !ok {
		t, ok = pgTypeElemArrayOverlay[oid]
	}
	if !ok {
		return 0, 0, 0
	}
	return int64(t[0]), int64(t[1]), int64(t[2])
}

// pgTypeRow encodes one pgTypeEntry into a 32-column executor.Row matching
// pgTypeColDefs(). All optional regproc fields and the three CATALOG_VARLEN
// columns are zero/NULL — only the fixed fields PG18 actually reads on the
// early-boot TupleDescInitEntry path are populated.
func pgTypeRow(e pgTypeEntry) executor.Row {
	typelem, typarray, typsubscript := pgTypeElemArraySubscriptForOID(e.OID)
	// Overlays (M0133-S1): the information_schema domains live in namespace
	// 13273 and carry a real typbasetype/typtypmod; every other seeded row is
	// pg_catalog (11) with typbasetype 0 and the default -1 typmod.
	typnamespace := uint32(11)
	if v, ok := pgTypeNamespaceOverlay[e.OID]; ok {
		typnamespace = v
	}
	typbasetype := uint32(0)
	if v, ok := pgTypeBaseTypeOverlay[e.OID]; ok {
		typbasetype = v
	}
	typtypmod := int64(-1)
	if v, ok := pgTypeTypModOverlay[e.OID]; ok {
		typtypmod = v
	}
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),                 // 1 oid
		executor.NewStringDatum(e.Name),                    // 2 typname
		executor.NewIntDatum(int64(typnamespace)),          // 3 typnamespace
		executor.NewIntDatum(10),                           // 4 typowner = BOOTSTRAP_SUPERUSERID
		executor.NewIntDatum(int64(e.Len)),                 // 5 typlen
		executor.NewBoolDatum(e.ByVal),                     // 6 typbyval
		executor.NewStringDatum(string(e.Type)),            // 7 typtype
		executor.NewStringDatum(string(e.Category)),        // 8 typcategory
		executor.NewBoolDatum(false),                       // 9 typispreferred
		executor.NewBoolDatum(true),                        // 10 typisdefined
		executor.NewStringDatum(","),                       // 11 typdelim
		executor.NewIntDatum(int64(pgTypeRelidOverlay[e.OID])), // 12 typrelid (M0131-S9.3g; 0 for every non-composite)
		executor.NewIntDatum(typsubscript),                 // 13 typsubscript (M0131-S9.3c)
		executor.NewIntDatum(typelem),                      // 14 typelem  (M0131-S9.3c)
		executor.NewIntDatum(typarray),                     // 15 typarray (M0131-S9.3c)
		executor.NewIntDatum(int64(e.Input)),               // 16 typinput
		executor.NewIntDatum(int64(e.Output)),              // 17 typoutput
		executor.NewIntDatum(int64(e.Receive)),             // 18 typreceive
		executor.NewIntDatum(int64(e.Send)),                // 19 typsend
		executor.NewIntDatum(0),                            // 20 typmodin
		executor.NewIntDatum(0),                            // 21 typmodout
		executor.NewIntDatum(0),                            // 22 typanalyze
		executor.NewStringDatum(string(e.Align)),           // 23 typalign  ← load-bearing
		executor.NewStringDatum(string(e.Storage)),         // 24 typstorage
		executor.NewBoolDatum(false),                       // 25 typnotnull
		executor.NewIntDatum(int64(typbasetype)),           // 26 typbasetype
		executor.NewIntDatum(typtypmod),                    // 27 typtypmod
		executor.NewIntDatum(0),                            // 28 typndims
		executor.NewIntDatum(pgTypeCollationForOID(e.OID)), // 29 typcollation (PG-canonical; must match attcollation, slice 188)
		executor.NullDatum,                                 // 30 typdefaultbin
		executor.NullDatum,                                 // 31 typdefault
		executor.NullDatum,                                 // 32 typacl
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
// `pg_internal.init` in the data directory (the E2E `copyInitFiles`
// workaround that used to seed them was deleted in M0131-S10 for
// exactly this reason). So every backend
// rebuilds tupledesc from the heap; `TupleDescInitEntry`
// (tupdesc.c:902) reads `typeForm->typalign` via SysCache lookup of
// pg_type by OID. If that byte isn't one of 'c'/'s'/'i'/'d', the next
// `populate_compact_attribute_internal` call FATALs at tupdesc.c:105
// (`invalid attalign value:`). Writing the canonical heap here makes
// the SysCache lookup return a proper Form_pg_type pointer with
// typalign at offset 128.
// pgTypeBootstrapEntryMap builds the combined bootstrap entry set: all PG18
// base types + array peers from pgTypeAllEntries() (generated from
// pg_type.dat), plus any nailed-attr OIDs that fall back to pgTypeCanonical()
// (e.g. goopg-specific OIDs like 10028 _pg_statistic that are not in
// pg_type.dat). Shared by the heap writer and both index bootstrappers so the
// heap rows and index entries always cover the identical OID set.
func pgTypeBootstrapEntryMap() map[uint32]pgTypeEntry {
	allMap := make(map[uint32]pgTypeEntry)
	for _, e := range pgTypeAllEntries() {
		allMap[e.OID] = e
	}
	for _, oid := range pgTypeOIDsUsedByNailedAttrs() {
		if _, alreadyIn := allMap[oid]; !alreadyIn {
			if e, ok := pgTypeCanonical(oid); ok {
				allMap[e.OID] = e
			}
		}
	}
	// M0131-S9.3g: a nailed rel's ROWTYPE needs a pg_type row too, not just
	// its columns' types. The loop above walks nailedAttr.TypeOID, which
	// never covers nailedRel.RelType — that is how pg_class 2619 could point
	// at a pg_type row (10029) nothing wrote. Deriving the set from RelType
	// instead of listing 10029 by hand keeps the two edits from drifting: the
	// next catalog that swaps its placeholder RelType for a real rowtype gets
	// its heap row automatically. Rels still carrying the historical
	// placeholder (83, pg_class's own rowtype) resolve out of pg_type.dat and
	// are already in allMap, so this adds nothing for them.
	for _, rel := range append(append([]nailedRel{}, nailedSharedRels...), nailedLocalRels...) {
		if rel.RelType == 0 {
			continue
		}
		if _, alreadyIn := allMap[rel.RelType]; alreadyIn {
			continue
		}
		if e, ok := pgTypeCanonical(rel.RelType); ok {
			allMap[e.OID] = e
		}
	}
	// M0133-S1: the information_schema domains + their array peers are seeded
	// into the heap but referenced by NO nailed attr (they are types of the
	// not-yet-on-disk information_schema views), so the loops above miss them.
	// They must be in the entry map so the heap writer and both index
	// bootstrappers cover them; absent this, a hosted PG dies with
	// `type with OID 13287 does not exist` the first time a domain resolves.
	for _, oid := range pgTypeInformationSchemaDomainOIDs() {
		if _, alreadyIn := allMap[oid]; alreadyIn {
			continue
		}
		if e, ok := pgTypeCanonical(oid); ok {
			allMap[e.OID] = e
		}
	}
	return allMap
}

// pgTypeInformationSchemaDomainOIDs returns the ten initdb-assigned pg_type
// OIDs of the information_schema domains and their array peers (M0133-S1),
// measured against a fresh PG 18.3 (information_schema.sql runs after
// bootstrap, so these come from the post-bootstrap OID counter, not
// pg_type.dat). Kept as one list so pgTypeBootstrapEntryMap and any guard over
// the bootstrap set share the same source.
func pgTypeInformationSchemaDomainOIDs() []uint32 {
	return []uint32{
		13286, 13287, // _cardinal_number, cardinal_number
		13289, 13290, // _character_data, character_data
		13291, 13292, // _sql_identifier, sql_identifier
		13297, 13298, // _time_stamp, time_stamp
		13299, 13300, // _yes_or_no, yes_or_no
	}
}

func bootstrapPgTypeTuples(dataDir string) (map[uint32]heapTID, error) {
	cols := pgTypeColDefs()

	allMap := pgTypeBootstrapEntryMap()

	// Sort by OID for deterministic heap layout.
	oids := make([]uint32, 0, len(allMap))
	for oid := range allMap {
		oids = append(oids, oid)
	}
	sort.Slice(oids, func(i, j int) bool { return oids[i] < oids[j] })

	rows := make([]executor.Row, 0, len(oids))
	for _, oid := range oids {
		rows = append(rows, pgTypeRow(allMap[oid]))
	}
	rawTIDs, err := writeMultiPageHeapRows(dataDir, "1247", cols, rows)
	if err != nil {
		return nil, err
	}
	tidMap := make(map[uint32]heapTID, len(oids))
	for i, oid := range oids {
		tidMap[oid] = rawTIDs[i]
	}
	return tidMap, nil
}
