package executor

// operators_reindex.go — REINDEX statement executor (M0097-0023).
//
// REINDEX INDEX name validates the index exists; REINDEX TABLE validates
// the table. Raises 42P01 for nonexistent targets matching PostgreSQL.
//
// Plain (non-CONCURRENTLY) REINDEX INDEX / REINDEX TABLE / REINDEX SCHEMA
// physically rebuild their btree index(es) from a fresh heap scan, reusing
// CREATE INDEX's bulk-build path (M0122-0007, design
// 0122-0007-reindex-physical-rebuild). Every CONCURRENTLY form remains a
// catalog-only no-op — see that design doc's Deferral section.

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

type reindexOp struct {
	stmt *parser.ReindexStmt
	ctx  *Context
	done bool
}

func newReindexOp(s *parser.ReindexStmt) *reindexOp { return &reindexOp{stmt: s} }

func (o *reindexOp) Schema() planner.Schema { return nil }

func (o *reindexOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *reindexOp) Close() error { return nil }

func (o *reindexOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true

	if o.stmt.Name == "" || o.ctx == nil || o.ctx.Catalog == nil {
		return nil, EOF
	}

	name := parser.ObjectName{Name: o.stmt.Name}
	// Try schema-qualified form.
	if dotIdx := indexOfDot(o.stmt.Name); dotIdx >= 0 {
		name.Schema = o.stmt.Name[:dotIdx]
		name.Name = o.stmt.Name[dotIdx+1:]
	}

	// REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name> targets a synthetic
	// TOAST relation/index (exposed by the TOAST-exposure epic slices 1–4). It
	// has no physical heap/btree to rebuild, but the spec exercises the
	// CONCURRENTLY wait: REINDEX waits for every transaction holding the PARENT
	// table locked to finish before swapping the rebuilt index, then — if the
	// parent was dropped while it waited — errors that the TOAST relation no
	// longer exists (PG resolves the toast relation through its parent). Route
	// both object types here so the synthetic TOAST name resolves before the
	// real-relation lookups below (which would only see numeric pg_toast names).
	// M0118-0008 (reindex-concurrently-toast, design 0118-0088).
	if im, isMem := o.ctx.Catalog.(*catalog.InMemory); isMem && strings.EqualFold(name.Schema, "pg_toast") {
		toastOID, _, ok := im.LookupToastRel(name.Schema, name.Name)
		if !ok {
			return nil, &ExecError{
				Code:    "42P01",
				Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
			}
		}
		if o.stmt.Concurrently {
			// Wait for lockers of the TOAST relation (parentOID + offset), not
			// the parent table: REINDEX CONCURRENTLY on a toast relation/index
			// waits for transactions that toasted a value or dropped the table
			// (which lock the toast rel), not for a bare LOCK TABLE on the parent
			// (which doesn't). Both REINDEX TABLE and REINDEX INDEX on a toast
			// object resolve to the same toast relation's lockers.
			if parent, pok := im.ToastParentTable(toastOID); pok {
				if toastRel, has := im.ToastRelFileNode(im.RelFileNode(parent)); has {
					if err := o.ctx.waitForRelationLockers(toastRel); err != nil {
						return nil, err
					}
				}
			}
			// The wait above returned once the locking transaction ended; if it
			// dropped the parent table the synthetic TOAST relation is gone too.
			if _, _, stillOK := im.LookupToastRel(name.Schema, name.Name); !stillOK {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
		// Physical reindex of the synthetic TOAST object is a no-op in v0.
		return nil, EOF
	}

	switch o.stmt.ObjectType {
	case "INDEX":
		idx, ok := o.ctx.Catalog.LookupIndex(name)
		if !ok {
			// Try unqualified.
			idx, ok = o.ctx.Catalog.LookupIndex(parser.ObjectName{Name: name.Name})
		}
		if !ok {
			return nil, &ExecError{
				Code:    "42P01",
				Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
			}
		}
		// REINDEX INDEX CONCURRENTLY physically rebuilds via a shadow-file
		// build-then-swap (rebuildIndexConcurrently) so concurrent readers
		// see the live index throughout the build; the plain form rebuilds
		// in place instead, under a real ACCESS EXCLUSIVE lock on the index
		// for the whole duration (see rebuildIndex). M0122-0007.
		if o.stmt.Concurrently {
			if err := o.rebuildIndexConcurrently(idx, o.stmt.Pos()); err != nil {
				return nil, err
			}
		} else if err := o.rebuildIndex(idx, o.stmt.Pos()); err != nil {
			return nil, err
		}
	case "TABLE":
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			if tbl, ok = o.ctx.Catalog.LookupTable(parser.ObjectName{Name: name.Name}); !ok {
				return nil, &ExecError{
					Code:    "42P01",
					Message: fmt.Sprintf("relation %q does not exist", o.stmt.Name),
				}
			}
		}
		// REINDEX TABLE CONCURRENTLY waits for every transaction that holds a
		// lock on the table to finish before it can swap in the rebuilt
		// index(es), without itself blocking concurrent reads or writes for
		// the bulk of the operation (it holds only ShareUpdateExclusive) —
		// reproduced via the WaitForLockers analog on the dedicated table
		// lock manager, unchanged from before this slice (M0118-0008,
		// reindex-concurrently isolation spec). rebuildTableIndexesConcurrently
		// now wraps that same single wait call with a real shadow-file
		// build-then-swap for every btree index on the table. M0122-0007.
		if o.stmt.Concurrently {
			if err := o.rebuildTableIndexesConcurrently(tbl, o.stmt.Pos()); err != nil {
				return nil, err
			}
		} else if err := o.rebuildTableIndexes(tbl, o.stmt.Pos()); err != nil {
			return nil, err
		}
	case "SCHEMA":
		// REINDEX SCHEMA rebuilds every btree index on every table in the
		// schema, one relation at a time: a plain reindex holds a ShareLock
		// on each table for the duration of that table's own rebuild (via
		// rebuildTableIndexes/rebuildIndex, same as REINDEX TABLE), which
		// conflicts with a concurrent SHARE UPDATE EXCLUSIVE holder and so
		// waits there; REINDEX SCHEMA CONCURRENTLY takes no conflicting lock
		// and instead waits for existing lockers to drain (the CONCURRENTLY
		// contract), with its physical rebuild staying a no-op, same as
		// REINDEX TABLE/INDEX CONCURRENTLY. Relations are processed in OID
		// (creation) order so the stall lands on the earliest-created locked
		// table first — which lets a concurrent DROP of a later,
		// not-yet-reached table proceed while the reindex waits. Such a
		// table is re-looked-up by OID immediately before use (rather than
		// resolved once up front) and silently skipped if it no longer
		// exists, matching upstream's tolerate-concurrent-drop behaviour
		// (reindex-schema.spec: drop3 succeeds mid-wait, reindex2/
		// reindex_conc2 still completes). M0118-0008 (reindex-schema);
		// M0122-0007 (physical rebuild).
		schemaName := name.Name
		if name.Schema != "" {
			schemaName = name.Schema
		}
		for _, tn := range o.schemaTableNamesByOID(schemaName) {
			tbl, ok := o.ctx.Catalog.LookupTable(tn)
			if !ok {
				continue
			}
			if o.stmt.Concurrently {
				if err := o.rebuildTableIndexesConcurrently(tbl, o.stmt.Pos()); err != nil {
					return nil, err
				}
			} else if err := o.rebuildTableIndexes(tbl, o.stmt.Pos()); err != nil {
				return nil, err
			}
		}
	}
	// Physical reindex is a no-op in v0.
	return nil, EOF
}

// rebuildIndex physically rebuilds idx's on-disk btree pages from a fresh
// scan of its table's heap, keeping the index's OID and catalog metadata
// unchanged — reusing the exact bulk-build path CREATE INDEX uses
// (ddlOp.bulkBuildBTree/bulkBuildBTreeWithPredicate, which truncates the
// relation before repacking it). M0122-0007 (REINDEX physical rebuild).
//
// Non-btree access methods (gist/spgist/gin/brin) are catalog-only in
// goopg — CREATE INDEX never builds physical storage for them either (see
// execCreateIndex's catalog-only branch) — so there is nothing to rebuild;
// this is a no-op for those, matching the pre-existing behaviour.
//
// Held for the duration: an ACCESS EXCLUSIVE lock on the index (blocking any
// concurrent index scan/DROP INDEX against it) plus a SHARE lock on the
// parent table (blocking concurrent writes but not reads), mirroring real
// PostgreSQL's plain-REINDEX locking (reindex.sgml). Without this, a
// concurrent writer could mutate the heap — or another session's index scan
// could be mid-read of the very pages TruncateRelation is about to discard —
// while the rebuild is in flight.
func (o *reindexOp) rebuildIndex(idx *catalog.Index, pos int) error {
	if idx == nil || idx.Method != "btree" || idx.Table == nil {
		return nil
	}
	tbl := idx.Table
	cols := make([]*catalog.Column, len(idx.Columns))
	for i, name := range idx.Columns {
		if name == "" {
			// Expression column (e.g. lower(col)); collectBTreeEntries skips
			// rows with an all-expression key, matching CREATE INDEX's own
			// documented limitation for expression indexes.
			continue
		}
		col, ok := o.ctx.Catalog.LookupColumn(tbl, name)
		if !ok {
			return &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
		}
		cols[i] = col
	}
	var predExpr planner.Expr
	if idx.HasPredicate && idx.Predicate != nil {
		predExpr, _ = planner.ResolveIndexPredicate(idx.Predicate, tbl)
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
	tblRel := o.ctx.Catalog.RelFileNode(tbl)
	release, err := o.acquireReindexLocks(idxRel, tblRel)
	if err != nil {
		return err
	}
	defer release()
	dop := &ddlOp{ctx: o.ctx}
	if predExpr != nil {
		return dop.bulkBuildBTreeWithPredicate(idxRel, tbl, cols, idx.Unique, idx.NullsNotDistinct, idx.Name, pos, predExpr)
	}
	return dop.bulkBuildBTree(idxRel, tbl, cols, idx.Unique, idx.NullsNotDistinct, idx.Name, pos)
}

// rebuildTableIndexes rebuilds every btree index on tbl. Used by plain
// REINDEX TABLE. M0122-0007.
func (o *reindexOp) rebuildTableIndexes(tbl *catalog.Table, pos int) error {
	for _, idx := range o.ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)) {
		if err := o.rebuildIndex(idx, pos); err != nil {
			return err
		}
	}
	return nil
}

