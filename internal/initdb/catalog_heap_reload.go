package initdb

// Phase-B0.1 (docs/design/wal-pg-identical-stream/02a §2): the generic
// per-catalog heap-scan reload framework. One shared scan loop + ONE
// visibility filter replace the per-scanner copies; each converted catalog
// contributes a catalogReloadDesc instead of a bespoke *_ddl_recovery.go
// scanner. loadUserTablesFromHeapForDB (pg_class + pg_attribute) is the
// first user — a pure refactor with zero behavior change.

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// catalogRowLive is the SINGLE liveness filter for catalog-heap reload scans,
// transcribed from the pre-B0.1 pg_class/pg_attribute scans (open.go, M0030-0007
// / M0106-0010) — doc 02a §2.3 is the normative statement of these rules:
//
//  1. xmin == Invalid → not a real tuple.
//  2. Any non-zero xmax → dead. Unconditional today: catalog mutations are
//     delete+reinsert, and an aborted DDL's reinserted row dies by rule 3.
//     (B0.2 — catalog heap UPDATE — upgrades this rule to consult the xmax's
//     CLOG status so an ABORTED updater does not kill the only live version;
//     that change lands WITH the update emit, not here.)
//  3. Aborted xmin → dead, for every layout. This is deliberately the ONLY
//     xmin check for PG18-canonical rows: basebackup tuples carry upstream
//     xmin values that are out-of-range for the local clog (GetStatus →
//     Unknown) and must pass through, or catalogs empty on standby bootstrap.
//  4. requireCommittedXmin (legacy-layout rows in scans that opt in) → the
//     row additionally needs a locally-committed xmin.
func catalogRowLive(clog *mvcc.CLog, ht storage.HeapTuple, requireCommittedXmin bool) bool {
	if ht.Header.Xmin == storage.InvalidTransactionID {
		return false
	}
	if ht.Header.Xmax != storage.InvalidTransactionID {
		return false
	}
	if clog == nil {
		return true
	}
	if clog.GetStatus(ht.Header.Xmin) == mvcc.TxnStatusAborted {
		return false
	}
	if requireCommittedXmin && clog.GetStatus(ht.Header.Xmin) != mvcc.TxnStatusCommitted {
		return false
	}
	return true
}

// scanCatalogHeapRows is the generic reload scan loop: walk every block of a
// catalog heap, extract live tuples per catalogRowLive, and decode them.
//
// decode inspects the raw tuple (and its heap TID, for TID-carrying caches)
// and returns (row, requireCommittedXmin, err):
// requireCommittedXmin feeds rule 4 above — pg_class returns !physicalRow
// (legacy-layout rows need a committed xmin), pg_attribute always returns
// false (its scan never had the committed branch). A decode error skips the
// tuple, exactly like the pre-B0.1 scans.
//
// A missing or empty heap returns (nil, nil): catalogs are absent on old
// clusters / fresh initdb and that is not an error. Block-read failures are
// fatal (the file exists but cannot be read); torn line-pointer counts skip
// the block, matching the historical loops.
func scanCatalogHeapRows(mgr *storage.Manager, rel storage.RelFileNode, clog *mvcc.CLog,
	name string, decode func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error)) ([]any, error) {
	nBlocks, err := mgr.NBlocks(rel)
	if err != nil || nBlocks == 0 {
		return nil, nil
	}
	page := make(storage.Page, storage.BlockSize)
	var rows []any
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		if err := mgr.ReadBlock(rel, blk, page); err != nil {
			return nil, fmt.Errorf("%s reload: read blk %d: %w", name, blk, err)
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			continue
		}
		for slot := uint16(1); slot <= uint16(count); slot++ {
			ht, err := storage.PageGetHeapTuple(page, slot)
			if err != nil {
				continue
			}
			if ht.Header.Xmin == storage.InvalidTransactionID ||
				ht.Header.Xmax != storage.InvalidTransactionID {
				continue
			}
			row, requireCommitted, derr := decode(ht, storage.ItemPointer{Block: blk, Offset: slot})
			if derr != nil {
				continue
			}
			if !catalogRowLive(clog, ht, requireCommitted) {
				continue
			}
			rows = append(rows, row)
		}
	}
	return rows, nil
}

