package initdb

// Phase-B0.1 (docs/design/wal-pg-identical-stream/02a §2): the generic
// per-catalog heap-scan reload framework. One shared scan loop + ONE
// visibility filter replace the per-scanner copies; each converted catalog
// contributes a catalogReloadDesc instead of a bespoke *_ddl_recovery.go
// scanner. loadUserTablesFromHeapForDB (pg_class + pg_attribute) is the
// first user — a pure refactor with zero behavior change.

import (
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/wal"
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

// reloadUserTablespacesFromHeap is B4.1e's pg_tablespace reload — the generic
// heap-scan replacement for the retired replayTablespaceDDLRecords scanner
// (RecordKinds 124/125). pg_tablespace is a SHARED catalog (global/1213), so
// the scan targets DBOid=0. Live rows with oid >= FirstUserOID are the user
// in-place tablespaces; the two bootstrap rows (pg_default/pg_global) are
// surfaced separately by catalog.tablespaceVirtualRows and skipped by the OID
// filter. The registry's owner/location fields are never read (the virtual
// view hardcodes spcowner=10), so they are re-registered empty. Dropped
// tablespaces carry xmax and the liveness filter skips them.
func reloadUserTablespacesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	type tsRow struct {
		oid  uint32
		name string
	}
	rel := storage.RelFileNode{DBOid: 0, RelOid: 1213, Fork: storage.MainFork}
	cols := executor.PGTablespaceColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_tablespace",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return tsRow{oid: uint32(decoded[0].Int), name: decoded[1].StringValue()}, false, nil
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		ts := r.(tsRow)
		if ts.oid < catalog.FirstUserOID || ts.name == "" {
			continue
		}
		cat.RegisterTablespaceDuringRecovery(ts.name, "", "", ts.oid)
	}
	return nil
}

// reloadDbRoleSettingsFromHeap is B4.2's pg_db_role_setting reload — the
// generic heap-scan replacement for the retired replayDatabaseConfigRecords +
// replayRoleConfigRecords scanners (RecordKinds 73-78). pg_db_role_setting is
// SHARED (global/2964). Each live row's setconfig text[] is split back into
// "name=value" entries and re-applied to the dbRoleSettings/roleSettings
// registries via the idempotent SetDatabaseConfig (setrole==0) / SetRoleConfig
// (setrole!=0). Dropped rows (RESET ALL) carry xmax and are skipped by the
// liveness filter.
func reloadDbRoleSettingsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	type cfgRow struct {
		setDatabase uint32
		setRole     uint32
		entries     []string
	}
	rel := storage.RelFileNode{DBOid: 0, RelOid: 2964, Fork: storage.MainFork}
	cols := executor.PGDbRoleSettingColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_db_role_setting",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return cfgRow{
				setDatabase: uint32(decoded[0].Int),
				setRole:     uint32(decoded[1].Int),
				entries:     executor.ParseTextArrayLiteral(decoded[2].StringValue()),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		c := r.(cfgRow)
		for _, entry := range c.entries {
			eq := strings.IndexByte(entry, '=')
			if eq < 0 {
				continue
			}
			name, value := entry[:eq], entry[eq+1:]
			if c.setRole == 0 {
				cat.SetDatabaseConfig(c.setDatabase, name, value)
			} else {
				cat.SetRoleConfig(c.setRole, c.setDatabase, name, value)
			}
		}
	}
	return nil
}

// reloadSubscriptionsFromHeap is B4.4's pg_subscription reload — the generic
// heap-scan replacement for the retired replayPubSubDDLRecords scanner
// (RecordKinds 53/54/55; the publication kinds were already retired in B3.3).
// pg_subscription is SHARED (global/6100). Each live row's 8 goopg-tracked
// columns rebuild a catalog.Subscription (the other 10 PG columns are
// registry-irrelevant defaults); dropped rows carry xmax and are skipped.
func reloadSubscriptionsFromHeap(mgr *storage.Manager, pubsub *catalog.PubSub, clog *mvcc.CLog) error {
	rel := storage.RelFileNode{DBOid: 0, RelOid: 6100, Fork: storage.MainFork}
	cols := executor.PGSubscriptionColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_subscription",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return &catalog.Subscription{
				OID:          uint32(decoded[0].Int),
				DBOid:        uint32(decoded[1].Int),
				Name:         decoded[3].StringValue(),
				Owner:        uint32(decoded[4].Int),
				Enabled:      decoded[5].BoolValue(),
				Conninfo:     decoded[13].StringValue(),
				SlotName:     decoded[14].StringValue(),
				Publications: executor.ParseTextArrayLiteral(decoded[16].StringValue()),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		pubsub.CreateSubscriptionDuringRecovery(r.(*catalog.Subscription))
	}
	return nil
}

