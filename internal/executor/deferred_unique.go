package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// Deferred UNIQUE / PRIMARY KEY constraint checking (0119-0004, deferred-unique).
//
// PostgreSQL lets a UNIQUE or PRIMARY KEY constraint be declared
// `DEFERRABLE INITIALLY DEFERRED` (or be deferred at runtime via
// `SET CONSTRAINTS … DEFERRED`). When deferred, a transient duplicate is allowed
// while the transaction runs — the uniqueness is only enforced at COMMIT (or at
// `SET CONSTRAINTS … IMMEDIATE`). This is what makes e.g. `UPDATE t SET id = id+1`
// over a contiguous key range succeed: the intermediate states collide, but the
// final state does not.
//
// goopg already has the analogous machinery for foreign keys (operators_fk.go):
// a per-session queue (BasicSession.deferredFKChecks) filled at DML time and
// drained at COMMIT. This file is the UNIQUE/PK parallel:
//
//   - uniqueCheckDeferred decides, per unique index, whether the INSERT/UPDATE
//     enforcement site should queue instead of raising immediately.
//   - the enforcement sites (checkUniqueIndexes{ForInsert,ForUpdate}) enqueue a
//     DeferredUniqueCheck holding the candidate btree key and stop there.
//   - RunDeferredUniqueChecks drains the queue at COMMIT (both commit paths) and
//     re-probes each key; setConstraintsOp drains the matching subset for
//     `… IMMEDIATE`.
//
// The deferred re-probe differs from the immediate one: at COMMIT the candidate
// row is already present in the index, so a violation is "two or more live
// visible tuples share this key", not "any other live tuple shares this key".

// uniqueCheckDeferred reports whether the uniqueness enforcement for idx
// should be QUEUED instead of raised synchronously now. This is the "needs a
// partial check" predicate, not "deferred to COMMIT": PostgreSQL sets
// pg_index.indimmediate = false for ANY DEFERRABLE unique/PK index regardless
// of its INITIALLY mode (postgres/src/backend/catalog/index.c:2080-2082), so
// every DEFERRABLE index always uses UNIQUE_CHECK_PARTIAL at insert time and
// NEVER blocks per-row — the only thing INITIALLY {DEFERRED|IMMEDIATE} (and
// any in-effect SET CONSTRAINTS override) selects is *when* the queued
// candidate is rechecked: end-of-statement (the common case) vs COMMIT. See
// uniqueCheckDeferToCommit for that companion decision, consulted only once a
// check is already known to be queued (Bucket 4 slice 1: b4-s1-stmt-end-unique).
// A plain (NOT DEFERRABLE) unique index keeps its immediate synchronous check.
// Mirrors fkCheckDeferred in spirit but NOT in the InExplicitTransaction gate
// — that gate stays on fkCheckDeferred (FK checks have no PARTIAL tier) but is
// deliberately dropped here.
func uniqueCheckDeferred(ctx *Context, idx *catalog.Index) bool {
	return idx != nil && idx.Deferrable
}

// uniqueCheckDeferToCommit reports whether a queued deferrable-unique check
// should be rechecked at COMMIT (true) rather than at the end of the current
// statement (false). Callers must have already established idx.Deferrable via
// uniqueCheckDeferred. Honours the constraint's declared INITIALLY
// {DEFERRED|IMMEDIATE} default and any in-effect SET CONSTRAINTS override on
// the session, exactly like the pre-existing UniqueConstraintDeferred
// resolver — this is that SAME resolver, just consulted independently of
// InExplicitTransaction() (an autocommit statement has no override in effect,
// so it correctly falls through to idx.InitiallyDeferred). b4-s1-stmt-end-unique.
func uniqueCheckDeferToCommit(ctx *Context, idx *catalog.Index) bool {
	if ctx.Session == nil {
		return idx.InitiallyDeferred
	}
	if sess, ok := ctx.Session.(*BasicSession); ok {
		return sess.UniqueConstraintDeferred(idx.Name, idx.InitiallyDeferred)
	}
	return idx.InitiallyDeferred
}

// queueDeferredUniqueCheck enqueues a deferred uniqueness re-probe for idx's
// candidate key on the session. No-op when the session is not a BasicSession or
// the key is empty (a NULL-keyed row under NULLS DISTINCT never collides).
func queueDeferredUniqueCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row, key []byte) {
	if len(key) == 0 {
		return
	}
	sess, ok := ctx.Session.(*BasicSession)
	if !ok {
		return
	}
	sess.AddDeferredUniqueCheck(DeferredUniqueCheck{
		TableName:     tbl.Name,
		IndexName:     idx.Name,
		Key:           append([]byte(nil), key...),
		Detail:        buildUniqueConstraintDetail(idx, cols, row),
		DeferToCommit: uniqueCheckDeferToCommit(ctx, idx),
	})
}