// catalogReloadDesc describes one catalog's startup reload. Converted
// catalogs (B1+) register a descriptor here instead of keeping a bespoke
// scanner; Slot values are DERIVED from the historical recovery-pass call
// sequence in Open (doc 02a §2.4) so relative order never changes as
// catalogs migrate.
type catalogReloadDesc struct {
	Name string // "pg_namespace" — log/error labels only
	Slot int    // recovery-pass slot (02a §2.4); lower runs first
	// Fatal: a reload error aborts startup (schema/table precedent) instead
	// of warn-and-continue (statistics/index precedent, Open).
	Fatal bool
	// Reload performs the catalog's scan+apply. Simple single-heap catalogs
	// build this with simpleCatalogReload; multi-heap joins (the pg_class +
	// pg_attribute exemplar) implement it directly.
	Reload func(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog, heapDBOid, nsDBOid uint32) error
}

// simpleCatalogReload builds a Reload for a single-heap catalog from the doc
// 02a §2.2 Decode/ApplyBatch pair. shared=true scans global/<relOid> (B4).
func simpleCatalogReload(relOid uint32, shared bool, name string,
	decode func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error),
	applyBatch func(cat *catalog.InMemory, nsDBOid uint32, rows []any) error,
) func(*storage.Manager, *catalog.InMemory, *mvcc.CLog, uint32, uint32) error {
	return func(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog, heapDBOid, nsDBOid uint32) error {
		dbOid := heapDBOid
		if shared {
			dbOid = 0 // global/ tablespace routing (B4)
		}
		rel := storage.RelFileNode{DBOid: dbOid, RelOid: relOid, Fork: storage.MainFork}
		rows, err := scanCatalogHeapRows(mgr, rel, clog, name, decode)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		return applyBatch(cat, nsDBOid, rows)
	}
}

// reloadUserSchemasFromHeap is B1.1's pg_namespace reload — the generic
// heap-scan replacement for the retired replaySchemaDDLRecords scanner
// (RecordKinds 34/35/100/101). It scans base/<cat.DBOID()>/2615 for live
// rows with oid >= FirstUserOID (builtin schemas stay compiled-in +
// initdb-populated), re-registers each into the global schema registry with
// its recovered OID and owner, and seeds the TID-carrying cache so a later
// ALTER/DROP can locate the row (doc 02a §3.3). Dropped/renamed schemas
// need no special handling: their old versions carry xmax and the liveness
// filter skips them.
func reloadUserSchemasFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	type nsRow struct {
		oid   uint32
		name  string
		owner uint32
		tid   storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2615, Fork: storage.MainFork}
	cols := executor.PGNamespaceColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_namespace",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return nsRow{
				oid:   uint32(decoded[0].Int),
				name:  decoded[1].StringValue(),
				owner: uint32(decoded[2].Int),
				tid:   tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		ns := r.(nsRow)
		if ns.oid < catalog.FirstUserOID || ns.name == "" {
			continue
		}
		cat.RegisterSchemaDuringRecovery(ns.name, ns.oid)
		if ns.owner != 0 {
			cat.SetSchemaOwnerDuringRecovery(ns.name, ns.owner)
		}
		cat.SetSchemaHeapTID(ns.name, catalog.SchemaHeapTID{Block: uint32(ns.tid.Block), Offset: ns.tid.Offset})
	}
	return nil
}