// rebuildIndexConcurrently is REINDEX INDEX CONCURRENTLY's physical rebuild:
// build idx's replacement btree into a fresh, catalog-invisible shadow file
// (buildIndexShadow) while every other backend's reads/writes of idx and its
// table proceed unimpeded, wait for every transaction that already held the
// PARENT TABLE locked at the start of this build to finish (the existing
// waitForRelationLockers contract, unchanged from before this slice — the
// CONCURRENTLY guarantee real PostgreSQL gives via WaitForLockers), then
// atomically swap the shadow file in under a brief ACCESS EXCLUSIVE hold on
// the index (acquireReindexLocks — the same locking plain REINDEX INDEX
// holds for its *entire* rebuild, here held only for the swap itself, which
// is the whole point of CONCURRENTLY not blocking for the build). idx's
// OID/RelFileNode never changes; only its on-disk bytes do, so the swap
// needs no catalog mutation or WAL record — see swapRelationPhysicalFile's
// doc comment (operators_ddl.go). Mirrors the same single-scan-then-wait
// simplification CREATE INDEX CONCURRENTLY already makes (M0122-0007, design
// 0122-0007-reindex-physical-rebuild "Deferral"): a write that lands on the
// table WHILE the shadow build's heap scan is in flight is not guaranteed to
// appear in the rebuilt index — real PostgreSQL closes this gap with a
// second validation scan, which goopg implements for neither CONCURRENTLY
// form.
func (o *reindexOp) rebuildIndexConcurrently(idx *catalog.Index, pos int) error {
	if idx == nil || idx.Table == nil {
		return nil
	}
	shadowRel, built, err := o.buildIndexShadow(idx, pos)
	if err != nil {
		return err
	}
	if !built {
		// Non-btree access method: catalog-only, matches the pre-existing no-op.
		return nil
	}
	tblRel := o.ctx.Catalog.RelFileNode(idx.Table)
	if err := o.ctx.waitForRelationLockers(tblRel); err != nil {
		o.removeShadowFile(shadowRel)
		return err
	}
	idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
	release, err := o.acquireReindexLocks(idxRel, tblRel)
	if err != nil {
		o.removeShadowFile(shadowRel)
		return err
	}
	defer release()
	dop := &ddlOp{ctx: o.ctx}
	if err := dop.swapRelationPhysicalFile(idxRel, shadowRel); err != nil {
		return &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
	}
	return nil
}