// reloadRoleMembershipsFromHeap is B4.3's pg_auth_members reload — the generic
// heap-scan replacement for the retired replayRoleMembershipRecords scanner
// (RecordKinds 79/80). pg_auth_members is SHARED (global/1261). Each live row
// is re-registered into the roleMembers registry with its original OID and
// option flags; revoked rows carry xmax and are skipped by the liveness filter.
func reloadRoleMembershipsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	rel := storage.RelFileNode{DBOid: 0, RelOid: 1261, Fork: storage.MainFork}
	cols := executor.PGAuthMembersColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_auth_members",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return catalog.RoleMembership{
				OID:           uint32(decoded[0].Int),
				RoleOID:       uint32(decoded[1].Int),
				MemberOID:     uint32(decoded[2].Int),
				GrantorOID:    uint32(decoded[3].Int),
				AdminOption:   decoded[4].BoolValue(),
				InheritOption: decoded[5].BoolValue(),
				SetOption:     decoded[6].BoolValue(),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, r := range rows {
		cat.RegisterRoleMembershipDuringRecovery(r.(catalog.RoleMembership))
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
			// B2.2 slice 2: prokind='a' rows are aggregates — they belong to
			// the userAggregates registry (reloadUserAggregatesFromHeap),
			// not Routines; registering one here would shadow the aggregate
			// in function resolution.
			if r.KindChar == "a" {
				return nil, false, errSkipBuiltinRow
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

// reloadSequencesFromHeap is B1.3b's sequence reload — the generic
// heap-scan replacement for the retired replaySequenceDDLRecords scanner
// (RecordKinds 65/66). The DEFINITION comes from the pg_sequence heap row,
// the COUNTER from the sequence relation's physical page (block 0, rebuilt
// by XLOG_SEQ_LOG replay), OWNED BY from the narrow pg_depend rows, and
// identity/serial column state from the pg_attribute reload (attidentity)
// plus name-derived serial spelling. Also seeds the pg_sequence TID cache.
//
// Pre-B1.3b data dirs (sequences journaled only via kind-65) need re-init:
// their counters have no physical page and the WAL scanner is gone.
func reloadSequencesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	// OWNED BY map: seqrelid → (tableOID, attnum) from pg_depend auto rows.
	depCols := executor.PGDependColumnsPG18()
	type depRow struct{ objid, refobjid, refsubid uint32 }
	depRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2608, Fork: storage.MainFork}
	depRaw, err := scanCatalogHeapRows(mgr, depRel, clog, "pg_depend",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(depCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, depCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) != catalog.RelationRelationId || decoded[6].StringValue() != "a" {
				return nil, false, errSkipBuiltinRow
			}
			return depRow{
				objid:    uint32(decoded[1].Int),
				refobjid: uint32(decoded[4].Int),
				refsubid: uint32(decoded[5].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	owned := make(map[uint32]depRow, len(depRaw))
	for _, raw := range depRaw {
		d := raw.(depRow)
		owned[d.objid] = d
	}

	type seqRow struct {
		decoded executor.Row
		tid     storage.ItemPointer
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
			return seqRow{decoded: decoded, tid: tid}, false, nil
		})
	if err != nil {
		return err
	}
	page := make(storage.Page, storage.BlockSize)
	for _, raw := range rows {
		sr := raw.(seqRow)
		d := sr.decoded
		seqrelid := uint32(d[0].Int)
		tbl, dbOid, ok := cat.LookupTableByOIDAllDBs(seqrelid)
		if !ok || tbl == nil || !tbl.IsSequence {
			continue
		}
		name := tbl.Name
		if tbl.Schema != "" && tbl.Schema != "public" {
			name = tbl.Schema + "." + tbl.Name
		}
		// Counter from the physical sequence page — it lives at the
		// USER-DATA location base/<cat.DBOID()> (the session's physical
		// database), not the catalog-heap base/1 + mirror scheme.
		seqFile := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: seqrelid, Fork: storage.MainFork}
		last, called := int64(0), false
		haveCounter := false
		if n, nerr := mgr.NBlocks(seqFile); nerr == nil && n > 0 {
			if rerr := mgr.ReadBlock(seqFile, 0, page); rerr == nil {
				if ht, herr := storage.PageGetHeapTuple(page, 1); herr == nil && len(ht.Data) >= 17 {
					last = int64(binary.LittleEndian.Uint64(ht.Data[0:8]))
					called = ht.Data[16] != 0
					haveCounter = true
				}
			}
		}
		p := wal.SequenceStatePayload{
			Name:      name,
			Start:     d[2].Int,
			Increment: d[3].Int,
			Max:       d[4].Int,
			Min:       d[5].Int,
			Cache:     d[6].Int,
			Cycle:     d[7].BoolValue(),
			DataType:  catalog.OIDToTypeName(uint32(d[1].Int)),
			DBOid:     dbOid,
		}
		if haveCounter {
			p.Current, p.Called = last, called
		} else {
			p.Current, p.Called = p.Start, false
		}
		// OWNED BY + identity/serial markers from pg_depend + attidentity.
		if dep, ok := owned[seqrelid]; ok {
			if ownTbl, _, ok2 := cat.LookupTableByOIDAllDBs(dep.refobjid); ok2 && ownTbl != nil &&
				int(dep.refsubid) >= 1 && int(dep.refsubid) <= len(ownTbl.Columns) {
				col := &ownTbl.Columns[dep.refsubid-1]
				ownName := ownTbl.Name
				if ownTbl.Schema != "" && ownTbl.Schema != "public" {
					ownName = ownTbl.Schema + "." + ownTbl.Name
				}
				p.OwnedBy = ownName + "." + col.Name
				if col.IdentityColumn {
					p.IdentityKind = 1
					if col.IdentityAlways {
						p.IdentityKind = 2
					}
				} else {
					// Serial spelling derives from the base int type; the
					// auto-increment INSERT path keys on Type.Name.
					switch strings.ToLower(col.Type.Name) {
					case "int2", "smallint":
						p.ColSpelling = "smallserial"
					case "int8", "bigint":
						p.ColSpelling = "bigserial"
					case "int4", "integer", "int":
						p.ColSpelling = "serial"
					}
				}
			}
		}
		executor.RestoreSequenceFromWAL(p)
		executor.CreateSequenceCatalogRelation(cat, parser.ObjectName{Schema: tbl.Schema, Name: tbl.Name}, name, dbOid)
		// Restore the owning column's serial spelling (heap-reloaded columns
		// read back as the base integer type).
		if p.ColSpelling != "" {
			if dep, ok := owned[seqrelid]; ok {
				if ownTbl, _, ok2 := cat.LookupTableByOIDAllDBs(dep.refobjid); ok2 && ownTbl != nil &&
					int(dep.refsubid) >= 1 && int(dep.refsubid) <= len(ownTbl.Columns) {
					ownTbl.Columns[dep.refsubid-1].Type.Name = p.ColSpelling
				}
			}
		}
		executor.SeedSequenceHeapTID(name, dbOid, seqrelid,
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
			sort, _ := strconv.ParseFloat(decoded[2].Format(), 64)
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

// reloadUserCastsFromHeap is B2.2a's cast reload — the generic heap-scan
// replacement for the retired replayCastDDLRecords scanner (RecordKinds
// 38/39). Fully physical: the six pg_cast columns map 1:1 onto the Cast
// registry; source/target type names revive via pgTypeCanonical for
// builtins and the user pg_type rows otherwise.
func reloadUserCastsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	castCols := executor.PGCastColumnsPG18()
	type castRow struct {
		oid, source, target, funcOID uint32
		context, method              string
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2605, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_cast",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(castCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, castCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return castRow{
				oid:     uint32(decoded[0].Int),
				source:  uint32(decoded[1].Int),
				target:  uint32(decoded[2].Int),
				funcOID: uint32(decoded[3].Int),
				context: decoded[4].StringValue(),
				method:  decoded[5].StringValue(),
			}, false, nil
		})
	if err != nil || len(rows) == 0 {
		return err
	}
	// User-type OID → name (domains/enums/etc. as cast endpoints).
	typeName := func(oid uint32) string {
		if e, ok := pgTypeCanonical(oid); ok {
			return e.Name
		}
		if tbl, _, ok := cat.LookupTableByOIDAllDBs(oid); ok && tbl != nil {
			return tbl.Name
		}
		if d, ok := cat.LookupDomainByOID(oid); ok && d != nil {
			return d.Name
		}
		if et, ok := cat.LookupEnumByOID(oid); ok && et != nil {
			return et.Name
		}
		return ""
	}
	for _, raw := range rows {
		cr := raw.(castRow)
		src, tgt := typeName(cr.source), typeName(cr.target)
		if src == "" || tgt == "" {
			continue // endpoint type gone (dropped) — dead cast
		}
		cat.RegisterCastDuringRecovery(src, tgt, cr.context, cr.method, cr.oid, cr.funcOID)
	}
	return nil
}

// reloadUserAggregatesFromHeap is B2.2 slice 2's aggregate reload — the
// generic heap-scan replacement for the retired replayAggregateDDLRecords
// scanner (RecordKinds 46-49). Aggregates journal as prokind='a' pg_proc
// rows whose proargdefaults JSON meta carries the full UserAggregate
// definition (Routine.Aggregate — the physical pg_aggregate columns store
// proc OIDs, which are 0 for builtin transition functions outside the
// hand-curated BuiltinProc set, so an OID reversal would be lossy exactly
// where it matters). The pg_aggregate row's own liveness needs no separate
// check: DROP AGGREGATE stamps xmax on BOTH rows in the same transaction.
// Must run after reloadUserRoutinesFromHeap (which skips prokind='a' rows)
// and after the schema reload (RegisterUserAggregateDuringRecovery resolves
// NamespaceOID by schema name).
func reloadUserAggregatesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	rs := cat.Routines()
	if rs == nil {
		return nil
	}
	type aggRow struct {
		r   *catalog.Routine
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 1255, Fork: storage.MainFork}
	cols := executor.PGProcColumnsPG18()
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_proc(aggregates)",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			r, derr := executor.DecodePGProcArgMeta(decoded[23].StringValue(), decoded[25].StringValue())
			if derr != nil {
				return nil, false, derr
			}
			if r.KindChar != "a" || r.Aggregate == nil {
				return nil, false, errSkipBuiltinRow
			}
			return aggRow{r: r, tid: tid}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		ar := raw.(aggRow)
		cat.RegisterUserAggregateDuringRecovery(ar.r.Aggregate, ar.r.Schema)
		// Seed the routine funnel's TID cache so a post-restart ALTER
		// AGGREGATE RENAME/OWNER updates the pg_proc row in place.
		rs.SetHeapTID(ar.r.OID, catalog.SchemaHeapTID{Block: uint32(ar.tid.Block), Offset: ar.tid.Offset})
	}
	return nil
}