// reloadUserRoutinesFromHeap is B1.2's pg_proc reload — the generic
// heap-scan replacement for the retired replayFunctionDDLRecords scanner
// (RecordKinds 61-64/121-123). Live rows with oid >= FirstUserOID carry the
// full Routine metadata as JSON in proargdefaults (col 24, see
// executor.DecodePGProcArgMeta) + the body in prosrc; builtins (the 3397
// seed rows) stay compiled-in/seeded and are skipped by the OID filter.
func reloadUserRoutinesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	rs := cat.Routines()
	if rs == nil {
		return nil
	}
	type procRow struct {
		r   *catalog.Routine
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 1255, Fork: storage.MainFork}
	cols := executor.PGProcColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_proc",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			oid := uint32(decoded[0].Int)
			if oid < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			meta := decoded[23].StringValue() // proargdefaults: Routine JSON
			body := decoded[25].StringValue() // prosrc
			r, derr := executor.DecodePGProcArgMeta(meta, body)
			if derr != nil {
				return nil, false, derr
			}
			return procRow{r: r, tid: tid}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		pr := raw.(procRow)
		rs.CreateDuringRecovery(pr.r)
		rs.SetHeapTID(pr.r.OID, catalog.SchemaHeapTID{Block: uint32(pr.tid.Block), Offset: pr.tid.Offset})
	}
	return nil
}

// errSkipBuiltinRow marks reload rows filtered by policy (decode-level skip).
var errSkipBuiltinRow = fmt.Errorf("catalog reload: builtin row skipped")

// reloadSequenceHeapTIDs is B1.3's pg_sequence TID seeding: for each live
// pg_sequence heap row, reverse-map seqrelid to the (already kind-65-
// restored) sequence and record the row's TID + definition fingerprint so
// post-restart ALTERs update in place. Rows whose seqrelid no longer
// resolves (dropped sequence's stale row on a pre-B1.3 dir) are skipped.
func reloadSequenceHeapTIDs(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	type seqRow struct {
		seqrelid uint32
		tid      storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2224, Fork: storage.MainFork}
	cols := executor.PGSequenceColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_sequence",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return seqRow{seqrelid: uint32(decoded[0].Int), tid: tid}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		sr := raw.(seqRow)
		tbl, dbOid, ok := cat.LookupTableByOIDAllDBs(sr.seqrelid)
		if !ok || tbl == nil || !tbl.IsSequence {
			continue
		}
		name := tbl.Name
		if tbl.Schema != "" && tbl.Schema != "public" {
			name = tbl.Schema + "." + tbl.Name
		}
		p, ok := executor.SnapshotSequenceState(name, dbOid)
		if !ok {
			continue
		}
		executor.SeedSequenceHeapTID(name, dbOid, sr.seqrelid,
			catalog.SchemaHeapTID{Block: uint32(sr.tid.Block), Offset: sr.tid.Offset}, p)
	}
	return nil
}

// runCatalogReloads executes every descriptor in Slot order. Non-Fatal
// descriptor errors are returned in aggregate form by the caller's logging
// convention; Fatal ones abort immediately.
func runCatalogReloads(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog,
	heapDBOid, nsDBOid uint32, descs []catalogReloadDesc, warn func(name string, err error)) error {
	sorted := make([]catalogReloadDesc, len(descs))
	copy(sorted, descs)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Slot < sorted[j].Slot })
	for _, d := range sorted {
		if err := d.Reload(mgr, cat, clog, heapDBOid, nsDBOid); err != nil {
			if d.Fatal {
				return fmt.Errorf("catalog reload %s: %w", d.Name, err)
			}
			if warn != nil {
				warn(d.Name, err)
			}
		}
	}
	return nil
}

