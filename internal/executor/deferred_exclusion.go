package executor

import (
	"fmt"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
)

// Deferred EXCLUDE constraint checking (0119-0004, deferred-exclusion).
//
// PostgreSQL lets an EXCLUDE constraint be declared `DEFERRABLE INITIALLY
// DEFERRED` (or be deferred at runtime via `SET CONSTRAINTS … DEFERRED`). When
// deferred, a transient conflict is allowed while the transaction runs — the
// exclusion is only enforced at COMMIT (or at `SET CONSTRAINTS … IMMEDIATE`):
//
//	CREATE TABLE t (c int, EXCLUDE (c WITH =) DEFERRABLE INITIALLY DEFERRED);
//	BEGIN;
//	INSERT INTO t VALUES (1);
//	INSERT INTO t VALUES (1);   -- transient conflict, allowed (deferred)
//	DELETE FROM t WHERE ctid = (SELECT min(ctid) FROM t);
//	COMMIT;                     -- OK: only one row with c=1 survives
//
// This is the EXCLUDE sibling of the deferred-FK (operators_fk.go) and
// deferred-UNIQUE (deferred_unique.go) machinery: a per-session queue
// (BasicSession.deferredExclChecks) filled at INSERT time and drained at COMMIT.
//
//   - excludeCheckDeferred decides, per exclusion index, whether the INSERT-time
//     enforcement site (checkExclusionConstraintsForInsert) should queue instead
//     of raising immediately.
//   - queueDeferredExclusionCheck captures the candidate's exclusion key (the
//     encoded btree key for a `WITH =` constraint, or the box text for a
//     `USING gist … WITH &&` overlap constraint) and enqueues it.
//   - RunDeferredExclusionChecks drains the queue at COMMIT (both commit paths)
//     and re-probes each candidate; setConstraintsOp drains the matching subset
//     for `… IMMEDIATE`.
//
// As with deferred UNIQUE, the deferred re-probe differs from the immediate one:
// at COMMIT the candidate row is already in the heap/index, so a violation is
// "two or more live visible tuples conflict on this key" (the candidate is itself
// one of them), not "any other live tuple conflicts". A transient conflict
// resolved before COMMIT (e.g. one collider deleted) leaves a single live match
// and passes.

// excludeCheckDeferred reports whether the exclusion enforcement for idx should
// be queued to COMMIT instead of raised now. It honours both the constraint's
// declared DEFERRABLE INITIALLY {DEFERRED|IMMEDIATE} mode and any in-effect
// SET CONSTRAINTS override on the session. Only a DEFERRABLE exclusion constraint
// inside an explicit transaction can ever be deferred. Mirrors uniqueCheckDeferred
// / fkCheckDeferred. 0119-0004.
func excludeCheckDeferred(ctx *Context, idx *catalog.Index) bool {
	if idx == nil || !idx.IsExclusion || !idx.Deferrable ||
		ctx.Session == nil || !ctx.Session.InExplicitTransaction() {
		return false
	}
	if sess, ok := ctx.Session.(*BasicSession); ok {
		return sess.ExclusionConstraintDeferred(idx.Name, idx.InitiallyDeferred)
	}
	return idx.InitiallyDeferred
}

// queueDeferredExclusionCheck captures idx's candidate exclusion key from the
// new row and enqueues a deferred re-probe on the session. No-op when the session
// is not a BasicSession or the candidate key is NULL (exclusion constraints, like
// UNIQUE, ignore NULL key columns — a NULL never conflicts). 0119-0004.
func queueDeferredExclusionCheck(ctx *Context, tbl *catalog.Table, idx *catalog.Index, cols []catalog.Column, row Row) {
	sess, ok := ctx.Session.(*BasicSession)
	if !ok {
		return
	}
	switch idx.ExclusionOp {
	case "=":
		key, err := encodeIndexKeyFromCols(idx, cols, row, ctx.Catalog)
		if err != nil || key == nil {
			return // NULL key column never conflicts.
		}
		sess.AddDeferredExclusionCheck(DeferredExclusionCheck{
			TableName:   tbl.Name,
			IndexName:   idx.Name,
			ExclusionOp: "=",
			Key:         append([]byte(nil), key...),
			Detail:      buildExclusionConstraintDetail(idx, cols, row),
		})
	case "&&":
		boxStr, ok := exclusionBoxValue(idx, cols, row)
		if !ok {
			return // NULL box never conflicts.
		}
		sess.AddDeferredExclusionCheck(DeferredExclusionCheck{
			TableName:   tbl.Name,
			IndexName:   idx.Name,
			ExclusionOp: "&&",
			BoxStr:      boxStr,
		})
	}
}

// exclusionBoxValue extracts the candidate row's box text for a `WITH &&` gist
// exclusion index's leading column. Returns (_, false) when the column is NULL or
// not present in the row. Mirrors the newBoxStr lookup in checkGistOverlapExclusion.
func exclusionBoxValue(idx *catalog.Index, cols []catalog.Column, row Row) (string, bool) {
	if len(idx.Columns) == 0 {
		return "", false
	}
	excColName := idx.Columns[0]
	for i, col := range cols {
		if col.Name == excColName && i < len(row) {
			s, ok := datumAsString(row[i])
			if !ok {
				return "", false // NULL box.
			}
			return s, s != ""
		}
	}
	return "", false
}