// queueDeferredNNDUniqueCheck enqueues a deferred NULLS-NOT-DISTINCT uniqueness
// recheck for a candidate row that has one or more NULL key columns on an NND
// index (no btree key, so the COMMIT recheck is a heap scan over the recorded
// NULL pattern). It captures the candidate's per-key-column NULL-ness / encoded
// value so no live catalog/Row pointers are held across statements. No-op when
// the session is not a BasicSession or the key columns cannot be resolved (the
// immediate path's rowHasNullKeyColumn guard already ensures a plain column key).
// 0119-0004-deferred-unique-nnd.
func queueDeferredNNDUniqueCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row) {
	sess, ok := ctx.Session.(*BasicSession)
	if !ok {
		return
	}
	keyCols, ok := resolveNNDKeyColsFromRow(tbl, idx, cols, row)
	if !ok {
		return
	}
	nnd := make([]DeferredNNDKeyCol, 0, len(keyCols))
	for i, kc := range keyCols {
		// keyCols and idx.Columns are 1:1 in order (resolveNNDKeyColsFromRow
		// iterates idx.Columns), so idx.Columns[i] is this column's name.
		name := ""
		if i < len(idx.Columns) {
			name = idx.Columns[i]
		}
		nnd = append(nnd, DeferredNNDKeyCol{
			ColName: name,
			Null:    kc.candNull,
			Key:     append([]byte(nil), kc.candKey...),
		})
	}
	sess.AddDeferredUniqueCheck(DeferredUniqueCheck{
		TableName:     tbl.Name,
		IndexName:     idx.Name,
		NNDKeyCols:    nnd,
		Detail:        nndDetail(idx, cols, row),
		DeferToCommit: uniqueCheckDeferToCommit(ctx, idx),
	})
}

// RunDeferredUniqueChecks re-verifies every UNIQUE/PK constraint key queued
// during the current transaction and clears the queue. Both commit paths invoke
// it before TxnMgr.Commit (the executor's execCommit and the simple-query
// dispatcher, which bypasses execCommit); a violation returns a 23505 *ExecError
// so the caller can roll the transaction back. No-op when nothing is queued.
// 0119-0004.
func RunDeferredUniqueChecks(ctx *Context, sess *BasicSession) error {
	if sess == nil || ctx == nil || ctx.Pool == nil {
		return nil
	}
	checks := sess.TakeDeferredUniqueChecks()
	if len(checks) == 0 {
		return nil
	}
	return runAllDeferredUniqueChecks(ctx, checks)
}

// RunStmtEndDeferredUniqueChecks re-verifies every queued UNIQUE/PK check that
// is NOT currently deferred to COMMIT (i.e. every DEFERRABLE-but-not-deferred
// candidate queued by uniqueCheckDeferred during the statement that just
// finished) and clears just that subset — the COMMIT-tier subset stays queued
// for RunDeferredUniqueChecks. This is the "end of statement" firing PostgreSQL
// gives every constraint trigger whose deferred flag is not currently set
// (postgres/src/backend/commands/trigger.c, AfterTriggerFireDeferred at
// end-of-command). Called once per top-level statement from both the
// simple-query dispatcher and the extended-protocol Execute path — regardless
// of whether the statement is inside an explicit transaction block or
// autocommit, matching PG's per-statement (not per-transaction) firing
// granularity. A violation raises the same 23505 shape as the immediate path.
// No-op when nothing is queued for this tier. b4-s1-stmt-end-unique.
func RunStmtEndDeferredUniqueChecks(ctx *Context, sess *BasicSession) error {
	if sess == nil || ctx == nil || ctx.Pool == nil {
		return nil
	}
	checks := sess.TakeDeferredUniqueChecksStmtEnd()
	if len(checks) == 0 {
		return nil
	}
	return runAllDeferredUniqueChecks(ctx, checks)
}