// pgTypeArgsFromTypmod is the decode twin of executor's pgAttTypmod
// (pg18_user_catalog_rows.go): reconstruct a base type's argument list from
// the stored typtypmod so a reloaded domain's Base.Args round-trips
// (numeric(10,2), varchar(32), bit(8), ...). -1 (or anything non-positive
// for the VARHDRSZ families) means "no args".
func pgTypeArgsFromTypmod(baseOID uint32, typmod int64) []int64 {
	switch baseOID {
	case 1700: // numeric: ((precision<<16) | scale) + VARHDRSZ
		if typmod < 4 {
			return nil
		}
		raw := typmod - 4
		precision := (raw >> 16) & 0xffff
		scale := raw & 0xffff
		if scale != 0 {
			return []int64{precision, scale}
		}
		return []int64{precision}
	case 1042, 1043: // char(n) / varchar(n): n + VARHDRSZ
		if typmod < 4 {
			return nil
		}
		return []int64{typmod - 4}
	case 1560, 1562: // bit(n) / varbit(n): raw length
		if typmod <= 0 {
			return nil
		}
		return []int64{typmod}
	}
	return nil
}

// domainInValuesFromConbin re-derives a DomainCheck's InValues fast-path
// list from the synthesized ScalarArrayOpExpr text goopg stores in conbin —
// `VALUE = ANY (ARRAY['a'::text, 'b'::text])` and the varchar variant
// `(VALUE)::text = ANY ((ARRAY[...])::text[])` (see
// domainInValuesCheckExpr, operators_ddl.go). Returns nil for any other
// CHECK shape (general expressions carry no membership list). Runtime
// enforcement reads ONLY InValues (expr.go:871), so this restoration is
// load-bearing, not cosmetic.
func domainInValuesFromConbin(conbin string) []string {
	start := strings.Index(conbin, "ARRAY[")
	if start < 0 || !strings.Contains(conbin, "= ANY") {
		return nil
	}
	rest := conbin[start+len("ARRAY["):]
	end := strings.IndexByte(rest, ']')
	if end < 0 {
		return nil
	}
	var out []string
	for _, part := range strings.Split(rest[:end], ",") {
		v := strings.TrimSpace(part)
		// Strip a trailing ::type cast (everything from the first "::").
		if i := strings.Index(v, "::"); i >= 0 {
			v = v[:i]
		}
		v = strings.TrimSuffix(strings.TrimPrefix(v, "("), ")")
		// Unquote 'literal' (single quotes; embedded '' unescapes).
		if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
			v = strings.ReplaceAll(v[1:len(v)-1], "''", "'")
		}
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

// reloadUserDomainsFromHeap is B2.1b's domain reload — the generic heap-scan
// replacement for the retired replayDomainDDLRecords scanner (RecordKinds
// 119/120). The domain skeleton comes from its pg_type row (typtype='d',
// oid >= FirstUserOID); its CHECK constraints come from the pg_constraint
// heap rows keyed by contypid. Every user pg_type row's TID (domains AND
// their array peers — also enum/range/composite rows) is seeded into the
// TypeHeapTID cache so ALTER-driven non-HOT updates find the live version.
func reloadUserDomainsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	typeCols := executor.PGTypeColumnsPG18()
	type typeRow struct {
		decoded executor.Row
		tid     storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: catalog.TypeRelationId, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_type",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(typeCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, typeCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return typeRow{decoded: decoded, tid: tid}, false, nil
		})
	if err != nil {
		return err
	}

	// Constraint rows grouped by contypid (col 9), decoded up front so each
	// domain picks up its CHECKs in one pass.
	conCols := executor.PGConstraintColumnsPG18()
	type conRow struct {
		oid   uint32
		typid uint32
		name  string
		expr  string
	}
	conRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2606, Fork: storage.MainFork}
	conRaw, err := scanCatalogHeapRows(mgr, conRel, clog, "pg_constraint",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(conCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, conCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			typid := uint32(decoded[9].Int)
			if typid < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return conRow{
				oid:   uint32(decoded[0].Int),
				typid: typid,
				name:  decoded[1].StringValue(),
				expr:  decoded[27].StringValue(),
			}, false, nil
		})
	if err != nil {
		return err
	}
	checksByDomain := make(map[uint32][]catalog.DomainCheck)
	for _, raw := range conRaw {
		c := raw.(conRow)
		checksByDomain[c.typid] = append(checksByDomain[c.typid], catalog.DomainCheck{
			Name:     c.name,
			Expr:     c.expr,
			OID:      c.oid,
			InValues: domainInValuesFromConbin(c.expr),
		})
	}

	for _, raw := range rows {
		tr := raw.(typeRow)
		d := tr.decoded
		oid := uint32(d[0].Int)
		// Seed the TID cache for EVERY user pg_type row (domains, arrays,
		// enums, ranges, composites) — ALTER updates need the live TID.
		cat.SetTypeHeapTID(oid, catalog.SchemaHeapTID{Block: uint32(tr.tid.Block), Offset: tr.tid.Offset})
		if d[6].StringValue() != "d" { // typtype
			continue
		}
		baseOID := uint32(d[25].Int) // typbasetype
		baseName := "text"           // TypeNameToOID's own fallback
		if e, ok := pgTypeCanonical(baseOID); ok {
			baseName = e.Name
		}
		dom := &catalog.Domain{
			Name: d[1].StringValue(),
			OID:  oid,
			// Key under the scanned DB's OID — the OID a live session
			// resolves for LookupDomain (see RegisterDomainDuringRecovery).
			DBOid:    cat.DBOID(),
			ArrayOID: uint32(d[14].Int), // typarray
			Base: catalog.Type{
				Name: baseName,
				Args: pgTypeArgsFromTypmod(baseOID, d[26].Int), // typtypmod
			},
			BaseOID: baseOID,
			// BaseIsEnum stays false: the enum registry is not yet
			// heap-durable (B2.1d), so an enum-based domain's base is
			// already gone post-restart — matches prior behavior.
			NotNull: d[24].BoolValue(), // typnotnull
			Owner:   uint32(d[3].Int),  // typowner
			Checks:  checksByDomain[oid],
		}
		if bin := d[29].StringValue(); bin != "" { // typdefaultbin
			if expr, perr := parser.ParseExpr(bin); perr == nil {
				dom.Default = expr
			}
			// A deparse/reparse gap degrades to no DEFAULT rather than
			// failing startup (ported from replayDomainDDLRecords).
		}
		cat.RegisterDomainDuringRecovery(dom)
	}
	return nil
}