// rebuildTableIndexesConcurrently is REINDEX TABLE/SCHEMA CONCURRENTLY's
// per-table physical rebuild: builds a shadow file for every btree index on
// tbl up front (mirroring how CREATE INDEX CONCURRENTLY's own start-time
// snapshot is captured before its single build+wait), then performs the
// SAME single waitForRelationLockers call on the table this code path took
// before this slice added a physical rebuild at all — so the isolation-spec
// timing (reindex-concurrently.spec's session interleavings) is unchanged —
// and finally swaps each index's shadow in, one at a time, exactly as plain
// REINDEX TABLE's rebuildTableIndexes already does per index. A build
// failure on any index cleans up every shadow already built for earlier
// indexes on this table before returning the error. M0122-0007.
func (o *reindexOp) rebuildTableIndexesConcurrently(tbl *catalog.Table, pos int) error {
	idxs := o.ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid))
	shadows := make(map[uint32]storage.RelFileNode, len(idxs))
	for _, idx := range idxs {
		shadowRel, built, err := o.buildIndexShadow(idx, pos)
		if err != nil {
			for _, sr := range shadows {
				o.removeShadowFile(sr)
			}
			return err
		}
		if built {
			shadows[idx.OID] = shadowRel
		}
	}
	tblRel := o.ctx.Catalog.RelFileNode(tbl)
	if err := o.ctx.waitForRelationLockers(tblRel); err != nil {
		for _, sr := range shadows {
			o.removeShadowFile(sr)
		}
		return err
	}
	dop := &ddlOp{ctx: o.ctx}
	for _, idx := range idxs {
		shadowRel, ok := shadows[idx.OID]
		if !ok {
			continue
		}
		idxRel := o.ctx.Catalog.IndexRelFileNode(idx)
		release, err := o.acquireReindexLocks(idxRel, tblRel)
		if err != nil {
			return err
		}
		swapErr := dop.swapRelationPhysicalFile(idxRel, shadowRel)
		release()
		if swapErr != nil {
			return &ExecError{Code: "XX000", Pos: pos, Message: swapErr.Error()}
		}
	}
	return nil
}