// reloadUserOperatorsFromHeap is B2.2 slice 3's operator reload — the
// generic heap-scan replacement for the retired replayOperatorDDLRecords
// scanner (RecordKinds 83/84). Fully physical: every proc link
// (oprcode/oprrest/oprjoin) and the commutator/negator OIDs are stored as
// OIDs in the UserOperator registry too, so only the two argument type
// NAMES (oprleft/oprright) and the schema name (oprnamespace) reverse via
// lookups. Shell operators (oprcode=0) reload as shells, exactly as the
// pre-crash server held them. Seeds the operator TID cache so a post-restart
// back-patch or shell fill-in updates the row in place.
func reloadUserOperatorsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	opCols := executor.PGOperatorColumnsPG18()
	type opRow struct {
		op  catalog.UserOperator
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2617, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_operator",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(opCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, opCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return opRow{
				op: catalog.UserOperator{
					OID:           uint32(decoded[0].Int),
					Name:          decoded[1].StringValue(),
					NamespaceOID:  uint32(decoded[2].Int),
					Owner:         uint32(decoded[3].Int),
					CanMerge:      decoded[5].BoolValue(),
					CanHash:       decoded[6].BoolValue(),
					LeftType:      reloadTypeNameForOID(cat, uint32(decoded[7].Int)),
					RightType:     reloadTypeNameForOID(cat, uint32(decoded[8].Int)),
					CommutatorOID: uint32(decoded[10].Int),
					NegatorOID:    uint32(decoded[11].Int),
					FuncOID:       uint32(decoded[12].Int),
					RestrictOID:   uint32(decoded[13].Int),
					JoinOID:       uint32(decoded[14].Int),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		or := raw.(opRow)
		schema := cat.SchemaNameForOID(or.op.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.RegisterUserOperatorDuringRecovery(&or.op, schema)
		cat.SetOperatorHeapTID(or.op.OID, catalog.SchemaHeapTID{Block: uint32(or.tid.Block), Offset: or.tid.Offset})
	}
	return nil
}

// reloadTypeNameForOID reverses a pg_type OID to the name the string-keyed
// registries store — the cast-reload pattern (builtin → table/composite →
// domain → enum). "" for InvalidOid (a unary operator's absent side).
func reloadTypeNameForOID(cat *catalog.InMemory, oid uint32) string {
	if oid == 0 {
		return ""
	}
	if e, ok := pgTypeCanonical(oid); ok {
		return e.Name
	}
	if tbl, _, ok := cat.LookupTableByOIDAllDBs(oid); ok && tbl != nil {
		return tbl.Name
	}
	if d, ok := cat.LookupDomainByOID(oid); ok && d != nil {
		return d.Name
	}
	if et, ok := cat.LookupEnumByOID(oid); ok && et != nil {
		return et.Name
	}
	return ""
}

// reloadUserCollationsFromHeap is B2.2 slice 4's collation reload — the
// generic heap-scan replacement for the retired replayCollationDDLRecords
// scanner (RecordKinds 42-45/93). Fully physical: FormData_pg_collation maps
// 1:1 onto UserCollation (NULL text columns ↔ "" registry fields). Each
// collation registers under DefaultDBOid — NamespaceDBOid maps postgres-DB
// sessions to DefaultDBOid for namespace-scoped registries, so that IS what
// live lookups key on (unlike the domain/enum registries, which key on the
// resolved session DB; verified empirically — registering under cat.DBOID()
// made every post-restart lookup miss). Cross-database collations remain
// non-dbOid-aware (pre-existing ledger row).
func reloadUserCollationsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	collCols := executor.PGCollationColumnsPG18()
	type collRow struct {
		uc  catalog.UserCollation
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3456, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_collation",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(collCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, collCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			provider := byte('c')
			if p := decoded[4].StringValue(); p != "" {
				provider = p[0]
			}
			return collRow{
				uc: catalog.UserCollation{
					OID:           uint32(decoded[0].Int),
					Name:          decoded[1].StringValue(),
					NamespaceOID:  uint32(decoded[2].Int),
					Owner:         uint32(decoded[3].Int),
					Provider:      provider,
					Deterministic: decoded[5].BoolValue(),
					Encoding:      int(int32(decoded[6].Int)),
					Collate:       decoded[7].StringValue(),
					Ctype:         decoded[8].StringValue(),
					Locale:        decoded[9].StringValue(),
					Rules:         decoded[10].StringValue(),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		cr := raw.(collRow)
		schema := cat.SchemaNameForOID(cr.uc.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.CreateCollationDuringRecovery(&cr.uc, schema)
		cat.SetCollationHeapTID(cr.uc.OID, catalog.SchemaHeapTID{Block: uint32(cr.tid.Block), Offset: cr.tid.Offset})
	}
	return nil
}

// reloadUserConversionsFromHeap is the pg_conversion twin (RecordKinds
// 40/41/130-132 retired). conproc stays an OID in the registry; the
// dump-facing ProcSchema/ProcName fallback re-derives from it via the
// routines registry (user funcs) or the curated builtin set — "" when
// neither resolves (the virtual view then renders from FuncOID, which is
// the authoritative source anyway).
func reloadUserConversionsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	convCols := executor.PGConversionColumnsPG18()
	type convRow struct {
		uc  catalog.UserConversion
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2607, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_conversion",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(convCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, convCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return convRow{
				uc: catalog.UserConversion{
					OID:          uint32(decoded[0].Int),
					Name:         decoded[1].StringValue(),
					NamespaceOID: uint32(decoded[2].Int),
					Owner:        uint32(decoded[3].Int),
					ForEncoding:  int32(decoded[4].Int),
					ToEncoding:   int32(decoded[5].Int),
					FuncOID:      uint32(decoded[6].Int),
					Default:      decoded[7].BoolValue(),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	rs := cat.Routines()
	for _, raw := range rows {
		cr := raw.(convRow)
		if cr.uc.FuncOID != 0 {
			if rs != nil {
				if r := rs.LookupByOID(cr.uc.FuncOID); r != nil {
					cr.uc.ProcSchema, cr.uc.ProcName = r.Schema, r.Name
				}
			}
			if cr.uc.ProcName == "" {
				if bp, ok := catalog.LookupBuiltinProcByOID(cr.uc.FuncOID); ok {
					cr.uc.ProcSchema, cr.uc.ProcName = "pg_catalog", bp.Name
				}
			}
		}
		schema := cat.SchemaNameForOID(cr.uc.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.CreateConversionDuringRecovery(&cr.uc, schema)
		cat.SetConversionHeapTID(cr.uc.OID, catalog.SchemaHeapTID{Block: uint32(cr.tid.Block), Offset: cr.tid.Offset})
	}
	return nil
}

// reloadOpClassFamilyFromHeap is B2.2 slice 5's operator-class/family
// reload — the generic heap-scan replacement for the retired
// replayOperatorClassDDLRecords scanner (RecordKinds 85-92). It rebuilds all
// four registries from their heaps in dependency order: pg_opfamily →
// pg_opclass (needs its family) → pg_amop / pg_amproc (need nothing; every
// link is an OID).
//
// The member structs' ClassOID has no pg_amop/pg_amproc column — PG records
// that attribution as an INTERNAL pg_depend row on the opclass
// (opclasscmds.c storeOperators), and goopg now journals the same row, so
// this reload re-derives ClassOID from pg_depend. A member with no such row
// is an ALTER OPERATOR FAMILY ADD member, whose zero ClassOID is correct
// (PG gives those an AUTO dependency on the family instead).
//
// Registries key on DefaultDBOid for postgres-DB sessions (NamespaceDBOid),
// so the reload leaves DBOid unset and the *DuringRecovery zero-fallback
// supplies it — the B2.2d rule.
func reloadOpClassFamilyFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	if err := reloadOpFamiliesFromHeap(mgr, cat, clog); err != nil {
		return err
	}
	if err := reloadOpClassesFromHeap(mgr, cat, clog); err != nil {
		return err
	}
	classByMember, err := scanAmMemberClassDepends(mgr, cat, clog)
	if err != nil {
		return err
	}
	if err := reloadAmOpMembersFromHeap(mgr, cat, clog, classByMember); err != nil {
		return err
	}
	return reloadAmProcMembersFromHeap(mgr, cat, clog, classByMember)
}

func reloadOpFamiliesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGOpfamilyColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2753, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_opfamily",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.UserOperatorFamily{
				OID:          uint32(decoded[0].Int),
				Method:       uint32(decoded[1].Int),
				Name:         decoded[2].StringValue(),
				NamespaceOID: uint32(decoded[3].Int),
				Owner:        uint32(decoded[4].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		fam := raw.(catalog.UserOperatorFamily)
		schema := cat.SchemaNameForOID(fam.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.RegisterUserOperatorFamilyDuringRecovery(&fam, schema)
	}
	return nil
}

func reloadOpClassesFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGOpclassColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2616, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_opclass",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.UserOperatorClass{
				OID:          uint32(decoded[0].Int),
				Method:       uint32(decoded[1].Int),
				Name:         decoded[2].StringValue(),
				NamespaceOID: uint32(decoded[3].Int),
				Owner:        uint32(decoded[4].Int),
				FamilyOID:    uint32(decoded[5].Int),
				InTypeOID:    uint32(decoded[6].Int),
				IsDefault:    decoded[7].BoolValue(),
				KeyTypeOID:   uint32(decoded[8].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		oc := raw.(catalog.UserOperatorClass)
		schema := cat.SchemaNameForOID(oc.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.RegisterUserOperatorClassDuringRecovery(&oc, schema)
	}
	return nil
}

// scanAmMemberClassDepends returns (member classid, member oid) → owning
// opclass OID, read from the INTERNAL pg_depend rows the member writers
// journal (see executor.writeAmMemberClassDependRow).
func scanAmMemberClassDepends(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) (map[[2]uint32]uint32, error) {
	cols := executor.PGDependColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2608, Fork: storage.MainFork}
	type dep struct {
		classID, objID, refObjID uint32
	}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_depend(opclass members)",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			classID := uint32(decoded[0].Int)
			if (classID != 2602 && classID != 2603) ||
				uint32(decoded[3].Int) != 2616 || decoded[6].StringValue() != "i" {
				return nil, false, errSkipBuiltinRow // not an opclass-member attribution row
			}
			return dep{classID: classID, objID: uint32(decoded[1].Int), refObjID: uint32(decoded[4].Int)}, false, nil
		})
	if err != nil {
		return nil, err
	}
	out := make(map[[2]uint32]uint32, len(rows))
	for _, raw := range rows {
		d := raw.(dep)
		out[[2]uint32{d.classID, d.objID}] = d.refObjID
	}
	return out, nil
}

func reloadAmOpMembersFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog, classByMember map[[2]uint32]uint32) error {
	cols := executor.PGAmopColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2602, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_amop",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.AmOpMember{
				OID:           uint32(decoded[0].Int),
				FamilyOID:     uint32(decoded[1].Int),
				LeftType:      uint32(decoded[2].Int),
				RightType:     uint32(decoded[3].Int),
				Strategy:      uint32(decoded[4].Int),
				OperOID:       uint32(decoded[6].Int),
				Method:        uint32(decoded[7].Int),
				SortFamilyOID: uint32(decoded[8].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		m := raw.(catalog.AmOpMember)
		m.ClassOID = classByMember[[2]uint32{2602, m.OID}]
		cat.RegisterAmOpMemberDuringRecovery(&m)
	}
	return nil
}

func reloadAmProcMembersFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog, classByMember map[[2]uint32]uint32) error {
	cols := executor.PGAmprocColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2603, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_amproc",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.AmProcMember{
				OID:       uint32(decoded[0].Int),
				FamilyOID: uint32(decoded[1].Int),
				LeftType:  uint32(decoded[2].Int),
				RightType: uint32(decoded[3].Int),
				ProcNum:   uint32(decoded[4].Int),
				ProcOID:   uint32(decoded[5].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	// pg_amproc has no amprocmethod column (unlike pg_amop's amopmethod) —
	// AmProcMember.Method (goopg's own field, feeding
	// amForcesSoftFunctionDependency) re-derives from the owning family.
	for _, raw := range rows {
		m := raw.(catalog.AmProcMember)
		m.ClassOID = classByMember[[2]uint32{2603, m.OID}]
		if fam := cat.LookupUserOperatorFamilyByOID(m.FamilyOID); fam != nil {
			m.Method = fam.Method
		}
		cat.RegisterAmProcMemberDuringRecovery(&m)
	}
	return nil
}

// reloadUserTransformsFromHeap is B3.1's transform reload — the generic
// heap-scan replacement for the retired replayTransformDDLRecords scanner
// (RecordKinds 36/37). Fully physical: the five pg_transform columns are all
// OIDs, and the registry's two name fields reverse from trftype/trflang
// (the cast-reload type-name pattern; the language name comes from the same
// four-way builtin map LanguageNameToOID renders forward).
func reloadUserTransformsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGTransformColumnsPG18()
	type trfRow struct {
		oid, typeOID, langOID, fromFn, toFn uint32
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3576, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_transform",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return trfRow{
				oid:     uint32(decoded[0].Int),
				typeOID: uint32(decoded[1].Int),
				langOID: uint32(decoded[2].Int),
				fromFn:  uint32(decoded[3].Int),
				toFn:    uint32(decoded[4].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		tr := raw.(trfRow)
		typeName := reloadTypeNameForOID(cat, tr.typeOID)
		lang := languageNameForOID(tr.langOID)
		if typeName == "" || lang == "" {
			continue // endpoint type or language gone — dead transform
		}
		cat.RegisterTransformDuringRecovery(typeName, lang, tr.oid, tr.fromFn, tr.toFn)
	}
	return nil
}

// languageNameForOID reverses catalog.LanguageNameToOID (the four languages
// goopg models). "" when the OID is not one of them.
func languageNameForOID(oid uint32) string {
	for _, name := range []string{"internal", "c", "sql", "plpgsql"} {
		if catalog.LanguageNameToOID(name) == oid {
			return name
		}
	}
	return ""
}

// reloadUserEventTriggersFromHeap is B3.2's event-trigger reload — the
// generic heap-scan replacement for the retired replayEventTriggerDDLRecords
// scanner (RecordKinds 56-60). Six scalar columns map 1:1 onto EventTrigger;
// the evttags text[] column decodes to the canonical "{a,b}" text, which
// ParseTextArrayLiteral splits back to the registry's Tags slice (NULL → nil,
// no WHEN TAG filter). Seeds the TID cache so a post-restart ALTER updates
// the row in place.
func reloadUserEventTriggersFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGEventTriggerColumnsPG18()
	type etRow struct {
		et  catalog.EventTrigger
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3466, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_event_trigger",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			var tags []string
			if lit := decoded[6].StringValue(); lit != "" && lit != "{}" {
				tags = executor.ParseTextArrayLiteral(lit)
			}
			return etRow{
				et: catalog.EventTrigger{
					OID:     uint32(decoded[0].Int),
					Name:    decoded[1].StringValue(),
					Event:   decoded[2].StringValue(),
					Owner:   uint32(decoded[3].Int),
					FuncOID: uint32(decoded[4].Int),
					Enabled: decoded[5].StringValue(),
					Tags:    tags,
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		er := raw.(etRow)
		cat.RegisterEventTriggerDuringRecovery(&er.et)
		cat.SetEventTriggerHeapTID(er.et.OID, catalog.SchemaHeapTID{Block: uint32(er.tid.Block), Offset: er.tid.Offset})
	}
	return nil
}

// reloadUserPublicationsFromHeap is B3.3's publication reload — the generic
// heap-scan replacement for the pg_publication half of the retired
// replayPubSubDDLRecords scanner (kinds 50-52; subscription kinds 53-55 stay
// bespoke, pg_subscription being a SHARED catalog for B4). It scans
// pg_publication for base rows and pg_publication_rel for FOR TABLE members
// (prrelid → the qualified table name via cat.LookupTableByOID), then
// registers each into the PubSub registry. Seeds the InMemory TID cache so a
// post-restart ALTER PUBLICATION OWNER updates the base row in place.
func reloadUserPublicationsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, pubsub *catalog.PubSub, clog *mvcc.CLog) error {
	// Pass 1: member rows, grouped by publication OID.
	membersByPub := map[uint32][]string{}
	prCols := executor.PGPublicationRelColumnsPG18()
	prRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 6106, Fork: storage.MainFork}
	prRows, err := scanCatalogHeapRows(mgr, prRel, clog, "pg_publication_rel",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(prCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, prCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			return [2]uint32{uint32(decoded[1].Int), uint32(decoded[2].Int)}, false, nil // {prpubid, prrelid}
		})
	if err != nil {
		return err
	}
	for _, raw := range prRows {
		pr := raw.([2]uint32)
		if tbl, ok := cat.LookupTableByOID(pr[1]); ok && tbl != nil {
			membersByPub[pr[0]] = append(membersByPub[pr[0]], tbl.QualifiedName())
		}
	}

	// Pass 2: base rows.
	pubCols := executor.PGPublicationColumnsPG18()
	pubRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 6104, Fork: storage.MainFork}
	type pubRow struct {
		pub catalog.Publication
		tid storage.ItemPointer
	}
	rows, err := scanCatalogHeapRows(mgr, pubRel, clog, "pg_publication",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			decoded := make(executor.Row, len(pubCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(decoded, pubCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(decoded[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return pubRow{
				pub: catalog.Publication{
					OID:           uint32(decoded[0].Int),
					Name:          decoded[1].StringValue(),
					Owner:         uint32(decoded[2].Int),
					AllTables:     decoded[3].BoolValue(),
					PublishInsert: decoded[4].BoolValue(),
					PublishUpdate: decoded[5].BoolValue(),
					PublishDelete: decoded[6].BoolValue(),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		pr := raw.(pubRow)
		pr.pub.Tables = membersByPub[pr.pub.OID]
		pubsub.CreatePublicationDuringRecovery(&pr.pub)
		cat.SetPublicationHeapTID(pr.pub.OID, catalog.SchemaHeapTID{Block: uint32(pr.tid.Block), Offset: pr.tid.Offset})
	}
	return nil
}

// reloadForeignDataFromHeap is B3.4's foreign-data reload — the generic
// heap-scan replacement for the retired replayForeignServerDDLRecords /
// replayUserMappingDDLRecords scanners (RecordKinds 126-129). It reloads all
// three catalogs in dependency order: pg_foreign_data_wrapper (gained
// durability in B3.4 — none before) → pg_foreign_server (srvfdw OID reversed
// to the FDW name) → pg_user_mapping (umserver OID → server name, umuser OID
// → role name). Options are text[] columns decoded to "{a,b}" and split via
// ParseTextArrayLiteral. Per-database catalogs, so the reload keys under
// DefaultDBOid (the registries' resolveDBOid default), matching live writes.
func reloadForeignDataFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	if err := reloadFdwsFromHeap(mgr, cat, clog); err != nil {
		return err
	}
	if err := reloadForeignServersFromHeap(mgr, cat, clog); err != nil {
		return err
	}
	return reloadUserMappingsFromHeap(mgr, cat, clog)
}

func decodeOptions(d executor.Datum) []string {
	if lit := d.StringValue(); lit != "" && lit != "{}" {
		return executor.ParseTextArrayLiteral(lit)
	}
	return nil
}

func reloadFdwsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGForeignDataWrapperColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2328, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_foreign_data_wrapper",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.ForeignDataWrapper{
				OID:          uint32(d[0].Int),
				Name:         d[1].StringValue(),
				Owner:        uint32(d[2].Int),
				HandlerOID:   uint32(d[3].Int),
				ValidatorOID: uint32(d[4].Int),
				Options:      decodeOptions(d[6]),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		fdw := raw.(catalog.ForeignDataWrapper)
		cat.RegisterForeignDataWrapperDuringRecovery(&fdw)
	}
	return nil
}

func reloadForeignServersFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGForeignServerColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 1417, Fork: storage.MainFork}
	type srvRow struct {
		name, fdwName, srvType, srvVersion string
		options                            []string
		oid                                uint32
	}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_foreign_server",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			fdwName := ""
			if fdw := cat.LookupForeignDataWrapperByOID(uint32(d[3].Int)); fdw != nil {
				fdwName = fdw.Name
			}
			return srvRow{
				name:       d[1].StringValue(),
				fdwName:    fdwName,
				srvType:    d[4].StringValue(),
				srvVersion: d[5].StringValue(),
				options:    decodeOptions(d[7]),
				oid:        uint32(d[0].Int),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		sr := raw.(srvRow)
		cat.RegisterForeignServerDuringRecovery(sr.name, sr.fdwName, sr.srvType, sr.srvVersion, sr.options, sr.oid)
	}
	return nil
}

func reloadUserMappingsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGUserMappingColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 1418, Fork: storage.MainFork}
	type umRow struct {
		user, server string
		options      []string
		oid          uint32
	}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_user_mapping",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			user := "public"
			if umuser := uint32(d[1].Int); umuser != 0 {
				if name := cat.RoleNameForOID(umuser); name != "" {
					user = name
				}
			}
			server := ""
			if srv := cat.LookupForeignServerByOID(uint32(d[2].Int)); srv != nil {
				server = srv.Name
			}
			return umRow{user: user, server: server, options: decodeOptions(d[3]), oid: uint32(d[0].Int)}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		ur := raw.(umRow)
		if ur.server == "" {
			continue // server gone — dead mapping
		}
		cat.RegisterUserMappingDuringRecovery(ur.user, ur.server, ur.options, ur.oid)
	}
	return nil
}

