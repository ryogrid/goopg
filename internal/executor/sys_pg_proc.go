package executor

// B1.2 (docs/design/wal-pg-identical-stream/02c §2): pg_proc heap journaling.
// Function/procedure DDL writes real pg_proc heap rows — the WAL stream
// carries ordinary XLOG_HEAP_* records like PostgreSQL — replacing the seven
// bespoke function RecordKinds (61-64, 121-123). The routines registry stays
// the write-through cache and carries each routine's live heap TID.
//
// Index maintenance for pg_proc_oid_index(2690) / pg_proc_proname_args_nsp_
// index(2691) runs at every heap write (B2-prep): both bootstrap trees are
// multi-level (3397 entries), so entries ride the descent path in
// sys_catalog_btree_multilevel.go; 2691 is the first variable-length key
// (proargtypes oidvector). Non-HOT updates insert fresh entries at the new
// TID — old entries point at dead heap versions, exactly PG's convention
// (reaped by vacuum, never updated in place).

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/storage"
)

// pgProcRelOID is pg_proc's relation OID (catalog.ProcedureRelationId).
const pgProcRelOID = 1255

// PGProcColumnsPG18 mirrors initdb's pgProcColDefs (FormData_pg_proc,
// postgres/src/include/catalog/pg_proc.h): the 30-column PG18 layout.
// Exported for the initdb reload descriptor.
func PGProcColumnsPG18() []catalog.Column {
	return []catalog.Column{
		{Name: "oid", Type: catalog.Type{Name: "oid"}},
		{Name: "proname", Type: catalog.Type{Name: "name"}},
		{Name: "pronamespace", Type: catalog.Type{Name: "oid"}},
		{Name: "proowner", Type: catalog.Type{Name: "oid"}},
		{Name: "prolang", Type: catalog.Type{Name: "oid"}},
		{Name: "procost", Type: catalog.Type{Name: "float4"}},
		{Name: "prorows", Type: catalog.Type{Name: "float4"}},
		{Name: "provariadic", Type: catalog.Type{Name: "oid"}},
		{Name: "prosupport", Type: catalog.Type{Name: "regproc"}},
		{Name: "prokind", Type: catalog.Type{Name: "char"}},
		{Name: "prosecdef", Type: catalog.Type{Name: "bool"}},
		{Name: "proleakproof", Type: catalog.Type{Name: "bool"}},
		{Name: "proisstrict", Type: catalog.Type{Name: "bool"}},
		{Name: "proretset", Type: catalog.Type{Name: "bool"}},
		{Name: "provolatile", Type: catalog.Type{Name: "char"}},
		{Name: "proparallel", Type: catalog.Type{Name: "char"}},
		{Name: "pronargs", Type: catalog.Type{Name: "int2"}},
		{Name: "pronargdefaults", Type: catalog.Type{Name: "int2"}},
		{Name: "prorettype", Type: catalog.Type{Name: "oid"}},
		{Name: "proargtypes", Type: catalog.Type{Name: "oidvector"}},
		{Name: "proallargtypes", Type: catalog.Type{Name: "oid[]"}},
		{Name: "proargmodes", Type: catalog.Type{Name: "char[]"}},
		{Name: "proargnames", Type: catalog.Type{Name: "text[]"}},
		{Name: "proargdefaults", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "protrftypes", Type: catalog.Type{Name: "oid[]"}},
		{Name: "prosrc", Type: catalog.Type{Name: "text"}},
		{Name: "probin", Type: catalog.Type{Name: "text"}},
		{Name: "prosqlbody", Type: catalog.Type{Name: "pg_node_tree"}},
		{Name: "proconfig", Type: catalog.Type{Name: "text[]"}},
		{Name: "proacl", Type: catalog.Type{Name: "aclitem[]"}},
	}
}

// pgProcOidVectorBytes is the executor twin of initdb's oidVectorBytes
// (initdb.go:2428): a 24-byte 1-D no-null ArrayType header (elemtype
// OID=26), vl_len_ = total<<2, then n uint32 elements. Twins must agree
// byte-for-byte — the reload decodes with the shared PG-tuple decoder.
func pgProcOidVectorBytes(oids []uint32) []byte {
	const headerSize = 24
	total := headerSize + 4*len(oids)
	buf := make([]byte, total)
	binary.LittleEndian.PutUint32(buf[0:4], uint32(total)<<2)
	binary.LittleEndian.PutUint32(buf[4:8], 1)
	binary.LittleEndian.PutUint32(buf[8:12], 0)
	binary.LittleEndian.PutUint32(buf[12:16], 26)
	binary.LittleEndian.PutUint32(buf[16:20], uint32(len(oids)))
	binary.LittleEndian.PutUint32(buf[20:24], 0)
	for i, o := range oids {
		binary.LittleEndian.PutUint32(buf[24+i*4:28+i*4], o)
	}
	return buf
}