// buildIndexShadow physically builds idx's would-be-rebuilt btree contents
// into a fresh RelFileNode that shares idx's tablespace/database identity
// but a brand-new RelOid (minted via Catalog.AllocOID(), never registered in
// the catalog — the same "synthetic OID, no catalog entry" pattern named
// CHECK constraints already use) — so the build never touches idx's own
// on-disk file, letting concurrent readers/writers of the LIVE index proceed
// throughout. Returns built=false for non-btree access methods (gist/spgist/
// gin/brin are catalog-only in goopg — nothing to rebuild), matching
// rebuildIndex's existing no-op for those. The returned file is fully built
// and durably flushed before return; swapRelationPhysicalFile's atomic
// rename is the only remaining step.
func (o *reindexOp) buildIndexShadow(idx *catalog.Index, pos int) (storage.RelFileNode, bool, error) {
	if idx == nil || idx.Method != "btree" || idx.Table == nil {
		return storage.RelFileNode{}, false, nil
	}
	tbl := idx.Table
	cols := make([]*catalog.Column, len(idx.Columns))
	for i, name := range idx.Columns {
		if name == "" {
			// Expression column (e.g. lower(col)); collectBTreeEntries skips
			// rows with an all-expression key, matching CREATE INDEX's own
			// documented limitation for expression indexes.
			continue
		}
		col, ok := o.ctx.Catalog.LookupColumn(tbl, name)
		if !ok {
			return storage.RelFileNode{}, false, &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
		}
		cols[i] = col
	}
	var predExpr planner.Expr
	if idx.HasPredicate && idx.Predicate != nil {
		predExpr, _ = planner.ResolveIndexPredicate(idx.Predicate, tbl)
	}
	shadowRel := o.ctx.Catalog.IndexRelFileNode(idx)
	shadowRel.RelOid = o.ctx.Catalog.AllocOID()
	dop := &ddlOp{ctx: o.ctx}
	var buildErr error
	if predExpr != nil {
		buildErr = dop.bulkBuildBTreeWithPredicate(shadowRel, tbl, cols, idx.Unique, idx.NullsNotDistinct, idx.Name, pos, predExpr)
	} else {
		buildErr = dop.bulkBuildBTree(shadowRel, tbl, cols, idx.Unique, idx.NullsNotDistinct, idx.Name, pos)
	}
	if buildErr != nil {
		o.removeShadowFile(shadowRel)
		return storage.RelFileNode{}, false, buildErr
	}
	if o.ctx.Pool != nil {
		if err := o.ctx.Pool.FlushRel(shadowRel); err != nil {
			o.removeShadowFile(shadowRel)
			return storage.RelFileNode{}, false, &ExecError{Code: "XX000", Pos: pos, Message: err.Error()}
		}
	}
	return shadowRel, true, nil
}

