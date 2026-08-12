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
	case 1002:
		return pgTypeEntry{1002, "_char", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1003:
		// _name: the array of name (19). M0131-S9.2d — pg_policies.roles is
		// `name[]`, the first nailed-view column of this type, and capture
		// guard #5 refused the view until the canonical table carried it.
		// typalign is 'i', NOT name's own 'c': Catalog.pm:469 gives an array
		// type 'd' when its element is 'd' and 'i' otherwise.
		return pgTypeEntry{1003, "_name", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
	case 1021:
		return pgTypeEntry{1021, "_float4", -1, false, 'b', 'A', 'i', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
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
		// rowtype array for pg_statistic — typtype='c' carries no special
		// meaning for the standby's TupleDescInitEntry path; the load-
		// bearing fields are typalign='d' + typstorage='x'. Uses the
		// generic array I/O quad like every other multi-dim array.
		return pgTypeEntry{10028, "_pg_statistic", -1, false, 'c', 'A', 'd', 'x', arrayIn, arrayOut, arrayRecv, arraySend}, true
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
	return executor.Row{
		executor.NewIntDatum(int64(e.OID)),                 // 1 oid
		executor.NewStringDatum(e.Name),                    // 2 typname
		executor.NewIntDatum(11),                           // 3 typnamespace = pg_catalog
		executor.NewIntDatum(10),                           // 4 typowner = BOOTSTRAP_SUPERUSERID
		executor.NewIntDatum(int64(e.Len)),                 // 5 typlen
		executor.NewBoolDatum(e.ByVal),                     // 6 typbyval
		executor.NewStringDatum(string(e.Type)),            // 7 typtype
		executor.NewStringDatum(string(e.Category)),        // 8 typcategory
		executor.NewBoolDatum(false),                       // 9 typispreferred
		executor.NewBoolDatum(true),                        // 10 typisdefined
		executor.NewStringDatum(","),                       // 11 typdelim
		executor.NewIntDatum(0),                            // 12 typrelid
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
		executor.NewIntDatum(0),                            // 26 typbasetype
		executor.NewIntDatum(-1),                           // 27 typtypmod
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
	return allMap
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