// reloadUserTSDictsFromHeap is B3.5's text-search-dictionary reload — the
// generic heap-scan replacement for the retired replayTSDictDDLRecords
// scanner (RecordKinds 104/105/114/115/116). All six pg_ts_dict columns map
// 1:1 onto UserTSDict (dicttemplate is already an OID, dictinitoption the
// serialized text), so the reload is fully physical; only dictnamespace
// reverses to a schema name for CreateTSDictDuringRecovery. Seeds the TID
// cache so a post-restart ALTER updates the row in place.
func reloadUserTSDictsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGTSDictColumnsPG18()
	type dictRow struct {
		ud  catalog.UserTSDict
		tid storage.ItemPointer
	}
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3600, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_ts_dict",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return dictRow{
				ud: catalog.UserTSDict{
					OID:          uint32(d[0].Int),
					Name:         d[1].StringValue(),
					NamespaceOID: uint32(d[2].Int),
					Owner:        uint32(d[3].Int),
					Template:     uint32(d[4].Int),
					InitOption:   d[5].StringValue(),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		dr := raw.(dictRow)
		schema := cat.SchemaNameForOID(dr.ud.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.CreateTSDictDuringRecovery(&dr.ud, schema)
		cat.SetTSDictHeapTID(dr.ud.OID, catalog.SchemaHeapTID{Block: uint32(dr.tid.Block), Offset: dr.tid.Offset})
	}
	return nil
}