// removeShadowFile best-effort removes a shadow file built by
// buildIndexShadow that turned out not to be needed (a sibling index's build
// failed, or the post-build CONCURRENTLY wait itself errored) — delegates to
// removeShadowRelationFile's (operators_ddl.go) non-fatal cleanup contract.
func (o *reindexOp) removeShadowFile(rel storage.RelFileNode) {
	dop := &ddlOp{ctx: o.ctx}
	dop.removeShadowRelationFile(rel)
}

// acquireReindexLocks takes and HOLDS — unlike acquireRelLockMaybeTransient's
// wait-only-then-release pattern used elsewhere in this file — an ACCESS
// EXCLUSIVE lock on the index plus a SHARE lock on its parent table for the
// duration of a physical rebuild. The returned func releases both; call it
// unconditionally (defer) once the rebuild finishes, success or error. A zero
// backend identity (no live connection, e.g. an embedded/test context) or a
// system-catalog relation (OID below firstNormalObjectOID) is treated as
// always available, matching every other lock helper in this file.
func (o *reindexOp) acquireReindexLocks(idxRel, tblRel storage.RelFileNode) (func(), error) {
	c := o.ctx
	if idxRel.RelOid < firstNormalObjectOID || tblRel.RelOid < firstNormalObjectOID {
		return func() {}, nil
	}
	backend := c.BackendID
	if c.TxnLockBackendID != 0 {
		backend = c.TxnLockBackendID
	}
	if backend == 0 {
		return func() {}, nil
	}
	lockCtx := context.Background()
	if c.Ctx != nil {
		lockCtx = c.Ctx
	}
	tblTag := lockmgr.LockTag{DB: tblRel.DBOid, Rel: tblRel.RelOid}
	idxTag := lockmgr.LockTag{DB: idxRel.DBOid, Rel: idxRel.RelOid}
	if err := tableLockMgr.AcquireWithTimeout(lockCtx, backend, tblTag, lockmgr.ShareLock, c.deadlockTimeout()); err != nil {
		return nil, reindexLockWaitError(err)
	}
	if err := tableLockMgr.AcquireWithTimeout(lockCtx, backend, idxTag, lockmgr.AccessExclusiveLock, c.deadlockTimeout()); err != nil {
		tableLockMgr.Release(backend, tblTag, lockmgr.ShareLock)
		return nil, reindexLockWaitError(err)
	}
	return func() {
		tableLockMgr.Release(backend, idxTag, lockmgr.AccessExclusiveLock)
		tableLockMgr.Release(backend, tblTag, lockmgr.ShareLock)
	}, nil
}

// reindexLockWaitError maps a lockmgr wait failure onto the same ExecError
// shape acquireRelLockMaybeTransient uses (context.go), so REINDEX's lock
// wait reports deadlocks/cancellations identically to every other blocking
// DDL lock acquisition in this codebase.
func reindexLockWaitError(err error) error {
	if err == lockmgr.ErrDeadlockDetected {
		return &ExecError{Code: "40P01", Message: "deadlock detected"}
	}
	if ee := lockWaitCancelError(err); ee != nil {
		return ee
	}
	return &ExecError{Code: "XX000", Message: err.Error()}
}

// schemaTableNamesByOID returns the qualified names of every non-virtual user
// table in schemaName, ordered by OID (creation order). REINDEX SCHEMA
// locks/waits/rebuilds relations in this order so the first stall is on the
// earliest-created locked table, matching upstream's relation processing
// order. Returning names (rather than resolved Table pointers or
// RelFileNodes captured once up front) lets the caller re-resolve each
// relation via LookupTable immediately before use, so a table concurrently
// dropped while REINDEX SCHEMA waits on an earlier one is detected and
// skipped instead of operating on stale catalog state. M0118-0008.
func (o *reindexOp) schemaTableNamesByOID(schemaName string) []parser.ObjectName {
	tables := o.ctx.Catalog.TablesInSchema(schemaName)
	type namedOID struct {
		name parser.ObjectName
		oid  uint32
	}
	named := make([]namedOID, 0, len(tables))
	for _, tn := range tables {
		tbl, ok := o.ctx.Catalog.LookupTable(tn)
		if !ok {
			continue
		}
		named = append(named, namedOID{name: tn, oid: tbl.OID})
	}
	sort.Slice(named, func(i, j int) bool { return named[i].oid < named[j].oid })
	out := make([]parser.ObjectName, len(named))
	for i, n := range named {
		out[i] = n.name
	}
	return out
}

// indexOfDot returns the index of '.' in s, or -1 if not present.
func indexOfDot(s string) int {
	for i, c := range s {
		if c == '.' {
			return i
		}
	}
	return -1
}