// pgProcLangOID maps a routine's language name to pg_language's OID:
// internal=12, c=13, sql=14, plpgsql=13627 (stock PG 18.3 initdb value,
// matching goopg's pg_language virtual row — see pg_proc_view.go).
func pgProcLangOID(lang string) int64 {
	switch lang {
	case "internal":
		return 12
	case "c":
		return 13
	case "plpgsql":
		return 13627
	default: // "sql" and anything unregistered renders as SQL
		return 14
	}
}

// buildPGProcRow builds the 30-column PG18-physical pg_proc row for a user
// routine. Value semantics mirror the pg_proc virtual view (pg_proc_view.go)
// and upstream CREATE FUNCTION defaults.
func buildPGProcRow(cat catalog.Catalog, r *catalog.Routine) Row {
	kind := r.KindChar
	if kind == "" {
		if r.IsProcedure {
			kind = "p"
		} else if r.IsWindow {
			kind = "w"
		} else {
			kind = "f"
		}
	}
	vol := r.Volatile
	if vol == "" {
		vol = "v"
	}
	par := r.Parallel
	if par == "" {
		par = "u" // PG CREATE FUNCTION default: PARALLEL UNSAFE
	}
	cost := int64(100)
	if r.Language == "internal" || r.Language == "c" {
		cost = 1
	}
	if r.Cost != "" {
		if v, err := strconv.ParseFloat(r.Cost, 64); err == nil {
			cost = int64(v)
		}
	}
	rows := int64(0)
	if r.ReturnsSet {
		rows = 1000
	}
	if r.Rows != "" {
		if v, err := strconv.ParseFloat(r.Rows, 64); err == nil {
			rows = int64(v)
		}
	}
	argOIDs := make([]uint32, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		// Deferral row 1351: prefer the CREATE-time resolved OID for a `char`
		// arg (ArgTypeOIDs is char-only non-zero), so a quoted `"char"` writes
		// CHAROID(18) into proargtypes instead of TypeNameToOID's unconditional
		// BPCHAROID(1042). The i<len guard is OOB-safe for pre-change routines.
		if i < len(r.ArgTypeOIDs) && r.ArgTypeOIDs[i] != 0 {
			argOIDs[i] = r.ArgTypeOIDs[i]
		} else {
			argOIDs[i] = catalog.TypeNameToOID(t.Name)
		}
	}
	// Deferral row 1361: prefer the CREATE-time resolved RETURN-type OID
	// (ReturnTypeOID is char-only non-zero), so a `RETURNS "char"` writes
	// CHAROID(18) into prorettype instead of TypeNameToOID's unconditional
	// BPCHAROID(1042) — sibling of the proargtypes handling above.
	retTypeOID := catalog.TypeNameToOID(r.ReturnType.Name)
	if r.ReturnTypeOID != 0 {
		retTypeOID = r.ReturnTypeOID
	}
	nargDefaults := 0
	for _, d := range r.ArgDefaults {
		if d != "" {
			nargDefaults++
		}
	}
	// Absent nullable columns are genuinely NULL (pg_class builder
	// convention, pg18_user_catalog_rows.go): PG branches on attisnull for
	// these — a non-NULL empty text varlena in prosqlbody would send every
	// SQL-function call on a real PG standby into stringToNode(""), and an
	// empty non-array value in proargnames/proallargtypes would corrupt
	// array deconstruction in FuncnameGetCandidates.
	argNames := NullDatum
	if len(r.ArgNames) > 0 {
		hasName := false
		for _, n := range r.ArgNames {
			if n != "" {
				hasName = true
				break
			}
		}
		if hasName {
			argNames = NewBytesDatum(pgTextArrayBytes(r.ArgNames))
		}
	}
	proconfig := NullDatum
	if len(r.Config) > 0 {
		proconfig = NewBytesDatum(pgTextArrayBytes(r.Config))
	}
	return Row{
		NewIntDatum(int64(r.OID)),                                // 1  oid
		NewStringDatum(r.Name),                                   // 2  proname
		NewIntDatum(int64(namespaceOIDForSchema(cat, r.Schema))), // 3  pronamespace
		NewIntDatum(int64(r.OwnerOrDefault())),                   // 4  proowner
		NewIntDatum(pgProcLangOID(r.Language)),                   // 5  prolang
		NewIntDatum(cost),                                        // 6  procost
		NewIntDatum(rows),                                        // 7  prorows
		NewIntDatum(0),                                           // 8  provariadic
		NewIntDatum(0),                                           // 9  prosupport
		NewStringDatum(kind),                                     // 10 prokind
		NewBoolDatum(r.SecurityDefiner),                          // 11 prosecdef
		NewBoolDatum(r.Leakproof),                                // 12 proleakproof
		NewBoolDatum(r.Strict),                                   // 13 proisstrict
		NewBoolDatum(r.ReturnsSet),                               // 14 proretset
		NewStringDatum(vol),                                      // 15 provolatile
		NewStringDatum(par),                                      // 16 proparallel
		NewIntDatum(int64(len(r.ArgTypes))),                      // 17 pronargs
		NewIntDatum(int64(nargDefaults)),                         // 18 pronargdefaults
		NewIntDatum(int64(retTypeOID)),                           // 19 prorettype
		NewBytesDatum(pgProcOidVectorBytes(argOIDs)),             // 20 proargtypes
		NullDatum,              // 21 proallargtypes (OUT-arg metadata: follow-up)
		NullDatum,              // 22 proargmodes (follow-up with 21)
		argNames,               // 23 proargnames
		argMetaDatum(r),        // 24 proargdefaults (see pgProcArgMetaJSON)
		NullDatum,              // 25 protrftypes
		NewStringDatum(r.Body), // 26 prosrc
		NullDatum,              // 27 probin
		NullDatum,              // 28 prosqlbody (NULL ⇒ PG executes prosrc)
		proconfig,              // 29 proconfig
		NullDatum,              // 30 proacl (NULL ⇒ owner + PUBLIC EXECUTE default)
	}
}