// reloadUserTSConfigsFromHeap is B3.6's text-search-configuration reload —
// the generic heap-scan replacement for the retired replayTSConfigDDLRecords
// scanner (RecordKinds 106-113). It reads the base rows from pg_ts_config and
// the mapping rows from pg_ts_config_map (grouped by mapcfg → the config OID,
// token type reversed via TSTokenTypeAlias, dictionaries in mapseqno order),
// assembles each UserTSConfig with its inline Mappings, and registers it via
// CreateTSConfigDuringRecovery. Runs after the pg_ts_dict reload so a
// mapping's mapdict OID is a dictionary the registry already knows (though
// mapdict stays an OID in the mapping regardless). Seeds the base-row TID
// cache for post-restart ALTER RENAME / SET SCHEMA.
func reloadUserTSConfigsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	// Pass 1: config_map rows, grouped by mapcfg then token type.
	type mapKey struct {
		cfgOID  uint32
		tokType int32
	}
	mapRowsByCfg := map[uint32]map[int32]map[int32]uint32{} // cfgOID → tokType → seqno → dictOID
	mapCols := executor.PGTSConfigMapColumnsPG18()
	mapRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3603, Fork: storage.MainFork}
	if _, err := scanCatalogHeapRows(mgr, mapRel, clog, "pg_ts_config_map",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(mapCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, mapCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			k := mapKey{cfgOID: uint32(d[0].Int), tokType: int32(d[1].Int)}
			if mapRowsByCfg[k.cfgOID] == nil {
				mapRowsByCfg[k.cfgOID] = map[int32]map[int32]uint32{}
			}
			if mapRowsByCfg[k.cfgOID][k.tokType] == nil {
				mapRowsByCfg[k.cfgOID][k.tokType] = map[int32]uint32{}
			}
			mapRowsByCfg[k.cfgOID][k.tokType][int32(d[2].Int)] = uint32(d[3].Int)
			return struct{}{}, false, nil
		}); err != nil {
		return err
	}

	// Pass 2: base rows.
	cfgCols := executor.PGTSConfigColumnsPG18()
	cfgRel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 3602, Fork: storage.MainFork}
	type cfgRow struct {
		uc  catalog.UserTSConfig
		tid storage.ItemPointer
	}
	rows, err := scanCatalogHeapRows(mgr, cfgRel, clog, "pg_ts_config",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cfgCols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cfgCols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return cfgRow{
				uc: catalog.UserTSConfig{
					OID:          uint32(d[0].Int),
					Name:         d[1].StringValue(),
					NamespaceOID: uint32(d[2].Int),
					Owner:        uint32(d[3].Int),
					Parser:       uint32(d[4].Int),
					Mappings:     buildTSConfigMappings(mapRowsByCfg[uint32(d[0].Int)]),
				},
				tid: tid,
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		cr := raw.(cfgRow)
		schema := cat.SchemaNameForOID(cr.uc.NamespaceOID)
		if schema == "" {
			schema = "public"
		}
		cat.CreateTSConfigDuringRecovery(&cr.uc, schema)
		cat.SetTSConfigHeapTID(cr.uc.OID, catalog.SchemaHeapTID{Block: uint32(cr.tid.Block), Offset: cr.tid.Offset})
	}
	return nil
}

