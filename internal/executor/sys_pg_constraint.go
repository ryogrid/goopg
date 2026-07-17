package executor

// B2.1b (docs/design/wal-pg-identical-stream/02d §1): domain CHECK
// constraints journal as real pg_constraint heap rows (contype='c',
// contypid=<domain OID>) so the startup reload reconstructs them from the
// heap instead of the retired CreateDomain WAL record (kind 119). The
// pg_constraint VIEW stays virtual (registry-backed) — this heap is a
// write+reload surface only, exactly like pg_class (see
// goopg_pg_class_virtual_pg_attribute_heap).
//
// Scope notes (ledgered residuals):
//   - No pg_constraint index maintenance (2664-2667 stay bootstrap-empty):
//     a real PG standby loads domain constraints via
//     pg_constraint_contypid_index (2666) and so sees NONE — it accepts
//     values a goopg primary would reject. Rides the full pg_constraint
//     conversion (B3).
//   - conbin carries the raw CHECK expression text, the same deviation
//     convention as pg_type.typdefaultbin / pg_attrdef.adbin.
//   - Table constraints stay registry-only until B3.

import (
	"encoding/binary"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgConstraintRelOID is pg_constraint's relation OID.
const pgConstraintRelOID = 2606

// PGTypeColumnsPG18 exports the 32-column pg_type layout for the initdb
// reload descriptor (B2.1b — twin of pgTypeColumnsPG18, which stays
// unexported beside its builders in pg18_user_catalog_rows.go).
func PGTypeColumnsPG18() []catalog.Column { return pgTypeColumnsPG18() }

// PGConstraintColumnsPG18 mirrors FormData_pg_constraint
// (postgres/src/include/catalog/pg_constraint.h): the 28-column PG18 layout.
// Exported for the initdb reload descriptor.
func PGConstraintColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "conname", Type: catalog.Type{Name: "name"}},
		{Name: "connamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "contype", Type: catalog.Type{Name: "char"}},
		{Name: "condeferrable", Type: catalog.Type{Name: "bool"}},
		{Name: "condeferred", Type: catalog.Type{Name: "bool"}},
		{Name: "conenforced", Type: catalog.Type{Name: "bool"}},
		{Name: "convalidated", Type: catalog.Type{Name: "bool"}},
		{Name: "conrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "contypid", Type: catalog.Type{Name: "oid"}},
		{Name: "conindid", Type: catalog.Type{Name: "oid"}},
		{Name: "conparentid", Type: catalog.Type{Name: "oid"}},
		{Name: "confrelid", Type: catalog.Type{Name: "oid"}},
		{Name: "confupdtype", Type: catalog.Type{Name: "char"}},
		{Name: "confdeltype", Type: catalog.Type{Name: "char"}},
		{Name: "confmatchtype", Type: catalog.Type{Name: "char"}},
		{Name: "conislocal", Type: catalog.Type{Name: "bool"}},
		{Name: "coninhcount", Type: catalog.Type{Name: "int2"}},
		{Name: "connoinherit", Type: catalog.Type{Name: "bool"}},
		{Name: "conperiod", Type: catalog.Type{Name: "bool"}},
		{Name: "conkey", Type: catalog.Type{Name: "int2[]"}},
		{Name: "confkey", Type: catalog.Type{Name: "int2[]"}},
		{Name: "conpfeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conppeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conffeqop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "confdelsetcols", Type: catalog.Type{Name: "int2[]"}},
		{Name: "conexclop", Type: catalog.Type{Name: "oid[]"}},
		{Name: "conbin", Type: catalog.Type{Name: "pg_node_tree"}},
	}
}

// buildPGConstraintRowForDomainCheck builds the pg_constraint row for one
// domain CHECK constraint. Value semantics mirror PG's CreateConstraintEntry
// for a validated, enforced, local domain check: FK-only char columns carry
// the zero char, array columns are genuinely NULL.
func buildPGConstraintRowForDomainCheck(d *catalog.Domain, chk catalog.DomainCheck) Row {
	return Row{
		NewIntDatum(int64(chk.OID)),                    // 1  oid
		NewStringDatum(chk.Name),                       // 2  conname
		NewIntDatum(int64(catalog.PublicNamespaceOID)), // 3  connamespace
		NewStringDatum("c"),                            // 4  contype
		NewBoolDatum(false),                            // 5  condeferrable
		NewBoolDatum(false),                            // 6  condeferred
		NewBoolDatum(true),                             // 7  conenforced
		NewBoolDatum(true),                             // 8  convalidated
		NewIntDatum(0),                                 // 9  conrelid
		NewIntDatum(int64(d.OID)),                      // 10 contypid
		NewIntDatum(0),                                 // 11 conindid
		NewIntDatum(0),                                 // 12 conparentid
		NewIntDatum(0),                                 // 13 confrelid
		NewStringDatum(""),                             // 14 confupdtype (zero char, non-FK)
		NewStringDatum(""),                             // 15 confdeltype
		NewStringDatum(""),                             // 16 confmatchtype
		NewBoolDatum(true),                             // 17 conislocal
		NewIntDatum(0),                                 // 18 coninhcount
		NewBoolDatum(false),                            // 19 connoinherit
		NewBoolDatum(false),                            // 20 conperiod
		NullDatum,                                      // 21 conkey
		NullDatum,                                      // 22 confkey
		NullDatum,                                      // 23 conpfeqop
		NullDatum,                                      // 24 conppeqop
		NullDatum,                                      // 25 conffeqop
		NullDatum,                                      // 26 confdelsetcols
		NullDatum,                                      // 27 conexclop
		NewStringDatum(chk.Expr),                       // 28 conbin (raw expr text — adbin convention)
	}
}

func pgConstraintRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: pgConstraintRelOID,
		Fork:   storage.MainFork,
	}
}

