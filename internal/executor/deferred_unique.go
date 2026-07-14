package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
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

// uniqueCheckDeferred reports whether the uniqueness enforcement for idx should
// be queued to COMMIT instead of raised now. It honours both the constraint's
// declared DEFERRABLE INITIALLY {DEFERRED|IMMEDIATE} mode and any in-effect
// SET CONSTRAINTS override on the session. Only a DEFERRABLE constraint inside
// an explicit transaction can ever be deferred, so with no SET CONSTRAINTS in
// effect this is exactly `idx.Deferrable && idx.InitiallyDeferred &&
// InExplicitTransaction()` — every plain (NOT DEFERRABLE) unique index keeps
// its immediate check. Mirrors fkCheckDeferred. 0119-0004.
func uniqueCheckDeferred(ctx *Context, idx *catalog.Index) bool {
	if idx == nil || !idx.Deferrable || ctx.Session == nil || !ctx.Session.InExplicitTransaction() {
		return false
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
		TableName: tbl.Name,
		IndexName: idx.Name,
		Key:       append([]byte(nil), key...),
		Detail:    buildUniqueConstraintDetail(idx, cols, row),
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
		TableName:  tbl.Name,
		IndexName:  idx.Name,
		NNDKeyCols: nnd,
		Detail:     nndDetail(idx, cols, row),
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

// recheckDeferredUniqueKey scans the index for key and counts the distinct live
// visible heap tuples carrying it. Two or more is a deferred uniqueness
// violation (the candidate row is itself one of them); it raises 23505. A
// no-key-change transient duplicate that was resolved before COMMIT (e.g. the
// id+1 swap) leaves a single live tuple per key and passes. 0119-0004.
func recheckDeferredUniqueKey(ctx *Context, tbl *catalog.Table, idx *catalog.Index, key []byte, detail string) error {
	rel := ctx.Catalog.RelFileNode(tbl)
	idxRel := ctx.Catalog.IndexRelFileNode(idx)
	tree, err := btree.Open(ctx.Pool, idxRel)
	if err != nil {
		// Index unreadable (e.g. dropped in-txn) — nothing to enforce.
		return nil
	}
	seen := make(map[storage.ItemPointer]struct{})
	live := 0
	_ = tree.RangeScan(key, key, func(_ []byte, ptr storage.ItemPointer) (bool, error) {
		if _, dup := seen[ptr]; dup {
			return true, nil
		}
		slot, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: ptr.Block})
		if perr != nil {
			return true, nil
		}
		slot.RLock()
		tuple, terr := storage.PageGetHeapTuple(slot.Page(), ptr.Offset)
		slot.RUnlock()
		ctx.Pool.Unpin(slot)
		if terr != nil {
			return true, nil
		}
		if isLiveForUniqueCheck(ctx, tuple.Header.Xmin, tuple.Header.Xmax) {
			seen[ptr] = struct{}{}
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