// buildTSConfigMappings turns the (tokType → seqno → dictOID) map for one
// configuration into the inline TSConfigMapping slice, ordering dictionaries
// by mapseqno and token types by their numeric value.
func buildTSConfigMappings(byTok map[int32]map[int32]uint32) []catalog.TSConfigMapping {
	if len(byTok) == 0 {
		return nil
	}
	tokTypes := make([]int32, 0, len(byTok))
	for tt := range byTok {
		tokTypes = append(tokTypes, tt)
	}
	sort.Slice(tokTypes, func(i, j int) bool { return tokTypes[i] < tokTypes[j] })
	out := make([]catalog.TSConfigMapping, 0, len(tokTypes))
	for _, tt := range tokTypes {
		alias := catalog.TSTokenTypeAlias(int(tt))
		if alias == "" {
			continue
		}
		bySeq := byTok[tt]
		seqs := make([]int32, 0, len(bySeq))
		for s := range bySeq {
			seqs = append(seqs, s)
		}
		sort.Slice(seqs, func(i, j int) bool { return seqs[i] < seqs[j] })
		dicts := make([]uint32, 0, len(seqs))
		for _, s := range seqs {
			dicts = append(dicts, bySeq[s])
		}
		out = append(out, catalog.TSConfigMapping{TokenType: alias, DictOIDs: dicts})
	}
	return out
}