// reloadUserRangeTypesFromHeap is B2.1c's range-type reload — the generic
// heap-scan replacement for the retired replayRangeTypeDDLRecords scanner
// (RecordKinds 81/82/117/118). The registry entry reconstructs fully
// physically: the pg_range row (rngtypid >= FirstUserOID) carries the
// linkage (subtype, multirange, opclass, collation); the range and
// multirange pg_type rows carry names/array peers/owner; the subtype name
// resolves via pgTypeCanonical.
func reloadUserRangeTypesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	rangeCols := executor.PGRangeColumnsPG18()
	type rangeRow struct {
		typid, subtype, multitypid, collation, subopc uint32
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3541, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_range",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(rangeCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, rangeCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return rangeRow{
				typid:      uint32(decoded[0].Int),
				subtype:    uint32(decoded[1].Int),
				multitypid: uint32(decoded[2].Int),
				collation:  uint32(decoded[3].Int),
				subopc:     uint32(decoded[4].Int),
			}, false, nil
		})
	if err != nil || len(rows) == 0 {
		return err
	}

	// One pg_type pass collecting the user rows the linkage needs
	// (names, array peers, owner).
	typeCols := executor.PGTypeColumnsPG18()
	type typeInfo struct {
		name     string
		arrayOID uint32
		owner    uint32
	}
	typeRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: catalog.TypeRelationId, Fork: storage.MainFork}
	typeRows, err := scanCatalogHeapRows(mgr, typeRel, clog, "pg_type",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(typeCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, typeCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			oid := uint32(decoded[0].Int)
			if oid < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return [2]any{oid, typeInfo{
				name:     decoded[1].StringValue(),
				arrayOID: uint32(decoded[14].Int),
				owner:    uint32(decoded[3].Int),
			}}, false, nil
		})
	if err != nil {
		return err
	}
	types := make(map[uint32]typeInfo, len(typeRows))
	for _, raw := range typeRows {
		pair := raw.([2]any)
		types[pair[0].(uint32)] = pair[1].(typeInfo)
	}

	for _, raw := range rows {
		rr := raw.(rangeRow)
		rangeT, ok := types[rr.typid]
		if !ok {
			continue // pg_type row dead (dropped range) — skip
		}
		multiT := types[rr.multitypid]
		subtypeName := "text"
		if e, ok := pgTypeCanonical(rr.subtype); ok {
			subtypeName = e.Name
		}
		cat.RegisterRangeTypeDuringRecovery(&catalog.RangeType{
			Name:               rangeT.name,
			OID:                rr.typid,
			DBOid:              cat.DBOID(),
			ArrayOID:           rangeT.arrayOID,
			SubtypeName:        subtypeName,
			OpclassOID:         rr.subopc,
			CollationOID:       rr.collation,
			MultirangeOID:      rr.multitypid,
			MultirangeArrayOID: multiT.arrayOID,
			MultirangeName:     multiT.name,
			Owner:              rangeT.owner,
		})
	}
	return nil
}