// runAllDeferredUniqueChecks re-probes every queued deferred UNIQUE/PK key.
func runAllDeferredUniqueChecks(ctx *Context, checks []DeferredUniqueCheck) error {
	if len(checks) == 0 {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// Like deferred RI (runAllDeferredFKChecks), run under a freshly pushed
	// "latest" snapshot so a key inserted by a transaction that committed after
	// this transaction's snapshot was taken is visible — a deferred uniqueness
	// check at COMMIT must reflect the final committed state, not the firing
	// statement's possibly long-pinned snapshot. Own uncommitted writes stay
	// visible because isLiveForUniqueCheck classifies ctx.Tx.XID as self. 0119-0004.
	if ctx.TxnMgr != nil {
		savedSnap := ctx.Snap
		ctx.Snap = ctx.TxnMgr.FreshSnapshot()
		defer func() { ctx.Snap = savedSnap }()
	}
	for _, c := range checks {
		tbl, ok := im.LookupTable(parser.ObjectName{Name: c.TableName})
		if !ok {
			continue
		}
		idx := indexByNameOnTable(ctx, tbl, c.IndexName)
		if idx == nil {
			continue
		}
		if c.NNDKeyCols != nil {
			// NULLS NOT DISTINCT candidate with NULL key column(s): no btree key,
			// so re-scan the heap for its NULL pattern. 0119-0004-deferred-unique-nnd.
			if err := recheckDeferredNNDUniqueKey(ctx, tbl, idx, c.NNDKeyCols, c.Detail); err != nil {
				return err
			}
			continue
		}
		if err := recheckDeferredUniqueKey(ctx, tbl, idx, c.Key, c.Detail); err != nil {
			return err
		}
	}
	return nil
}

// recheckDeferredNNDUniqueKey re-verifies a deferred NULLS-NOT-DISTINCT unique
// check whose candidate had one or more NULL key columns. It rebuilds the
// per-column scan descriptors from the queued NULL pattern (resolving each
// column against the live table by name) and seq-scans the heap counting live
// tuples that match. The candidate row is itself one such tuple at COMMIT, so
// two or more live matches is the violation — exactly the ≥2 rule the btree path
// uses. A no-key-change transient NULL duplicate resolved before COMMIT (e.g. a
// DELETE or a key-changing UPDATE of one collider) leaves a single live match
// and passes. 0119-0004-deferred-unique-nnd.
func recheckDeferredNNDUniqueKey(ctx *Context, tbl *catalog.Table, idx *catalog.Index, nndCols []DeferredNNDKeyCol, detail string) error {
	rel := ctx.Catalog.RelFileNode(tbl)
	keyCols := make([]nndKeyCol, 0, len(nndCols))
	for _, nc := range nndCols {
		tblOrd, col := nndTableColumn(tbl, nc.ColName)
		if tblOrd < 0 || col == nil {
			// Column dropped within the transaction after the check was queued —
			// nothing to enforce against it. Skip the whole recheck.
			return nil
		}
		keyCols = append(keyCols, nndKeyCol{
			tblOrd:   tblOrd,
			col:      col,
			candNull: nc.Null,
			candKey:  nc.Key,
		})
	}
	count, _ := scanNNDLiveMatches(ctx, tbl, rel, keyCols, 2)
	if count >= 2 {
		return &ExecError{
			Code:    "23505",
			Pos:     0,
			Message: fmt.Sprintf("duplicate key value violates unique constraint %q", idx.Name),
			Detail:  detail,
		}
	}
	return nil
}

// indexByNameOnTable returns the named index on tbl, or nil if absent (it may
// have been dropped within the transaction after the check was queued).
func indexByNameOnTable(ctx *Context, tbl *catalog.Table, name string) *catalog.Index {
	for _, idx := range ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if idx.Name == name {
			return idx
		}
	}
	return nil
}