// writeDomainCheckConstraintRow journals one domain CHECK as a pg_constraint
// heap INSERT (XLOG_HEAP_INSERT).
func writeDomainCheckConstraintRow(ctx *Context, d *catalog.Domain, chk catalog.DomainCheck) error {
	_, err := writeHeapRowCanonical(ctx, pgConstraintRel(ctx), PGConstraintColumnsPG18(),
		buildPGConstraintRowForDomainCheck(d, chk))
	return err
}

// deleteConstraintRowByOID stamps xmax on the pg_constraint row whose oid
// column matches (DROP CONSTRAINT / DROP DOMAIN / pre-update delete).
func deleteConstraintRowByOID(ctx *Context, conOID uint32, xmax storage.TransactionID) {
	stampCatalogRows(ctx, pgConstraintRel(ctx), xmax, func(data []byte) bool {
		if len(data) < 4 {
			return false
		}
		return binary.LittleEndian.Uint32(data[0:4]) == conOID
	})
}

// mirrorConstraintCatalogFiles propagates the pg_constraint heap to the
// postgres DB's copy (reload reads base/5 — doc 02a §2.2 BLOCKER-3).
func mirrorConstraintCatalogFiles(ctx *Context) {
	_ = mirrorCatalogRelToPostgresDB(ctx, pgConstraintRelOID)
}

// pgTypeIOProcsForOID returns a base type's (typinput, typoutput,
// typreceive, typsend) pg_proc OIDs, sourced from pg_type.dat. PG's
// DefineDomain copies the base's I/O procs into the domain's own pg_type
// row and getTypeOutputInfo reads the DOMAIN row directly, so a zero
// typoutput makes every domain-value render on a real PG standby fail with
// "no output function available". Covers the domain-supported base types;
// unknown OIDs fall back to text's procs (matching TypeNameToOID's own
// text fallback). B2.1b.
func pgTypeIOProcsForOID(oid uint32) (in, out, recv, send int64) {
	switch oid {
	case 16: // bool
		return 1242, 1243, 2436, 2437
	case 17: // bytea
		return 1244, 31, 2412, 2413
	case 20: // int8
		return 460, 461, 2408, 2409
	case 21: // int2
		return 38, 39, 2404, 2405
	case 23: // int4
		return 42, 43, 2406, 2407
	case 700: // float4
		return 200, 201, 2424, 2425
	case 701: // float8
		return 214, 215, 2426, 2427
	case 869: // inet
		return 910, 911, 2420, 2421
	case 1042: // bpchar
		return 1044, 1045, 2430, 2431
	case 1043: // varchar
		return 1046, 1047, 2432, 2433
	case 1082: // date
		return 1084, 1085, 2468, 2469
	case 1083: // time
		return 1143, 1144, 2470, 2471
	case 1114: // timestamp
		return 1312, 1313, 2474, 2475
	case 1184: // timestamptz
		return 1150, 1151, 2476, 2477
	case 1560: // bit
		return 1564, 1565, 2456, 2457
	case 1562: // varbit
		return 1579, 1580, 2458, 2459
	case 1700: // numeric
		return 1701, 1702, 2460, 2461
	case 2950: // uuid
		return 2952, 2953, 2961, 2962
	default: // text and anything unregistered
		return 46, 47, 2414, 2415
	}
}
