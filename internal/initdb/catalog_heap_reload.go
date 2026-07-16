package initdb

// Phase-B0.1 (docs/design/wal-pg-identical-stream/02a §2): the generic
// per-catalog heap-scan reload framework. One shared scan loop + ONE
// visibility filter replace the per-scanner copies; each converted catalog
// contributes a catalogReloadDesc instead of a bespoke *_ddl_recovery.go
// scanner. loadUserTablesFromHeapForDB (pg_class + pg_attribute) is the
// first user — a pure refactor with zero behavior change.

import (
	"fmt"
	"sort"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
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
// decode inspects the raw tuple and returns (row, requireCommittedXmin, err):
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
	name string, decode func(ht storage.HeapTuple) (any, bool, error)) ([]any, error) {
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
			row, requireCommitted, derr := decode(ht)
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
	decode func(ht storage.HeapTuple) (any, bool, error),
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