// resolveDeferredUniqueChainTail walks the t_ctid chain from ptr forward to its
// HOT tail — the version a deferred UNIQUE recheck must judge. A non-key-column
// HOT update (tryApplyHOTUpdate) does no index maintenance, so the b-tree's only
// entry for the candidate key still points at the original slot; that slot's
// xmax has been stamped to the updater's own XID (correctly making it dead per
// isLiveForUniqueCheck) while the live successor sits one t_ctid hop away and is
// never visited by a raw per-pointer fetch. M0134-0005e §12.2
// (docs/design/0134-0005-constraints-sql-divergence.md).
//
// The walk follows a hop ONLY while the tuple being left behind carries
// HeapHotUpdated — mirroring PG's own recheck (unique_key_recheck,
// postgres/src/backend/commands/constraint.c) fetching through SnapshotSelf via
// table_index_fetch_tuple/heap_hot_search_buffer, which likewise stops at the
// first non-HOT link. This matters for correctness, not just fidelity: a
// non-HOT update (any indexed column changed) gets its OWN new index entry
// inserted separately, so continuing past it here would misattribute a later,
// unrelated key value to THIS entry's candidate key and over-count a resolved
// duplicate as still live. A tuple with no HOT successor (isChainTailCTID) or
// no successor at all IS the tail. Reuses isChainTailCTID for the "no
// successor" test and maxCTIDChainWalk as the loop bound, and mirrors
// epqFollowChainFull's per-hop traversal shape (Pin/RLock/copy/RUnlock/Unpin,
// one page pinned at a time) — but WITHOUT its predicate/snapshot evaluation,
// since this recheck needs only isLiveForUniqueCheck on the resolved tuple's
// Xmin/Xmax, not full MVCC visibility. Defensive: bounded by maxCTIDChainWalk,
// and on a missing/unreadable hop or chain exhaustion it falls back to the last
// tuple it did resolve (never panics, never loops forever); ok is false only
// when even the starting pointer could not be fetched.
func resolveDeferredUniqueChainTail(ctx *Context, rel storage.RelFileNode, ptr storage.ItemPointer) (tailPtr storage.ItemPointer, tuple storage.HeapTuple, ok bool) {
	curBlk, curSlot := ptr.Block, ptr.Offset
	var lastTup storage.HeapTuple
	var lastPtr storage.ItemPointer
	haveLast := false
	for i := 0; i < maxCTIDChainWalk; i++ {
		slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: curBlk})
		if perr != nil {
			return lastPtr, lastTup, haveLast
		}
		slot.RLock()
		tup, terr := storage.PageGetHeapTuple(slot.Page(), curSlot)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if terr != nil {
			return lastPtr, lastTup, haveLast
		}
		curPtr := storage.ItemPointer{Block: curBlk, Offset: curSlot}
		lastTup, lastPtr, haveLast = tup, curPtr, true
		if storage.IsMovedToAnotherPartition(tup.Header.CTID) {
			// Sentinel — no live successor reachable through this chain;
			// judge the sentinel-bearing tuple itself (it is dead: xmax was
			// stamped by the move).
			return curPtr, tup, true
		}
		if isChainTailCTID(tup.Header.CTID, curBlk, curSlot) {
			return curPtr, tup, true
		}
		if !tup.Header.IsHotUpdated() {
			// A successor is stamped, but this link is NOT a HOT link (a
			// non-key-column HOT update always sets HeapHotUpdated on the
			// tuple it leaves behind — PageStampHotOldTuple — while a
			// key-changing/non-HOT update clears it — PageStampUpdatedOldTuple
			// / PageApplyHeapUpdateOldRedo). The successor already has its own
			// separate index entry; stop here and judge THIS tuple.
			return curPtr, tup, true
		}
		curBlk, curSlot = tup.Header.CTID.Block, tup.Header.CTID.Offset
	}
	// Chain walk exhausted (corruption backstop) — judge the last tuple
	// resolved rather than looping forever.
	return lastPtr, lastTup, haveLast
}

// recheckDeferredUniqueKey scans the index for key and counts the distinct live
// visible heap tuples carrying it. Two or more is a deferred uniqueness
// violation (the candidate row is itself one of them); it raises 23505. A
// no-key-change transient duplicate that was resolved before COMMIT (e.g. the
// id+1 swap) leaves a single live tuple per key and passes. Each candidate
// pointer is first resolved to its t_ctid chain tail (resolveDeferredUniqueChainTail)
// before judgement, so a non-key HOT update of the candidate row does not hide
// its live successor from the count (M0134-0005e). 0119-0004.
func recheckDeferredUniqueKey(ctx *Context, tbl *catalog.Table, idx *catalog.Index, key []byte, detail string) error {
	rel := ctx.Catalog.RelFileNode(tbl)
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := openIndexBTree(ctx, idx, idxRel)
	if err != nil {
		// Index unreadable (e.g. dropped in-txn) — nothing to enforce.
		return nil
	}
	// De-duplicated by the RESOLVED TAIL pointer, not the raw b-tree pointer:
	// two entries whose chains converge on one physical tuple (e.g. the
	// insert-time entry and a later re-probe both HOT-chaining to the same
	// live successor) must not be double-counted. Mirrors the M0131-S32.1
	// TM_SelfModified convergence guard elsewhere in this package
	// (operators_storage.go:4685) — same "two pointers, one physical tuple"
	// hazard, different call site.
	seen := make(map[storage.ItemPointer]struct{})
	live := 0
	_ = tree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		tailPtr, tuple, ok := resolveDeferredUniqueChainTail(ctx, rel, ptr)
		if !ok {
			return true, nil
		}
		if _, dup := seen[tailPtr]; dup {
			return true, nil
		}
		if isLiveForUniqueCheck(ctx, tuple.Header.Xmin, tuple.Header.Xmax) {
			seen[tailPtr] = struct{}{}
			live++
		}
		return true, nil
	})
	if live >= 2 {
		return &ExecError{
			Code:    "23505",
			Pos:     0,
			Message: fmt.Sprintf("duplicate key value violates unique constraint %q", idx.Name),
			Detail:  detail,
		}
	}
	return nil
}