// pgProcArgMetaJSON serializes the Routine metadata the 30 physical columns
// cannot carry faithfully (arg names/modes/defaults as raw SQL, RETURNS
// TABLE shape, BEGIN ATOMIC/RETURN forms, dependency tracking) so the
// startup reload reconstructs the routine with today's WAL-payload fidelity.
// Stored in proargdefaults (pg_node_tree): PostgreSQL reads that column ONLY
// when pronargdefaults > 0 (functions that HAVE defaults were already a
// deviation surface); goopg's reload always reads it. Body is excluded —
// prosrc carries it.
func pgProcArgMetaJSON(r *catalog.Routine) []byte {
	clone := *r
	clone.Body = ""
	b, err := json.Marshal(&clone)
	if err != nil {
		return nil
	}
	return b
}

// argMetaDatum wraps the JSON blob as a text-varlena datum for the
// pg_node_tree column ("" only if marshaling failed).
func argMetaDatum(r *catalog.Routine) Datum {
	b := pgProcArgMetaJSON(r)
	if len(b) == 0 {
		return NewStringDatum("")
	}
	return NewStringDatum(string(b))
}

// DecodePGProcArgMeta reconstructs the Routine from the proargdefaults JSON
// blob + the physical prosrc. Exported for the initdb reload.
func DecodePGProcArgMeta(meta string, body string) (*catalog.Routine, error) {
	var r catalog.Routine
	if err := json.Unmarshal([]byte(meta), &r); err != nil {
		return nil, err
	}
	r.Body = body
	return &r, nil
}

// pgProcRel returns the pg_proc heap relfile for this connection's
// catalog-write database.
func pgProcRel(ctx *Context) storage.RelFileNode {
	return storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: pgProcRelOID,
		Fork:   storage.MainFork,
	}
}

// mirrorProcCatalogFiles propagates the pg_proc heap file to the postgres
// DB's copy (doc 02a §2.2 review BLOCKER-3 — reload reads base/5).
func mirrorProcCatalogFiles(ctx *Context) error {
	if tableCatalogHeapDBOid(ctx) != catalog.DefaultDBOid {
		return nil
	}
	return mirrorTouchedCatalogsToPostgresDB(ctx)
}