// RunDeferredExclusionChecks re-verifies every EXCLUDE constraint candidate queued
// during the current transaction and clears the queue. Both commit paths invoke it
// before TxnMgr.Commit (the executor's execCommit and the simple-query dispatcher,
// which bypasses execCommit); a violation returns a 23P01 *ExecError so the caller
// can roll the transaction back. No-op when nothing is queued. 0119-0004.
func RunDeferredExclusionChecks(ctx *Context, sess *BasicSession) error {
	if sess == nil || ctx == nil || ctx.Pool == nil {
		return nil
	}
	checks := sess.TakeDeferredExclusionChecks()
	if len(checks) == 0 {
		return nil
	}
	return runAllDeferredExclusionChecks(ctx, checks)
}

// runAllDeferredExclusionChecks re-probes every queued deferred EXCLUDE candidate.
func runAllDeferredExclusionChecks(ctx *Context, checks []DeferredExclusionCheck) error {
	if len(checks) == 0 {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// Like deferred RI / deferred UNIQUE, run under a freshly pushed "latest"
	// snapshot so the COMMIT-time check reflects the final committed state rather
	// than the firing statement's possibly long-pinned snapshot. Own uncommitted
	// writes stay visible because isLiveForUniqueCheck classifies ctx.Tx.XID as
	// self. 0119-0004.
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
		switch c.ExclusionOp {
		case "=":
			if err := recheckDeferredExclusionEq(ctx, tbl, idx, c.Key, c.Detail); err != nil {
				return err
			}
		case "&&":
			if err := recheckDeferredExclusionOverlap(ctx, tbl, idx, c.BoxStr); err != nil {
				return err
			}
		}
	}
	return nil
}

// recheckDeferredExclusionEq scans the exclusion btree for key and counts the
// distinct live visible heap tuples carrying it. Two or more conflict (the
// candidate row is itself one of them); it raises 23P01. A transient conflict
// resolved before COMMIT leaves a single live tuple and passes. Mirrors
// recheckDeferredUniqueKey but raises exclusion_violation. 0119-0004.
func recheckDeferredExclusionEq(ctx *Context, tbl *catalog.Table, idx *catalog.Index, key []byte, detail string) error {
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
			Code:    "23P01",
			Pos:     0,
			Message: fmt.Sprintf("conflicting key value violates exclusion constraint %q", idx.Name),
			Detail:  detail,
		}
	}
	return nil
}

// recheckDeferredExclusionOverlap re-verifies a `USING gist … WITH &&` overlap
// exclusion by seq-scanning the heap and counting live tuples whose box overlaps
// the candidate's box. At COMMIT the candidate is itself one such tuple (a box
// always overlaps itself), so two or more overlapping live tuples is the
// violation; a transient overlap resolved before COMMIT (the candidate deleted,
// or the other collider removed) leaves a single match and passes. The DETAIL
// reports an overlapping row's box — a differing one when present, else the
// candidate's own (equal-box conflict). Mirrors checkGistOverlapExclusion's
// scan + overlap predicate. 0119-0004.
func recheckDeferredExclusionOverlap(ctx *Context, tbl *catalog.Table, idx *catalog.Index, boxStr string) error {
	if len(idx.Columns) == 0 {
		return nil
	}
	excColName := idx.Columns[0]
	newUR, newLL, ok := parseBoxText(boxStr)
	if !ok {
		return nil
	}
	excColIdx := -1
	for i, col := range tbl.Columns {
		if col.Name == excColName {
			excColIdx = i
			break
		}
	}
	if excColIdx < 0 {
		return nil
	}
	rel := ctx.Catalog.RelFileNode(tbl)
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil
	}
	overlapCount := 0
	existBox := boxStr // default: equal-box conflict reports the candidate's box.
	decRow := make(Row, len(tbl.Columns))
	for b := storage.BlockNumber(0); b < nBlocks; b++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: b})
		if err != nil {
			continue
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, cerr := storage.PageLinePointerCount(page)
		if cerr != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tup, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if !isLiveForUniqueCheck(ctx, tup.Header.Xmin, tup.Header.Xmax) {
				continue
			}
			storedNatts := int(tup.Header.Infomask2 & 0x07FF)
			if decErr := DecodeRowIntoMctxPGTuple(decRow, tbl.Columns, tup.Data, tup.Bitmap, storedNatts, nil); decErr != nil {
				continue
			}
			if excColIdx >= len(decRow) {
				continue
			}
			existBoxStr, ok2 := datumAsString(decRow[excColIdx])
			if !ok2 {
				continue
			}
			exUR, exLL, ok3 := parseBoxText(existBoxStr)
			if !ok3 {
				continue
			}
			if !(newUR[0] < exLL[0] || exUR[0] < newLL[0] || newUR[1] < exLL[1] || exUR[1] < newLL[1]) {
				overlapCount++
				if existBoxStr != boxStr {
					existBox = existBoxStr
				}
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	if overlapCount >= 2 {
		detail := fmt.Sprintf("Key (%s)=(%s) conflicts with existing key (%s)=(%s).",
			excColName, boxStr, excColName, existBox)
		return &ExecError{
			Code:    "23P01",
			Pos:     0,
			Message: fmt.Sprintf("conflicting key value violates exclusion constraint %q", idx.Name),
			Detail:  detail,
		}
	}
	return nil
}