// reloadUserAccessMethodsFromHeap is B3.7's access-method reload — the
// generic heap-scan replacement for the retired replayAccessMethodDDLRecords
// scanner (RecordKinds 70/71). It seq-scans the pg_am heap (no index exists;
// see sys_pg_am.go), skips the built-in rows (oid < FirstUserOID), and
// re-registers each user access method. amtype and amhandler are stored
// directly (char / pg_proc OID).
func reloadUserAccessMethodsFromHeap(mgr *storage.Manager, cat *catalog.InMemory, clog *mvcc.CLog) error {
	cols := executor.PGAccessMethodColumnsPG18()
	rel := storage.RelFileNode{DBOid: cat.DBOID(), RelOid: 2601, Fork: storage.MainFork}
	rows, err := scanCatalogHeapRows(mgr, rel, clog, "pg_am",
		func(ht storage.HeapTuple, tid storage.ItemPointer) (any, bool, error) {
			natts := int(ht.Header.Infomask2 & storage.HeapNattsMask)
			d := make(executor.Row, len(cols))
			if derr := executor.DecodeRowIntoMctxPGTuple(d, cols, ht.Data, ht.Bitmap, natts, nil); derr != nil {
				return nil, false, derr
			}
			if uint32(d[0].Int) < catalog.FirstUserOID {
				return nil, false, errSkipBuiltinRow
			}
			return catalog.AccessMethod{
				OID:        uint32(d[0].Int),
				Name:       d[1].StringValue(),
				HandlerOID: uint32(d[2].Int),
				AMType:     d[3].StringValue(),
			}, false, nil
		})
	if err != nil {
		return err
	}
	for _, raw := range rows {
		am := raw.(catalog.AccessMethod)
		cat.RegisterAccessMethodDuringRecovery(&am)
	}
	return nil
}