// syncRoutineToCatalogHeap journals CREATE [OR REPLACE] FUNCTION/PROCEDURE:
// a heap INSERT for a new routine, or a non-HOT heap UPDATE of the existing
// row when OR REPLACE preserved the OID and a live row exists.
func syncRoutineToCatalogHeap(ctx *Context, r *catalog.Routine) error {
	rs := ctx.Catalog.Routines()
	if rs == nil || ctx.Pool == nil {
		return nil
	}
	row := buildPGProcRow(ctx.Catalog, r)
	if tid, ok := rs.HeapTID(r.OID); ok {
		oldTID := storage.ItemPointer{Block: storage.BlockNumber(tid.Block), Offset: tid.Offset}
		newTID, err := updateHeapRowCanonicalPG(ctx, pgProcRel(ctx), PGProcColumnsPG18(), oldTID, row)
		if err != nil {
			return fmt.Errorf("pg_proc update: %w", err)
		}
		rs.SetHeapTID(r.OID, catalog.SchemaHeapTID{Block: uint32(newTID.Block), Offset: newTID.Offset})
		if err := insertPgProcIndexEntries(ctx, r, newTID); err != nil {
			return err
		}
		return mirrorProcCatalogFiles(ctx)
	}
	tid, err := writeHeapRowCanonical(ctx, pgProcRel(ctx), PGProcColumnsPG18(), row)
	if err != nil {
		return fmt.Errorf("pg_proc: %w", err)
	}
	rs.SetHeapTID(r.OID, catalog.SchemaHeapTID{Block: uint32(tid.Block), Offset: tid.Offset})
	if err := insertPgProcIndexEntries(ctx, r, tid); err != nil {
		return err
	}
	return mirrorProcCatalogFiles(ctx)
}

// insertPgProcIndexEntries adds the (oid) and (proname, proargtypes,
// pronamespace) entries for a freshly written pg_proc heap row version.
// Key derivation mirrors buildPGProcRow so index keys and heap columns
// always agree.
func insertPgProcIndexEntries(ctx *Context, r *catalog.Routine, tid storage.ItemPointer) error {
	if err := insertPgProcOidIndexEntry(ctx, r.OID, tid); err != nil {
		return fmt.Errorf("pg_proc_oid_index: %w", err)
	}
	argOIDs := make([]uint32, len(r.ArgTypes))
	for i, t := range r.ArgTypes {
		// Sibling of buildPGProcRow's argOIDs (deferral row 1351): prefer the
		// stored char OID so the (proname, proargtypes, pronamespace) index key
		// matches the heap row's proargtypes (quoted `"char"` → 18, not 1042).
		if i < len(r.ArgTypeOIDs) && r.ArgTypeOIDs[i] != 0 {
			argOIDs[i] = r.ArgTypeOIDs[i]
		} else {
			argOIDs[i] = catalog.TypeNameToOID(t.Name)
		}
	}
	nsp := namespaceOIDForSchema(ctx.Catalog, r.Schema)
	if err := insertPgProcPronameArgsNspIndexEntry(ctx, r.Name, argOIDs, nsp, tid); err != nil {
		return fmt.Errorf("pg_proc_proname_args_nsp_index: %w", err)
	}
	return nil
}

// updateRoutineCatalogHeapRow journals every ALTER FUNCTION variant
// (rename/owner/set-schema/flags/config): the registry has already been
// mutated, so rebuild the row from the live Routine and heap-UPDATE it.
// A missing TID (pre-conversion dir) falls back to a fresh INSERT.
func updateRoutineCatalogHeapRow(ctx *Context, r *catalog.Routine) error {
	return syncRoutineToCatalogHeap(ctx, r)
}

// deleteRoutineCatalogHeapRow journals DROP FUNCTION/PROCEDURE: a heap
// DELETE (xl_heap_delete) of the routine's row. Missing TID is a no-op.
func deleteRoutineCatalogHeapRow(ctx *Context, oid uint32) error {
	rs := ctx.Catalog.Routines()
	if rs == nil || ctx.Pool == nil {
		return nil
	}
	tid, ok := rs.HeapTID(oid)
	if !ok {
		return nil
	}
	if err := ctx.MaterializeWriterXID(); err != nil {
		return err
	}
	rel := pgProcRel(ctx)
	slot, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: storage.BlockNumber(tid.Block)})
	if err != nil {
		return err
	}
	slot.Lock()
	ht, err := storage.PageGetHeapTuple(slot.Page(), tid.Offset)
	if err != nil || ht.Header.Xmax != storage.InvalidTransactionID {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		rs.DeleteHeapTID(oid)
		return nil
	}
	oldTuple, err := ht.MarshalBinary()
	if err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	xmax := effectiveWriterXID(ctx)
	if err := storage.PageSetHeapTupleXmax(slot.Page(), tid.Offset, xmax); err != nil {
		slot.Unlock()
		ctx.Pool.Unpin(slot)
		return err
	}
	derr := markHeapDeleteDirty(ctx.Pool, slot, rel, storage.BlockNumber(tid.Block), tid.Offset, xmax, oldTuple)
	slot.Unlock()
	ctx.Pool.Unpin(slot)
	if derr != nil {
		return derr
	}
	rs.DeleteHeapTID(oid)
	return mirrorProcCatalogFiles(ctx)
}