// reloadUserEnumsFromHeap is B2.1d's enum reload — enums previously had NO
// restart durability at all (labels lived only in the in-memory registry;
// no WAL record, no scanner). The enum skeleton comes from its pg_type row
// (typtype='e', oid >= FirstUserOID); its labels come from the pg_enum heap
// rows grouped by enumtypid, ordered by enumsortorder.
func reloadUserEnumsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	enumCols := executor.PGEnumColumnsPG18()
	type labelRow struct {
		oid   uint32
		typid uint32
		sort  float64
		label string
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3501, Fork: storage.MainFork}
	labels, err := scanCatalogHeapRows(mgr, rel, clog, "pg_enum",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(enumCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, enumCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			// enumsortorder rides the "xid" encode-hint: LE uint32 carrying
			// IEEE-754 float32 bits (see executor.PGEnumColumnsPG18).
			sort := float64(math.Float32frombits(uint32(decoded[2].Int)))
			return labelRow{
				oid:   uint32(decoded[0].Int),
				typid: uint32(decoded[1].Int),
				sort:  sort,
				label: decoded[3].StringValue(),
			}, false, nil
		})
	if err != nil || len(labels) == 0 {
		return err
	}
	byType := make(map[uint32][]labelRow)
	for _, raw := range labels {
		lr := raw.(labelRow)
		byType[lr.typid] = append(byType[lr.typid], lr)
	}

	typeCols := executor.PGTypeColumnsPG18()
	typeRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: catalog.TypeRelationId, Fork: storage.MainFork}
	typeRows, err := scanCatalogHeapRows(mgr, typeRel, clog, "pg_type",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(typeCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, typeCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			oid := uint32(decoded[0].Int)
			if oid < catalog.FirstUserOID || decoded[6].StringValue() != "e" {
				return nil, false, errSkipBuiltinRow
			}
			return [4]any{oid, decoded[1].StringValue(), uint32(decoded[14].Int), uint32(decoded[3].Int)}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range typeRows {
		tr := raw.([4]any)
		oid := tr[0].(uint32)
		lrs := byType[oid]
		if len(lrs) == 0 {
			continue
		}
		sort.Slice(lrs, func(i, j int) bool { return lrs[i].sort < lrs[j].sort })
		values := make([]catalog.EnumValue, len(lrs))
		for i, lr := range lrs {
			values[i] = catalog.EnumValue{Label: lr.label, SortOrder: lr.sort, OID: lr.oid}
		}
		cat.RegisterEnumDuringRecovery(&catalog.EnumType{
			Name:     tr[1].(string),
			OID:      oid,
			ArrayOID: tr[2].(uint32),
			Values:   values,
			Owner:    tr[3].(uint32),
			DBOid:    cat.DBOID(),
		})
	}
	return nil
}
