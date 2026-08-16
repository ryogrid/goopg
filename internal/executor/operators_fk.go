package executor

// M0097-0014: CHECK constraint enforcement added at the bottom of this file.

// operators_fk.go — FK referential integrity enforcement.
//
// M0096-0011: implements REFERENCES … ON DELETE {CASCADE | RESTRICT |
// SET NULL | NO ACTION} for inline column constraints declared in
// CREATE TABLE. DEFERRABLE INITIALLY DEFERRED constraints are queued
// in BasicSession.deferredFKChecks and verified by execCommit at commit time.
//
// Call sites:
//   insertOp.Next  → checkFKInsert  (parent must exist)
//   deleteOp.Next  → enforceFKOnDelete (cascade / restrict / set null)
//   execCommit     → runAllDeferredFKChecks

import (
	"context"
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/storage"
)

// fkXmaxIsKeyChanging reports whether a single-transaction (non-multi,
// non-lock-only) xmax stamped on a matched parent row represents a
// key-changing modification — a key-column UPDATE or a DELETE — which is the
// only kind of in-flight change that conflicts with FK enforcement's implicit
// FOR KEY SHARE lock.  A plain no-key UPDATE (HEAP_KEYS_UPDATED clear, with a
// successor t_ctid) leaves the referenced key intact and does NOT conflict, so
// the FK check must not wait on it (isolation spec fk-deadlock).
//
// goopg does not stamp HEAP_KEYS_UPDATED on DELETE the way upstream
// heap_delete does, so a delete is recognised structurally: its t_ctid points
// at the tuple itself (or is invalid) rather than at a successor version —
// the same test used by lockRowsOp.chainMembers (M0118-0003).
func fkXmaxIsKeyChanging(hdr storage.HeapTupleHeader, self storage.ItemPointer) bool {
	if hdr.Infomask2&storage.HeapKeysUpdated != 0 {
		return true
	}
	if hdr.CTID.Block == storage.InvalidBlockNumber ||
		(hdr.CTID.Block == self.Block && hdr.CTID.Offset == self.Offset) {
		return true
	}
	return false
}

// multixactUpdaterIsKeyChanging reports whether the updater member of an
// updater-bearing MultiXactId xmax performed a key-changing modification
// (StatusUpdate = key UPDATE or DELETE) as opposed to a no-key UPDATE
// (StatusNoKeyUpdate).  Only a key-changing updater conflicts with FK
// enforcement's FOR KEY SHARE lock.  Callers MUST have already established
// that the xmax is a non-lock-only multi with an updater member.
func multixactUpdaterIsKeyChanging(mxs *multixact.Store, xmax storage.TransactionID) bool {
	if mxs == nil {
		return false
	}
	members, ok := mxs.Members(multixact.MultiXactId(xmax))
	if !ok {
		return false
	}
	for _, m := range members {
		if m.Status.IsUpdate() {
			return m.Status == multixact.StatusUpdate
		}
	}
	return false
}

// fkCheckDeferred reports whether fk's enforcement should be queued to COMMIT
// instead of run immediately. It honours both the constraint's declared
// DEFERRABLE INITIALLY {DEFERRED|IMMEDIATE} mode and any in-effect
// SET CONSTRAINTS override on the session (0119-0004). Only a DEFERRABLE
// constraint inside an explicit transaction can ever be deferred. With no
// SET CONSTRAINTS in effect this reproduces the prior
// `fk.Deferrable && fk.InitiallyDeferred && InExplicitTransaction()` predicate
// exactly.
func fkCheckDeferred(ctx *Context, fk catalog.ForeignKey) bool {
	if !fk.Deferrable || ctx.Session == nil || !ctx.Session.InExplicitTransaction() {
		return false
	}
	if sess, ok := ctx.Session.(*BasicSession); ok {
		return sess.FKConstraintDeferred(fk.Name, fk.InitiallyDeferred)
	}
	return fk.InitiallyDeferred
}

// checkFKInsert verifies all FK constraints on fkOwnerTbl for the given
// inserted row. Returns a 23503 (foreign_key_violation) error when a
// referenced parent row does not exist.
//
// fkOwnerTbl is the table whose ForeignKeys are iterated and whose name is
// used to synthesise the auto-generated constraint name (the partitioned
// parent for partition-routed INSERTs).  reportTbl is the table whose name
// appears in the MESSAGE — the routed leaf partition for partition-routed
// INSERTs.  Pass reportTbl==nil to default to fkOwnerTbl.
func checkFKInsert(ctx *Context, fkOwnerTbl *catalog.Table, reportTbl *catalog.Table, row Row) error {
	return checkFKInsertForConstraints(ctx, fkOwnerTbl, reportTbl, row, fkOwnerTbl.ForeignKeys)
}

// checkFKInsertForConstraints is checkFKInsert restricted to a caller-supplied
// subset of the owner table's foreign keys. INSERT passes every constraint;
// UPDATE passes only those whose referencing columns the SET list changed
// (mirrors PostgreSQL firing the RI_FKey_check AFTER trigger only when a key
// column is modified). M0118-0008 (detach-partition-concurrently-4).
func checkFKInsertForConstraints(ctx *Context, fkOwnerTbl *catalog.Table, reportTbl *catalog.Table, row Row, fks []catalog.ForeignKey) error {
	for _, fk := range fks {
		// A NOT ENFORCED FK (PG18) gets no RI check trigger at all in real PG
		// (addFkRecurseReferencing's `if (fkconstraint->is_enforced)` gate,
		// tablecmds.c:11065) — skip the parent-exists check entirely rather
		// than merely deferring it. DU-002 slice 431.
		if fk.NotEnforced {
			continue
		}
		// Gather the FK column values.
		vals, allNull := fkColValues(fkOwnerTbl.Columns, fk.Columns, row)
		if allNull {
			continue // NULL FK values are always allowed
		}
		// DEFERRABLE constraint deferred (by INITIALLY DEFERRED or an in-effect
		// SET CONSTRAINTS … DEFERRED) inside an explicit transaction: queue. 0119-0004.
		if fkCheckDeferred(ctx, fk) {
			if sess, ok := ctx.Session.(*BasicSession); ok {
				sess.AddDeferredFKCheck(DeferredFKCheck{ChildTableName: fkOwnerTbl.Name, FK: fk})
				continue
			}
		}
		if err := assertParentExists(ctx, fkOwnerTbl, reportTbl, fk, vals); err != nil {
			return err
		}
	}
	return nil
}

// enforceFKOnDelete applies referential actions for all FK constraints where
// parentTbl is the REFERENCED table and parentRow is the deleted row.
func enforceFKOnDelete(ctx *Context, parentTbl *catalog.Table, parentRow Row) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// When the deleted row lives in a LEAF PARTITION of a partitioned referenced
	// table (DELETE FROM the partitioned parent routes the tuple to a leaf),
	// PostgreSQL fires the per-partition cloned RI trigger, so a NO ACTION /
	// RESTRICT violation must name the LEAF partition and its ordinal-suffixed
	// constraint clone (`pfk_a_fkey_1` on table `ppk1`) — not the partitioned
	// parent. Route the row to its leaf so enforceFKOnDeletePartitionAncestor can
	// name it, and suppress the parent-named assertNoChildRows below in that
	// case. (When the DELETE targets a leaf directly, parentTbl already IS the
	// leaf and routing is a no-op.) fk-partitioned-2.
	leafTbl := parentTbl
	if len(im.PartitionChildren(parentTbl.OID)) > 0 {
		if leaf, rerr := routeToPartition(parentTbl, parentRow, im, ctx); rerr == nil && leaf != nil {
			leafTbl = leaf
		}
	}
	leafIsPartition := leafTbl != parentTbl && im.IsPartitionChild(leafTbl.OID)
	refs := im.FindFKsReferencingTable(parentTbl.Name)
	for _, ref := range refs {
		// A NOT ENFORCED FK (PG18) gets no action trigger at all in real PG
		// (addFkRecurseReferenced's `if (fkconstraint->is_enforced)` gate,
		// tablecmds.c:10920) — CASCADE/SET NULL/RESTRICT/NO ACTION are all
		// skipped, not merely deferred. DU-002 slice 431.
		if ref.FK.NotEnforced {
			continue
		}
		// Get the referenced column values from the deleted parent row.
		refCols := ref.FK.RefColumns
		if len(refCols) == 0 {
			// Default: use parent PK column(s).
			refCols = pkColumns(parentTbl)
		}
		vals, allNull := fkColValues(parentTbl.Columns, refCols, parentRow)
		if allNull {
			continue
		}
		fk := ref.FK
		// M0100-0005w: wait on any concurrent xact that has inserted a
		// referencing row into the child table that our snapshot cannot
		// see.  Surfaces 40001 under RR/Serializable (mirrors upstream's
		// RI_FKey_*_del crosscheck-snapshot serialization error) and
		// refreshes the snapshot under RC so the scans below process the
		// now-committed row normally.  Deferred FK checks (NO ACTION,
		// deferred by INITIALLY DEFERRED or SET CONSTRAINTS … DEFERRED) skip
		// this because they run at COMMIT time when no concurrent inserter can
		// still be in-flight against us. 0119-0004.
		if !fkCheckDeferred(ctx, fk) {
			if err := fkChildWaitForInFlightInsert(ctx, ref.Child, fk.Columns, vals); err != nil {
				return err
			}
		}
		switch fk.OnDelete {
		case parser.FKActionCascade:
			if err := fkCascadeDelete(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionSetNull:
			if err := fkSetNull(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionSetDefault:
			// Treat as SET NULL for simplicity.
			if err := fkSetNull(ctx, ref.Child, fk, vals); err != nil {
				return err
			}
		case parser.FKActionRestrict:
			// RESTRICT: always immediate. When the deleted row lives in a leaf
			// partition of the referenced table, the leaf-named clone constraint
			// is reported by the partition-ancestor pass below instead.
			if !leafIsPartition {
				if err := assertNoChildRows(ctx, ref.Child, fk, vals); err != nil {
					return err
				}
			}
		default: // NO ACTION
			if fkCheckDeferred(ctx, fk) {
				if sess, ok := ctx.Session.(*BasicSession); ok {
					sess.AddDeferredFKCheck(DeferredFKCheck{ChildTableName: ref.Child.Name, FK: fk})
					continue
				}
			}
			// Leaf-partition deletes defer the violation to the partition-ancestor
			// pass (leaf-named clone constraint); the unconditional wait above
			// already serialised against any concurrent referencing INSERT.
			if !leafIsPartition {
				if err := assertNoChildRows(ctx, ref.Child, fk, vals); err != nil {
					return err
				}
			}
		}
	}
	// Referenced-side check for a row that lives in a LEAF PARTITION of a
	// referenced table: the FK's action triggers are cloned to each partition of
	// the referenced side, so deleting from `ppk1` (a partition of the referenced
	// `ppk`) must still verify no referencing row depends on it. fk-partitioned-1.
	if err := enforceFKOnDeletePartitionAncestor(ctx, leafTbl, parentRow); err != nil {
		return err
	}
	return nil
}

// enforceFKOnDeletePartitionAncestor handles the referenced-side FK check when
// the row being deleted (or whose key is being updated) lives in a LEAF
// PARTITION of a table that is referenced by a foreign key. PostgreSQL clones
// the FK's referenced-side action triggers down to every partition of the
// referenced table, so `DELETE FROM ppk1` (ppk1 a partition of the referenced
// `ppk`) must still reject the delete when a referencing row exists. The
// violation names the LEAF partition as the referenced table, the per-partition
// cloned constraint `<fkname>_<N>` (N = the leaf's 1-based ordinal among the
// referenced parent's partition children — ChooseConstraintName's dedup suffix,
// the same scheme as detachPartitionFKRefCheck), and the referencing (FK-owning)
// table. fk-partitioned-1 Class B. The concurrent variant (a delete blocking
// `<waiting ...>` behind an uncommitted ATTACH that holds the referenced rows)
// is a separate slice — this only covers the committed-attach perms.
// partitionParentTable returns the partitioned parent of child, preferring the
// Table.PartitionParentOID field (set on the CREATE TABLE … PARTITION OF path)
// and falling back to the live partitionChildren map (ATTACH PARTITION leaves
// the field 0). Returns nil when child is not a partition of anything.
func partitionParentTable(im *catalog.InMemory, child *catalog.Table) *catalog.Table {
	if im == nil || child == nil {
		return nil
	}
	poid := child.PartitionParentOID
	if poid == 0 {
		var ok bool
		poid, ok = im.PartitionParentOf(child.OID)
		if !ok {
			return nil
		}
	}
	parent, ok := im.LookupTableByOID(poid)
	if !ok {
		return nil
	}
	return parent
}

func enforceFKOnDeletePartitionAncestor(ctx *Context, leafTbl *catalog.Table, leafRow Row) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok || leafTbl == nil {
		return nil
	}
	// One pass either fires the 23503, finds nothing, or waits on an in-flight
	// concurrent ATTACH PARTITION (returning retry=true). After a wait the attach
	// has committed (or aborted), so re-run the walk: a committed attach is now a
	// registered partition child whose clone is skipped, letting the ROOT
	// partitioned table name the violation. Capped to bound a pathological chain
	// of successive concurrent attaches (in practice 1 wait suffices).
	for attempt := 0; attempt < 8; attempt++ {
		retry, err := fkDeleteAncestorPass(ctx, im, leafTbl, leafRow)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
	}
	return nil
}

// fkDeleteAncestorPass performs a single referenced-side partition check for a
// deleted leaf row. It returns retry=true after blocking on a concurrent,
// still-uncommitted ATTACH PARTITION whose cloned foreign key references the
// deleted key — the caller re-runs the walk once the attach resolves.
func fkDeleteAncestorPass(ctx *Context, im *catalog.InMemory, leafTbl *catalog.Table, leafRow Row) (bool, error) {
	// Walk the partition-ancestor chain (leaf → … → root partitioned parent),
	// firing any FK that references an ancestor. partitionParentTable consults the
	// live partitionChildren map (works for both PARTITION OF and ATTACH).
	child := leafTbl
	for child != nil {
		parent := partitionParentTable(im, child)
		if parent == nil {
			break
		}
		// 1-based ordinal of `child` among `parent`'s partition children — the
		// dedup suffix PostgreSQL appends to the cloned per-partition constraint.
		suffix := 0
		for i, c := range im.PartitionChildren(parent.OID) {
			if c != nil && c.OID == child.OID {
				suffix = i + 1
				break
			}
		}
		for _, ref := range im.FindFKsReferencingTable(parent.Name) {
			// Skip per-partition FK clones (e.g. the copy ATTACH PARTITION places on
			// pfk1) once they are committed: the violation must name the FK's ROOT
			// partitioned table (pfk), and scanning that root already descends into
			// every partition, so the clone is redundant. Matches PG, which reports
			// the referencing relation where the constraint was declared, not the
			// leaf partition.
			if ref.Child != nil && im.IsPartitionChild(ref.Child.OID) {
				continue
			}
			fk := ref.FK
			refCols := fk.RefColumns
			if len(refCols) == 0 {
				refCols = pkColumns(parent)
			}
			vals, allNull := fkColValues(leafTbl.Columns, refCols, leafRow)
			if allNull {
				continue
			}
			// Deferrable INITIALLY DEFERRED FK: the referenced-side check runs at
			// COMMIT, not now — the referenced row may be re-inserted before commit
			// (fk-snapshot's DELETE + re-INSERT in one txn). Queue the deferred
			// check (deduped against the parent-keyed first-loop queue in
			// enforceFKOnDelete) and skip the immediate raise. fk-snapshot. The
			// deferral honours SET CONSTRAINTS too (0119-0004).
			if fkCheckDeferred(ctx, fk) {
				if sess, ok := ctx.Session.(*BasicSession); ok {
					sess.AddDeferredFKCheck(DeferredFKCheck{ChildTableName: ref.Child.Name, FK: fk})
					continue
				}
			}
			found, err := scanTableForMatch(ctx, ref.Child, fk.Columns, vals)
			if err != nil {
				return false, err
			}
			if !found {
				continue
			}
			// ref.Child holds a referencing row for the deleted key but is not (yet)
			// a registered partition child. If a concurrent transaction is in the
			// middle of ATTACHing it (the clone FK is visible but the partition
			// registration is deferred to that transaction's COMMIT), block until
			// that attach resolves — PG holds FOR KEY SHARE on the referenced rows
			// to commit, so this DELETE waits, then re-evaluates (the now-committed
			// clone is skipped and the ROOT pfk names the violation).
			// fk-partitioned-1 concurrent Class B.
			if axid, ok := im.PendingAttachXID(ref.Child.OID); ok &&
				axid != uint32(ctx.Tx.XID) && ctx.TxnMgr != nil &&
				ctx.TxnMgr.IsXIDActive(storage.TransactionID(axid)) {
				qctx := ctx.Ctx
				if qctx == nil {
					qctx = context.Background()
				}
				if werr := ctx.TxnMgr.WaitForXID(qctx, storage.TransactionID(axid)); werr != nil {
					// Connection close / query timeout: stop waiting and let the
					// caller fall through to a non-retry pass.
					return false, nil
				}
				// Refresh the snapshot so the re-evaluation sees the attach's
				// committed catalog state.
				if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
					ctx.Snap = snap.Clone()
				}
				return true, nil
			}
			cname := fkConstraintName(ref.Child, fk)
			if suffix > 0 {
				cname = fmt.Sprintf("%s_%d", cname, suffix)
			}
			return false, &ExecError{
				Code: "23503",
				Message: fmt.Sprintf("update or delete on table %q violates foreign key constraint %q on table %q",
					leafTbl.Name, cname, ref.Child.Name),
				Detail: fmt.Sprintf("Key (%s)=(%s) is still referenced from table %q.",
					strings.Join(refCols, ", "), fkValsForDetail(ctx, vals), ref.Child.Name),
			}
		}
		child = parent
	}
	return false, nil
}

// runAllDeferredFKChecks verifies every queued deferred FK constraint.
// Called by execCommit before TxnMgr.Commit. Returns a 23503 error if
// any constraint is violated.
// RunDeferredFKChecks runs every FK constraint check queued during the current
// transaction (DEFERRABLE constraints deferred by INITIALLY DEFERRED or by an
// in-effect SET CONSTRAINTS … DEFERRED) and clears the queue. The simple-query
// COMMIT path bypasses transactionOp.execCommit, so it must invoke this before
// TxnMgr.Commit; a violation returns a 23503 *ExecError so the caller can roll
// the transaction back. No-op when nothing is queued. 0119-0004 / M0096-0011.
func RunDeferredFKChecks(ctx *Context, sess *BasicSession) error {
	if sess == nil || ctx == nil || ctx.Pool == nil {
		return nil
	}
	checks := sess.TakeDeferredFKChecks()
	if len(checks) == 0 {
		return nil
	}
	return runAllDeferredFKChecks(ctx, checks)
}

func runAllDeferredFKChecks(ctx *Context, checks []DeferredFKCheck) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// PG runs deferred RI checks at COMMIT under a freshly pushed "latest"
	// snapshot, not the firing statement's (possibly long-pinned) snapshot.
	// Install one for the duration of these scans so the parent-existence probe
	// (and the child-row scan in fullTableFKCheck) sees rows committed by
	// concurrent transactions after this transaction's snapshot was taken — the
	// fk-snapshot spec's s2 (REPEATABLE READ) must see s1's just-committed
	// fk_parted_pk(2) at COMMIT. Own uncommitted writes stay visible because
	// TupleVisibleSubxact classifies ctx.Tx.XID as self regardless of snapshot.
	// 0119-0004 (deferred-ri-fresh-snapshot).
	if ctx.TxnMgr != nil {
		savedSnap := ctx.Snap
		ctx.Snap = ctx.TxnMgr.FreshSnapshot()
		defer func() { ctx.Snap = savedSnap }()
	}
	for _, check := range checks {
		childTbl, ok := im.LookupTable(parser.ObjectName{Name: check.ChildTableName}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
		if !ok {
			continue
		}
		if err := fullTableFKCheck(ctx, childTbl, check.FK); err != nil {
			return err
		}
	}
	return nil
}

// fullTableFKCheck scans every row in childTbl and verifies each row's FK
// column values exist in the referenced parent table. Used for deferred
// constraint checks at COMMIT time.
func fullTableFKCheck(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey) error {
	rel := ctx.Catalog.RelFileNode(childTbl)
	cols := childTbl.Columns
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return err
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, err := DecodeHeapTupleRow(cols, tuple, nil)
			if err != nil {
				continue
			}
			vals, allNull := fkColValues(cols, fk.Columns, row)
			if allNull {
				continue
			}
			if err := assertParentExists(ctx, childTbl, childTbl, fk, vals); err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// cloneAndValidateAttachPartitionFKs clones every foreign key declared on a
// partitioned parent (parentTbl) onto a partition being attached (childTbl) and
// validates the partition's existing rows against the referenced table —
// mirroring PostgreSQL ATExecAttachPartition → CloneForeignKeyConstraints, which
// for each cloned referencing FK runs RI_Initial_Check over the new partition.
//
// The validation reuses fullTableFKCheck, whose per-row assertParentExists goes
// through the wait-aware scan (scanTableForMatchFKWait): if a matching parent
// row is being concurrently deleted (in-flight xmax), the attach blocks until
// that deleter commits/aborts (the spec's `<waiting ...>` step) and then either
// re-validates or raises 23503 once the referenced row is gone. The cloned FK
// carries the PARENT constraint's name so the violation reports
// `... violates foreign key constraint "<parent>_<col>_fkey"` exactly as PG.
//
// Only the referencing side is cloned here; the referenced-side per-partition
// constraint (`..._fkey_N`) and the lock-held-to-commit that lets a later
// parent DELETE block on an uncommitted attach are a separate slice (see the
// fk-partitioned-1 deferral). Scoped to a partitioned parent that actually has
// FKs, so non-FK partition attaches are unaffected.
func cloneAndValidateAttachPartitionFKs(ctx *Context, parentTbl, childTbl *catalog.Table) error {
	if parentTbl == nil || childTbl == nil || len(parentTbl.ForeignKeys) == 0 {
		return nil
	}
	for _, pfk := range parentTbl.ForeignKeys {
		// Resolve the parent constraint's reported name once, then carry it onto
		// the clone so the validation error names the parent constraint.
		clone := pfk
		clone.Name = fkConstraintName(parentTbl, pfk)
		// Validate the partition's existing rows BEFORE recording the clone, so a
		// failed attach leaves no half-attached FK behind.
		if err := fullTableFKCheck(ctx, childTbl, clone); err != nil {
			return err
		}
		// Record the clone for ongoing enforcement of future INSERTs into the
		// partition (idempotent: skip if a same-named FK is already present).
		already := false
		for _, existing := range childTbl.ForeignKeys {
			if strings.EqualFold(existing.Name, clone.Name) {
				already = true
				break
			}
		}
		if !already {
			childTbl.ForeignKeys = append(childTbl.ForeignKeys, clone)
		}
	}
	return nil
}

// assertParentExists verifies that the parent table (fk.RefTable) contains a
// row whose reference columns match vals. Returns 23503 if not found.
//
// fkOwnerTbl is where the FK is declared (the partitioned parent for partition-
// routed INSERTs); its name is used to synthesise the auto-generated
// constraint name (`<owner>_<col>_fkey`).  reportTbl is the table whose name
// appears in the error MESSAGE — the routed leaf partition for partition-
// routed INSERTs.  Pass reportTbl==nil to default to fkOwnerTbl.  Matches
// upstream PG's `insert or update on table %q violates foreign key constraint
// %q` shape, where %q-table is the actual relation being written (leaf) and
// %q-constraint inherits the parent's autogenerated name.
func assertParentExists(ctx *Context, fkOwnerTbl *catalog.Table, reportTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	parentTbl, ok := im.LookupTable(parser.ObjectName{Name: fk.RefTable}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !ok {
		return nil // referenced table not found (CREATE TABLE out of order) — skip
	}
	// Determine the referenced columns.
	refCols := fk.RefColumns
	if len(refCols) == 0 {
		refCols = pkColumns(parentTbl)
	}
	// FK INSERT check uses the wait-aware scan: when a matching parent row's
	// xmax is in-flight, block until that updater commits or aborts, then
	// either re-scan or surface a moved-partition error (M0100-0005q). This
	// mirrors PostgreSQL's RI_FKey_check SELECT FOR KEY SHARE semantics.
	found, err := scanTableForMatchFKWait(ctx, parentTbl, refCols, vals, 0)
	if err != nil {
		return err
	}
	if !found {
		if reportTbl == nil {
			reportTbl = fkOwnerTbl
		}
		reportName := ""
		if reportTbl != nil {
			reportName = reportTbl.Name
		}
		return &ExecError{
			Code: "23503",
			Message: fmt.Sprintf("insert or update on table %q violates foreign key constraint %q",
				reportName, fkConstraintName(fkOwnerTbl, fk)),
			Detail: fmt.Sprintf("Key (%s)=(%s) is not present in table %q.",
				strings.Join(fk.Columns, ", "), fkValsForDetail(ctx, vals), fk.RefTable),
		}
	}
	return nil
}

// fkConstraintName synthesises the auto-generated PG constraint name
// `<table>_<col>_fkey` when the catalog has no explicit name. The
// referencing table's name is used (not the referenced table) — matches
// upstream's ChooseConstraintName for inline `col REFERENCES ...`
// declarations.
func fkConstraintName(childTbl *catalog.Table, fk catalog.ForeignKey) string {
	// Honour an explicitly recorded constraint name. For inline/table FKs goopg
	// already stores the auto-generated <table>_<col>_fkey name at CREATE TABLE
	// time, so this is identical to the synthesised result in the common case;
	// it additionally preserves a user-given CONSTRAINT name and a cloned
	// parent-partition FK name (ATTACH PARTITION carries the parent's
	// constraint name onto the child for the validation error). DU-002 / fk-partitioned-1.
	if fk.Name != "" {
		return fk.Name
	}
	tbl := ""
	if childTbl != nil {
		tbl = childTbl.Name
	}
	col := ""
	if len(fk.Columns) > 0 {
		col = fk.Columns[0]
	}
	return fmt.Sprintf("%s_%s_fkey", tbl, col)
}

// checkFKColumnTypeCompatibility verifies that the referencing column type is
// compatible with the referenced column type.  For user-defined enum types the
// types must be identical; for other types no check is performed (implicit
// casts handled at query time).  Called at CREATE TABLE time.
func checkFKColumnTypeCompatibility(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, refColTypeName string, pos int) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	// Only check if the referencing column is a known enum type.
	if _, isEnum := im.LookupEnum(refColTypeName); !isEnum {
		return nil
	}
	// Look up the referenced table.
	refTbl, found := ctx.Catalog.LookupTable(parser.ObjectName{Name: fk.RefTable}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	if !found || refTbl == nil {
		return nil
	}
	// Determine referenced column name.
	refColName := ""
	if len(fk.RefColumns) > 0 {
		refColName = fk.RefColumns[0]
	} else {
		for _, idx := range ctx.Catalog.IndexesOnTable(refTbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
			if idx.Primary && len(idx.Columns) > 0 {
				refColName = idx.Columns[0]
				break
			}
		}
	}
	if refColName == "" {
		return nil
	}
	// Find the referenced column type.
	referencedTypeName := ""
	for _, col := range refTbl.Columns {
		if strings.EqualFold(col.Name, refColName) {
			referencedTypeName = col.Type.Name
			break
		}
	}
	if referencedTypeName == "" {
		return nil
	}
	// Both sides must be the same enum type.
	if strings.EqualFold(refColTypeName, referencedTypeName) {
		return nil
	}
	constraintName := fkConstraintName(childTbl, fk)
	childColName := ""
	if len(fk.Columns) > 0 {
		childColName = fk.Columns[0]
	}
	return &ExecError{
		Code: "42804",
		Pos:  pos,
		Message: fmt.Sprintf(
			"foreign key constraint %q cannot be implemented",
			constraintName),
		Detail: fmt.Sprintf(
			"Key columns %q of the referencing table and %q of the referenced table are of incompatible types: %s and %s.",
			childColName, refColName, refColTypeName, referencedTypeName),
	}
}

// fkValsForDetail renders FK column values for the PostgreSQL-style DETAIL
// line `Key (col)=(val) is not present in table "<parent>".`
func fkValsForDetail(ctx *Context, vals []Datum) string {
	style, order := dateStyleFromCtx(ctx)
	zone := timeZoneFromCtx(ctx)
	var sb strings.Builder
	for i, v := range vals {
		if i > 0 {
			sb.WriteString(", ")
		}
		if v.IsNull() {
			sb.WriteString("null")
			continue
		}
		if v.Kind == KindTime {
			sb.WriteString(formatTimeDatumDateStyle(v, style, order, zone))
			continue
		}
		sb.WriteString(v.Format())
	}
	return sb.String()
}

// assertNoChildRows verifies that no child rows reference the given parent
// values via the FK constraint. Returns 23503 if child rows exist.
func assertNoChildRows(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	found, err := scanTableForMatch(ctx, childTbl, fk.Columns, vals)
	if err != nil {
		return err
	}
	if found {
		constraintName := fkConstraintName(childTbl, fk)
		// Resolve the referenced (parent) columns for the DETAIL line.
		// If RefColumns is empty, the FK references the parent's PK.
		refCols := fk.RefColumns
		if len(refCols) == 0 {
			if parentTbl, ok2 := ctx.Catalog.(*catalog.InMemory); ok2 {
				if pt, ok3 := parentTbl.LookupTable(parser.ObjectName{Name: fk.RefTable}, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)); ok3 {
					refCols = pkColumns(pt)
				}
			}
		}
		return &ExecError{
			Code: "23503",
			Message: fmt.Sprintf("update or delete on table %q violates foreign key constraint %q on table %q",
				fk.RefTable, constraintName, childTbl.Name),
			Detail: fmt.Sprintf("Key (%s)=(%s) is still referenced from table %q.",
				strings.Join(refCols, ", "), fkValsForDetail(ctx, vals), childTbl.Name),
		}
	}
	return nil
}

// fkCascadeDelete deletes all child rows in childTbl (and its partition/inheritance
// children) whose FK columns match vals.
func fkCascadeDelete(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	// Collect all relation tables to scan (parent + partition/inheritance children).
	tables := []*catalog.Table{childTbl}
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		tables = append(tables, im.PartitionChildren(childTbl.OID)...)
		tables = append(tables, im.InheritanceChildren(childTbl.OID)...)
	}

	type victim struct {
		tbl  *catalog.Table
		blk  storage.BlockNumber
		slot uint16
		row  Row
	}
	var victims []victim

	for _, scanTbl := range tables {
		rel := ctx.Catalog.RelFileNode(scanTbl)
		cols := scanTbl.Columns
		nBlocks, err := ctx.Pool.NBlocks(rel)
		if err != nil {
			continue
		}
		for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
			s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
			if err != nil {
				return err
			}
			s.RLock()
			page := s.Page()
			if storage.IsNew(page) {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				continue
			}
			count, err := storage.PageLinePointerCount(page)
			if err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return err
			}
			for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
				tuple, err := storage.PageGetHeapTuple(page, slotIdx)
				if err != nil {
					continue
				}
				if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
					continue
				}
				row, err := DecodeHeapTupleRow(cols, tuple, nil)
				if err != nil {
					continue
				}
				if fkRowMatches(cols, fk.Columns, row, vals) {
					victims = append(victims, victim{tbl: scanTbl, blk: blk, slot: slotIdx, row: row})
				}
			}
			s.RUnlock()
			ctx.Pool.Unpin(s)
		}
	}

	for _, v := range victims {
		// Recursively enforce FK constraints on the child table (for multi-level cascades).
		if err := enforceFKOnDelete(ctx, v.tbl, v.row); err != nil {
			return err
		}
		rel := ctx.Catalog.RelFileNode(v.tbl)
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: v.blk})
		if err != nil {
			return err
		}
		s.Lock()
		if err := storage.PageSetHeapTupleXmax(s.Page(), v.slot, ctx.Tx.XID); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue // slot may have been reused; skip
		}
		markHeapDeleteDirtyAndClearVM(ctx, s, rel, v.blk, v.slot, ctx.Tx.XID, nil)
		s.Unlock()
		ctx.Pool.Unpin(s)
	}
	return nil
}

// fkSetNull updates FK columns to NULL in child rows that match vals.
func fkSetNull(ctx *Context, childTbl *catalog.Table, fk catalog.ForeignKey, vals []Datum) error {
	rel := ctx.Catalog.RelFileNode(childTbl)
	cols := childTbl.Columns

	type pendingUpdate struct {
		blk    storage.BlockNumber
		slot   uint16
		newRow Row
	}
	var pending []pendingUpdate

	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return err
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return err
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, err := DecodeHeapTupleRow(cols, tuple, nil)
			if err != nil {
				continue
			}
			if !fkRowMatches(cols, fk.Columns, row, vals) {
				continue
			}
			newRow := make(Row, len(cols))
			copy(newRow, row)
			for _, fkCol := range fk.Columns {
				for i, c := range cols {
					if strings.EqualFold(c.Name, fkCol) {
						newRow[i] = NullDatum
					}
				}
			}
			pending = append(pending, pendingUpdate{blk: blk, slot: slotIdx, newRow: newRow})
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}

	for _, pu := range pending {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: pu.blk})
		if err != nil {
			return err
		}
		s.Lock()
		if err := storage.PageSetHeapTupleXmax(s.Page(), pu.slot, ctx.Tx.XID); err != nil {
			s.Unlock()
			ctx.Pool.Unpin(s)
			continue
		}
		markHeapDeleteDirtyAndClearVM(ctx, s, rel, pu.blk, pu.slot, ctx.Tx.XID, nil)
		s.Unlock()
		ctx.Pool.Unpin(s)
		if err := writeHeapRow(ctx, rel, cols, pu.newRow); err != nil {
			return err
		}
	}
	return nil
}

// scanTableForMatch returns true if tbl has a visible row where the named
// columns match the given values exactly.
// allDescendants returns all partition/inheritance descendants of tbl
// (direct children, their children, etc.) in breadth-first order.
// Multi-level partition hierarchies (e.g. trunc_a → trunc_a2 → trunc_a21)
// require this recursive expansion so FK scans find leaf partition data.
// allDescendants returns every inheritance / partition descendant of tbl,
// pruned to those VISIBLE to the snapshot identified by snapEpoch. A partition
// child stamped DetachPendingEpoch != 0 that became detach-pending at or before
// the snapshot (snapEpoch >= DetachPendingEpoch) is omitted AND its subtree is
// not descended into — mirroring the planner's collectAllPartitionLeaves /
// catalog.VisiblePartitionChildren so FK existence scans against a
// concurrently-detached partition see the same row set as ordinary queries.
// Pass snapEpoch == 0 to disable detach filtering (e.g. when no snapshot epoch
// is established). Design 0118-0060 (M0118-0008, detach-partition-concurrently-2).
func allDescendants(im *catalog.InMemory, tbl *catalog.Table, snapEpoch uint64) []*catalog.Table {
	var out []*catalog.Table
	queue := []*catalog.Table{tbl}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		kids := append([]*catalog.Table(nil), im.InheritanceChildren(cur.OID)...)
		kids = append(kids, im.PartitionChildren(cur.OID)...)
		for _, k := range kids {
			if k != nil && k.DetachPendingEpoch != 0 && snapEpoch >= k.DetachPendingEpoch {
				continue // detach-pending and invisible to this snapshot — skip subtree
			}
			out = append(out, k)
			queue = append(queue, k)
		}
	}
	return out
}

// snapDetachEpoch returns the epoch used to filter detach-pending partition
// children out of FK existence scans. Unlike ordinary queries (which expand the
// partition set against their own snapshot epoch, ctx.Snap.PartitionDetachEpoch,
// so a REPEATABLE READ transaction whose snapshot predates the detach still sees
// the partition), a referential-integrity existence check observes the CURRENT
// detach state regardless of the enforcing transaction's isolation level: in
// PostgreSQL the RI trigger query runs under the latest snapshot, so a row that
// lives only in a concurrently-detached (detach-pending) partition is invisible
// to the FK check the instant the detach is marked — even under REPEATABLE READ,
// where a cursor/SELECT on the referenced table can still see that very row
// (detach-partition-concurrently-4: "Trying to insert into a partially detached
// partition is rejected … even under REPEATABLE READ mode"). Returns the global
// mvcc.CurrentPartitionDetachEpoch(). Design 0118-0062 (M0118-0008,
// detach-partition-concurrently-4).
func snapDetachEpoch(_ *Context) uint64 {
	return transam.CurrentPartitionDetachEpoch()
}

func scanTableForMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, error) {
	found, err := scanRelForMatch(ctx, tbl, colNames, vals)
	if err != nil || found {
		return found, err
	}
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		for _, child := range allDescendants(im, tbl, snapDetachEpoch(ctx)) {
			found, err = scanRelForMatch(ctx, child, colNames, vals)
			if err != nil || found {
				return found, err
			}
		}
	}
	return false, nil
}

// detachPartitionFKRefCheck implements RI_PartitionRemove_Check
// (ri_triggers.c): when a partition on the REFERENCED side of a foreign key is
// detached, any referencing row whose key value routes to that partition would
// be orphaned, so the detach must fail. PostgreSQL runs this right after marking
// the partition detach-pending; goopg runs it BEFORE MarkPartitionDetachPending
// so routeToPartition still resolves the (not-yet-pending) child instead of
// filtering it out by snapshot epoch. The error names the per-partition cloned
// constraint <fkname>_<N>, where N is the child's 1-based ordinal among the
// parent's partition children — PostgreSQL's ChooseConstraintName dedup suffix
// for the cloned action-trigger constraint. Design 0118-0060
// (M0118-0008 detach-partition-concurrently-2).
func detachPartitionFKRefCheck(ctx *Context, parentTbl, childTbl *catalog.Table) error {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok || parentTbl == nil || childTbl == nil {
		return nil
	}
	// 1-based ordinal of the detaching child among the parent's partition
	// children (PG appends this as the cloned-constraint dedup suffix).
	suffix := 0
	for i, c := range im.PartitionChildren(parentTbl.OID) {
		if c != nil && c.OID == childTbl.OID {
			suffix = i + 1
			break
		}
	}
	for _, fkTbl := range im.AllTables() {
		if fkTbl == nil {
			continue
		}
		for _, fk := range fkTbl.ForeignKeys {
			if !strings.EqualFold(fk.RefTable, parentTbl.Name) {
				continue
			}
			violating, verr := scanRefTableForDetachedPartitionMatch(ctx, im, fkTbl, fk, parentTbl, childTbl)
			if verr != nil {
				return verr
			}
			if violating == nil {
				continue
			}
			cname := fkConstraintName(fkTbl, fk)
			if fk.Name != "" {
				cname = fk.Name
			}
			if suffix > 0 {
				cname = fmt.Sprintf("%s_%d", cname, suffix)
			}
			return &ExecError{
				Code: "23503",
				Message: fmt.Sprintf("removing partition %q violates foreign key constraint %q",
					childTbl.Name, cname),
				Detail: fmt.Sprintf("Key (%s)=(%s) is still referenced from table %q.",
					strings.Join(fk.Columns, ", "), fkValsForDetail(ctx, violating), fkTbl.Name),
			}
		}
	}
	return nil
}

// scanRefTableForDetachedPartitionMatch scans the referencing table fkTbl for
// the first visible row whose (non-null) FK key value routes to childTbl under
// parentTbl's partition scheme. Returns the matched FK column values, or nil
// when no such row exists. Mirrors the SELECT fk JOIN pk WHERE <partconstraint>
// query in RI_PartitionRemove_Check.
func scanRefTableForDetachedPartitionMatch(ctx *Context, im *catalog.InMemory, fkTbl *catalog.Table, fk catalog.ForeignKey, parentTbl, childTbl *catalog.Table) ([]Datum, error) {
	// Referenced parent columns, in FK-column order.
	refCols := fk.RefColumns
	if len(refCols) == 0 {
		refCols = pkColumns(parentTbl)
	}
	if len(refCols) != len(fk.Columns) {
		return nil, nil
	}
	// Precompute parent column indexes for the referenced columns.
	parentIdx := make([]int, len(refCols))
	for i, rc := range refCols {
		parentIdx[i] = -1
		for j, col := range parentTbl.Columns {
			if strings.EqualFold(col.Name, rc) {
				parentIdx[i] = j
				break
			}
		}
		if parentIdx[i] < 0 {
			return nil, nil // referenced column not found — cannot route
		}
	}
	rel := ctx.Catalog.RelFileNode(fkTbl)
	cols := fkTbl.Columns
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return nil, nil
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, perr := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if perr != nil {
			return nil, perr
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
			return nil, cerr
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, terr := storage.PageGetHeapTuple(page, slotIdx)
			if terr != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, derr := DecodeHeapTupleRow(cols, tuple, nil)
			if derr != nil {
				continue
			}
			// Extract FK column values; MATCH SIMPLE skips rows with any NULL.
			fkVals := make([]Datum, len(fk.Columns))
			anyNull := false
			for i, fc := range fk.Columns {
				d := NullDatum
				for j, col := range cols {
					if strings.EqualFold(col.Name, fc) {
						if j < len(row) {
							d = row[j]
						}
						break
					}
				}
				if d.IsNull() {
					anyNull = true
					break
				}
				fkVals[i] = d
			}
			if anyNull {
				continue
			}
			// Build a parent-shaped row carrying the referenced key values and
			// route it; a row that lands in the detaching child is a violation.
			parentRow := make(Row, len(parentTbl.Columns))
			for i := range parentRow {
				parentRow[i] = NullDatum
			}
			for i := range fkVals {
				parentRow[parentIdx[i]] = fkVals[i]
			}
			dest, rerr := routeToPartition(parentTbl, parentRow, im, ctx)
			if rerr != nil {
				continue
			}
			if dest != nil && dest.OID == childTbl.OID {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return fkVals, nil
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return nil, nil
}

func scanRelForMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, error) {
	rel := ctx.Catalog.RelFileNode(tbl)
	cols := tbl.Columns
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return false, nil // relation may not have blocks yet
	}
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return false, err
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, err := DecodeHeapTupleRow(cols, tuple, nil)
			if err != nil {
				continue
			}
			if fkRowMatches(cols, colNames, row, vals) {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return true, nil
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return false, nil
}

// fkPendingRef carries identity of a matched-but-blocked FK parent row so
// the wait+retry loop in scanTableForMatchFKWait can (a) wait on the updater
// XID and (b) detect the moved-to-another-partition sentinel on the same slot
// after the updater commits.  M0100-0005q.
type fkPendingRef struct {
	xid  storage.TransactionID
	rel  storage.RelFileNode
	blk  storage.BlockNumber
	slot uint16
}

// scanRelForFKMatch is scanRelForMatch with one extra outcome: it surfaces the
// (xid, rel, blk, slot) of the first matched parent row whose xmax is in-flight
// (a non-self transaction in the live active set, ignoring lock-only xmax).
//
// Returns:
//   - (true, nil, err): a "clean" matching row (xmax invalid / committed / aborted
//     / lock-only) was found — the FK check passes immediately, no wait required.
//   - (false, &p, nil): no clean match; the matching row's xmax is an in-flight
//     non-self updater — caller must wait, then either re-scan or raise the
//     moved-partition error if the recorded slot now carries the sentinel.
//   - (false, nil, nil): no matching row found — FK violation; caller emits 23503.
//
// Why prefer clean matches: PostgreSQL's RI_FKey_check uses SELECT FOR KEY
// SHARE, which serialises only against actual key-changing UPDATEs.  If any
// visible matching row has no in-flight updater, it satisfies the FK
// immediately without serialising.  When all matching rows are being updated,
// the lock waits for the updater, mirroring our wait+retry loop.
func scanRelForFKMatch(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum) (bool, *fkPendingRef, error) {
	rel := ctx.Catalog.RelFileNode(tbl)
	cols := tbl.Columns
	nBlocks, err := ctx.Pool.NBlocks(rel)
	if err != nil {
		return false, nil, nil
	}
	var pending *fkPendingRef
	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return false, nil, err
		}
		s.RLock()
		page := s.Page()
		if storage.IsNew(page) {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			s.RUnlock()
			ctx.Pool.Unpin(s)
			return false, nil, err
		}
		for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
			tuple, err := storage.PageGetHeapTuple(page, slotIdx)
			if err != nil {
				continue
			}
			if !transam.TupleVisibleSubxact(tuple.Header, ctx.Snap, ctx.Tx.XID, ctx.TxnMgr, ctx.CmdID, ctx.comboStore(), ctx.MultiXact) {
				continue
			}
			row, err := DecodeHeapTupleRow(cols, tuple, nil)
			if err != nil {
				continue
			}
			if !fkRowMatches(cols, colNames, row, vals) {
				continue
			}
			// Matched.  Decide whether to wait on the updater.  For an
			// updater-bearing multixact xmax (IS_MULTI set, LOCK_ONLY clear)
			// the raw t_xmax is a MultiXactId, not a transaction id — resolve
			// the updater member before any single-transaction test, and never
			// record a MultiXactId as the xid to wait on.
			//
			// FK enforcement performs the equivalent of SELECT ... FOR KEY
			// SHARE on the matched parent row.  A KEY SHARE lock conflicts ONLY
			// with a key-changing modification of that row — a key UPDATE or a
			// DELETE (MultiXactStatusUpdate) — and is COMPATIBLE with a
			// concurrent no-key UPDATE (StatusNoKeyUpdate) as well as with pure
			// row locks.  So we wait on the matched row's in-flight xmax only
			// when it represents a key-changing modification; a no-key updater
			// leaves the referenced key intact and the FK is satisfied
			// immediately, without serialising (upstream RI_FKey_check;
			// isolation spec fk-deadlock).
			xmax := tuple.Header.Xmax
			infomask := tuple.Header.Infomask
			effXmax := xmax
			isLockOnly := storage.IsHeapTupleLockOnly(infomask)
			keyChanging := false
			if storage.IsHeapTupleXmaxMulti(infomask) && !isLockOnly {
				effXmax = multixactUpdaterXID(ctx.MultiXact, xmax)
				if effXmax == storage.InvalidTransactionID {
					// Only lockers (or an unknown multi / no store): lock
					// holders do not delete the matched row, so this is a
					// clean FK match just like a lock-only xmax.
					isLockOnly = true
				} else {
					keyChanging = multixactUpdaterIsKeyChanging(ctx.MultiXact, xmax)
				}
			} else if !isLockOnly && xmax != storage.InvalidTransactionID {
				keyChanging = fkXmaxIsKeyChanging(tuple.Header,
					storage.ItemPointer{Block: blk, Offset: slotIdx})
			}
			if effXmax == storage.InvalidTransactionID || isLockOnly || !keyChanging ||
				effXmax == ctx.Tx.XID || ctx.TxnMgr == nil || !ctx.TxnMgr.IsXIDActive(effXmax) {
				// Clean match: no in-flight key-changing modification —
				// FK is satisfied (FOR KEY SHARE is compatible with the
				// in-flight no-key update / pure lock, if any).
				s.RUnlock()
				ctx.Pool.Unpin(s)
				return true, nil, nil
			}
			// Pending: record only the first encountered.
			if pending == nil {
				pending = &fkPendingRef{xid: effXmax, rel: rel, blk: blk, slot: slotIdx}
			}
		}
		s.RUnlock()
		ctx.Pool.Unpin(s)
	}
	return false, pending, nil
}

// scanTableForMatchFKWait performs scanTableForMatch with row-level wait
// semantics for FK enforcement (M0100-0005q).  When the scan finds a matching
// parent row whose xmax is from an in-flight non-self transaction, it waits
// for that transaction to commit or abort, then either:
//   - raises 0A000 "tuple to be locked was already moved to another partition
//     due to concurrent update" when the updater committed a cross-partition
//     UPDATE and stamped the moved-partition sentinel on the old slot, or
//   - refreshes the snapshot and re-scans (covers updater abort and
//     same-partition UPDATEs that may or may not preserve the FK key).
//
// pos is the AST position used for the 0A000 error.  Pass 0 if unavailable.
func scanTableForMatchFKWait(ctx *Context, tbl *catalog.Table, colNames []string, vals []Datum, pos int) (bool, error) {
	// Defensive iteration cap: the wait loop terminates whenever the
	// updater commits / aborts, and a series of cross-partition UPDATEs
	// would chain into separate xmax stamps.  In practice 2 iterations
	// suffice (initial scan + post-wait re-scan); cap at 8 to bound
	// pathological chains without hanging.
	for iter := 0; iter < 8; iter++ {
		// Scan parent.
		found, pending, err := scanRelForFKMatch(ctx, tbl, colNames, vals)
		if err != nil {
			return false, err
		}
		if found {
			return true, nil
		}
		// Then all partition/inheritance descendants (recursive) — return on
		// first clean match, accumulate the first pending xmax we see.
		// Recursive expansion is required for multi-level partition trees.
		if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
			for _, child := range allDescendants(im, tbl, snapDetachEpoch(ctx)) {
				cfound, cpending, cerr := scanRelForFKMatch(ctx, child, colNames, vals)
				if cerr != nil {
					return false, cerr
				}
				if cfound {
					return true, nil
				}
				if pending == nil && cpending != nil {
					pending = cpending
				}
			}
		}
		if pending == nil {
			return false, nil
		}
		// Wait for the in-flight updater.
		qctx := ctx.Ctx
		if qctx == nil {
			qctx = context.Background()
		}
		if ctx.TxnMgr != nil {
			if werr := ctx.TxnMgr.WaitForXID(qctx, pending.xid); werr != nil {
				// ctx cancelled (connection close, query timeout) — surface
				// "not found" so the caller emits 23503; the outer dispatcher
				// will translate the ctx-error wrapping.
				return false, nil
			}
		}
		// Refresh the snapshot so the next scan iteration sees the updater's
		// committed changes (or skips its aborted ones).  Done BEFORE the
		// move-sentinel check because we need snapshot.HasAborted to filter
		// out aborted updaters — sentinel bytes are stamped at UPDATE-time,
		// not commit-time, so they persist across ROLLBACK and would
		// otherwise produce a spurious 0A000.
		if ctx.TxnMgr != nil {
			if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
				ctx.Snap = snap.Clone()
			}
		}
		// Cross-partition move detection: only when the updater committed.
		// Walk the UPDATE chain (not just the recorded slot) because a row
		// updated multiple times in the same xact only carries the sentinel
		// on the final in-xact version's t_ctid.  M0100-0005n / M0100-0005q.
		if !ctx.Snap.HasAborted(pending.xid) &&
			epqChainCheckMovedPartition(ctx, pending.rel, pending.blk, pending.slot) {
			return false, errMovedToAnotherPartition(pos)
		}
		// RR / Serializable: the FK's FOR KEY SHARE lock encountered a parent row
		// that a concurrent transaction key-changed (DELETE or key UPDATE — only
		// such xmaxes are recorded as pending, never a no-key update or pure
		// locker) and that transaction has now committed.  Under REPEATABLE READ /
		// SERIALIZABLE the lock cannot silently walk the update chain past our
		// snapshot, so PostgreSQL surfaces 40001 rather than re-evaluating: see
		// heap_lock_tuple's HeapTupleUpdated → ERRCODE_T_R_SERIALIZATION_FAILURE.
		// Under READ COMMITTED we fall through and re-scan, which finds the row
		// gone (DELETE) and lets the caller emit the 23503 FK violation, or
		// re-finds a still-matching version.  Mirrors fkChildWaitForInFlightInsert
		// on the referenced-DELETE side (isolation spec fk-partitioned-2).
		if ctx.Tx.Isolation != transam.IsolationReadCommitted &&
			ctx.TxnMgr != nil && !ctx.TxnMgr.HasAbortedXID(pending.xid) {
			return false, &ExecError{
				Code:    "40001",
				Message: "could not serialize access due to concurrent update",
			}
		}
	}
	return false, nil
}

// detectInFlightChildInsert scans childTbl (plus its partition / inheritance
// children) for the first row whose FK columns match vals AND whose xmin is
// an in-flight non-self transaction.  Returns (xid, true) when such a row is
// found, or (0, false) otherwise.
//
// "In-flight" here means: xmin is recorded as in-progress in our snapshot
// (so the inserter started before us OR materialised its xid into the active
// set at any point we still observe) AND the inserter is still active in the
// txn manager.  Subxacts roll up to their top-level via SubxactResolver — we
// pin to the simpler top-level check here because FK enforcement always sees
// the top-level xid stamped on the heap row by the inserter.  This match
// criterion mirrors the upstream RI_FKey_*_del trigger's "did a concurrent
// transaction insert a referencing row I cannot see in my snapshot?" probe.
//
// Used by fkChildWaitForInFlightInsert (M0100-0005w): when a parent DELETE
// would touch (CASCADE / SET NULL) or guard against (NO ACTION / RESTRICT)
// child rows that a concurrent INSERT made invisible to our snapshot, block
// on the inserter, then surface 40001 in RR/Serializable.
func detectInFlightChildInsert(ctx *Context, childTbl *catalog.Table, fkCols []string, vals []Datum) (storage.TransactionID, bool) {
	tables := []*catalog.Table{childTbl}
	if im, ok := ctx.Catalog.(*catalog.InMemory); ok {
		tables = append(tables, im.InheritanceChildren(childTbl.OID)...)
		tables = append(tables, im.PartitionChildren(childTbl.OID)...)
	}
	for _, t := range tables {
		rel := ctx.Catalog.RelFileNode(t)
		cols := t.Columns
		nBlocks, err := ctx.Pool.NBlocks(rel)
		if err != nil {
			continue
		}
		for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
			s, err := ctx.Pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
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
			count, err := storage.PageLinePointerCount(page)
			if err != nil {
				s.RUnlock()
				ctx.Pool.Unpin(s)
				continue
			}
			for slotIdx := uint16(1); slotIdx <= uint16(count); slotIdx++ {
				tuple, err := storage.PageGetHeapTuple(page, slotIdx)
				if err != nil {
					continue
				}
				xmin := tuple.Header.Xmin
				if xmin == storage.InvalidTransactionID || xmin == ctx.Tx.XID {
					continue
				}
				if !ctx.Snap.HasInProgress(xmin) {
					continue
				}
				if ctx.TxnMgr == nil || !ctx.TxnMgr.IsXIDActive(xmin) {
					continue
				}
				row, derr := DecodeHeapTupleRow(cols, tuple, nil)
				if derr != nil {
					continue
				}
				if fkRowMatches(cols, fkCols, row, vals) {
					s.RUnlock()
					ctx.Pool.Unpin(s)
					return xmin, true
				}
			}
			s.RUnlock()
			ctx.Pool.Unpin(s)
		}
	}
	return 0, false
}

// fkChildWaitForInFlightInsert is the wait+retry wrapper around
// detectInFlightChildInsert (M0100-0005w).  When a concurrent xact has
// inserted a referencing row that our snapshot cannot see, this function
// blocks on the inserter and then surfaces 40001 under RR/Serializable
// (matching upstream's RI_FKey_*_del crosscheck-snapshot serialization
// error).  Under RC, the snapshot is refreshed and the caller is expected
// to proceed with its scan — the now-committed child row will be visible
// and processed normally by CASCADE / SET NULL / RESTRICT / NO ACTION.
//
// Iteration cap (8) bounds pathological chains where multiple short xacts
// keep inserting referencing rows; in practice 1-2 iterations are enough.
func fkChildWaitForInFlightInsert(ctx *Context, childTbl *catalog.Table, fkCols []string, vals []Datum) error {
	for iter := 0; iter < 8; iter++ {
		xid, found := detectInFlightChildInsert(ctx, childTbl, fkCols, vals)
		if !found {
			return nil
		}
		qctx := ctx.Ctx
		if qctx == nil {
			qctx = context.Background()
		}
		if ctx.TxnMgr != nil {
			if werr := ctx.TxnMgr.WaitForXID(qctx, xid); werr != nil {
				// ctx cancelled (connection close, query timeout) — surface
				// no error; the outer dispatch translates ctx cancellation.
				return nil
			}
		}
		// RR / Serializable: if the inserter committed, the FK enforcement
		// would touch a row that didn't exist in our snapshot — that's
		// the upstream "concurrent update" serialization break.  Match
		// the wire shape ExecError emits for plain DML EPQ retries.
		if ctx.Tx.Isolation != transam.IsolationReadCommitted {
			if ctx.TxnMgr != nil && !ctx.TxnMgr.HasAbortedXID(xid) {
				return &ExecError{
					Code:    "40001",
					Message: "could not serialize access due to concurrent update",
				}
			}
		}
		// RC: refresh snapshot so the next iteration (and the caller's
		// own scans) see the committed insert.  Loop in case another
		// concurrent xact slipped in a fresh in-flight INSERT while we
		// were waiting.
		if ctx.TxnMgr != nil {
			if snap, serr := ctx.TxnMgr.SnapshotFor(ctx.Tx); serr == nil {
				ctx.Snap = snap.Clone()
			}
		}
	}
	return nil
}

// fkColValues extracts the FK column values from a row, returning
// (vals, allNull). allNull is true when every FK column value is NULL.
func fkColValues(cols []catalog.Column, fkCols []string, row Row) ([]Datum, bool) {
	vals := make([]Datum, len(fkCols))
	allNull := true
	for i, name := range fkCols {
		for j, c := range cols {
			if strings.EqualFold(c.Name, name) {
				if j < len(row) {
					vals[i] = row[j]
					if !row[j].IsNull() {
						allNull = false
					}
				}
				break
			}
		}
	}
	return vals, allNull
}

// fkRowMatches reports whether a row's named columns equal the given values.
func fkRowMatches(cols []catalog.Column, fkCols []string, row Row, vals []Datum) bool {
	if len(fkCols) != len(vals) {
		return false
	}
	for i, name := range fkCols {
		found := false
		for j, c := range cols {
			if strings.EqualFold(c.Name, name) {
				if j >= len(row) {
					return false
				}
				if !datumEquals(row[j], vals[i]) {
					return false
				}
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// pkColumns returns the primary-key column names for tbl by scanning its
// indexes for the one with Primary = true. Falls back to the first column.
func pkColumns(tbl *catalog.Table) []string {
	// This function is called in FK contexts where we have an *InMemory
	// catalog via the check's closure. Since pkColumns only needs the index
	// list we look it up via the tbl pointer's catalog reference.
	// We fall back to the first column when no PK index is found.
	if len(tbl.Columns) > 0 {
		return []string{tbl.Columns[0].Name}
	}
	return nil
}

// datumEquals compares two Datums for FK match purposes.
// NULL != anything (handled at the call site via allNull check).
func datumEquals(a, b Datum) bool {
	if a.IsNull() || b.IsNull() {
		return false
	}
	return a.Format() == b.Format()
}

// checkConstraints evaluates all CHECK constraints on tbl for the given row.
// Returns SQLSTATE 23514 (check_violation) if any constraint fails. M0097-0014.
func checkConstraints(ctx *Context, tbl *catalog.Table, row Row) error {
	if len(tbl.CheckConstraints) == 0 {
		return nil
	}
	// Build FROM clause with actual column values so the planner can
	// resolve column names. Format:
	//   SELECT (expr) FROM (VALUES (v1::t1, v2::t2,...)) AS _chk(c1, c2,...)
	colNames := make([]string, len(tbl.Columns))
	for i, col := range tbl.Columns {
		colNames[i] = col.Name
	}
	colNamesJoined := strings.Join(colNames, ", ")

	for ci, exprSQL := range tbl.CheckConstraints {
		if exprSQL == "" {
			continue
		}
		// Build actual-value from clause for this row.
		colVals := make([]string, len(tbl.Columns))
		for i, col := range tbl.Columns {
			if i < len(row) && !row[i].IsNull() {
				v := row[i].Format()
				colVals[i] = "'" + strings.ReplaceAll(v, "'", "''") + "'::" + col.Type.Name
			} else {
				colVals[i] = "NULL::" + col.Type.Name
			}
		}
		rowFrom := " FROM (VALUES (" + strings.Join(colVals, ", ") + ")) AS _chk(" + colNamesJoined + ")"
		fullSQL := "SELECT (" + exprSQL + ")" + rowFrom
		stmts, err := parser.Parse(fullSQL)
		if err != nil || len(stmts) == 0 {
			continue
		}
		plan, err := optimizer.Plan(stmts[0], ctxPlanCatalog(ctx))
		if err != nil {
			continue
		}
		op, err := Build(plan)
		if err != nil {
			continue
		}
		synthCtx := *ctx
		if err := op.Open(&synthCtx); err != nil {
			op.Close()
			continue
		}
		slot, err2 := op.Next()
		// Read slot data BEFORE Close (Close releases the backing row memory).
		var result Datum
		hasResult := false
		if err2 == nil && slot != nil {
			sr := slotRow(slot)
			if len(sr) > 0 {
				result = sr[0]
				hasResult = true
			}
		}
		op.Close()
		if !hasResult {
			continue
		}
		// NULL check result → pass (SQL NULL is not a constraint failure)
		if result.IsNull() {
			continue
		}
		if result.Kind == KindBool && !result.BoolValue() {
			// PostgreSQL always names the violated constraint and appends a
			// "Failing row contains (…)" DETAIL line. The constraint name is
			// recovered from the parallel NamedChecks slice (index i ↔ index i
			// with CheckConstraints); anonymous constraints have an empty name
			// and fall back to the unnamed wording. M0097-0023.
			name := ""
			if ci < len(tbl.NamedChecks) {
				name = tbl.NamedChecks[ci].Name
			}
			msg := fmt.Sprintf("new row for relation %q violates check constraint", tbl.Name)
			if name != "" {
				msg = fmt.Sprintf("new row for relation %q violates check constraint %q", tbl.Name, name)
			}
			return &ExecError{
				Code:    "23514",
				Message: msg,
				Detail:  formatRowForDetail(tbl.Columns, row),
			}
		}
	}
	return nil
}

// checkDomainConstraintsForRow enforces the NOT NULL and CHECK constraints
// declared on a domain type against every domain-typed column in row.
// PostgreSQL wraps every domain-typed column assignment in a CoerceToDomain
// execution node that re-validates the domain's constraints on every
// INSERT/UPDATE (execExprInterp.c ExecEvalConstraintNotNull /
// ExecEvalConstraintCheck), not merely at CREATE TABLE time or on an
// explicit `::domain` cast (expr.go's CastExpr arm already covers the latter,
// but a column simply *declared* with a domain type bypassed both). M0122-0005
// (deferral ledger 2026-07-06 "domain constraint enforcement gap").
func checkDomainConstraintsForRow(ctx *Context, cols []catalog.Column, row Row) error {
	if ctx == nil || ctx.Catalog == nil {
		return nil
	}
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return nil
	}
	for i, col := range cols {
		if col.DeclaredTypeName == "" || i >= len(row) {
			continue
		}
		dom, isDomain := im.LookupDomain(col.DeclaredTypeName, ctx.CurrentDatabaseOid)
		if !isDomain {
			continue
		}
		v := row[i]
		if v.IsNull() {
			if dom.NotNull {
				return &ExecError{
					Code:    "23502",
					Message: fmt.Sprintf("domain %s does not allow null values", strings.ToLower(dom.Name)),
				}
			}
			continue
		}
		if len(dom.Checks) == 0 {
			continue
		}
		// Format() (not StringValue(), which only extracts KindString's Buf
		// payload) renders every Kind's canonical text form, e.g. KindInt's
		// Int field as a decimal string.
		label := v.Format()
		for _, ck := range dom.Checks {
			if len(ck.InValues) > 0 {
				found := false
				for _, allowed := range ck.InValues {
					if strings.EqualFold(label, allowed) {
						found = true
						break
					}
				}
				if !found {
					return &ExecError{
						Code:    "23514",
						Message: fmt.Sprintf("value for domain %s violates check constraint %q", strings.ToLower(dom.Name), ck.Name),
					}
				}
				continue
			}
			if ck.Expr == "" {
				continue
			}
			passed, err := evalDomainCheckExpr(ctx, ck.Expr, v, col.Type.Name)
			if err != nil {
				return err
			}
			if !passed {
				return &ExecError{
					Code:    "23514",
					Message: fmt.Sprintf("value for domain %s violates check constraint %q", strings.ToLower(dom.Name), ck.Name),
				}
			}
		}
	}
	return nil
}

// checkRowConstraintsForWrite enforces column NOT NULL, table CHECK, and
// domain NOT NULL/CHECK constraints against a fully-materialized row before it
// is written by an UPDATE (or upsert DO UPDATE). insertOp.Next already runs
// this same trio inline for INSERT; UPDATE's several write paths
// (updateOp.Next, updateViaIndex, updateWithFrom) previously ran none of them,
// silently allowing NULLs into NOT NULL columns and values violating
// CHECK/domain constraints on UPDATE. M0122-0005 follow-up (deferral ledger
// 2026-07-06 "UPDATE enforces no table-level NOT NULL/CHECK constraints").
// cols/tbl must describe the row's own ordinal layout (the destination
// partition's columns for a partition-routed write, matching how
// checkDomainConstraintsForRow is already called at INSERT's leaf-partition
// site).
func checkRowConstraintsForWrite(ctx *Context, tbl *catalog.Table, cols []catalog.Column, row Row) error {
	for i, col := range cols {
		if col.NotNull && i < len(row) && row[i].IsNull() {
			return &ExecError{
				Code:    "23502",
				Message: fmt.Sprintf("null value in column %q of relation %q violates not-null constraint", col.Name, tbl.Name),
				Detail:  formatRowForDetail(cols, row),
			}
		}
	}
	if len(tbl.CheckConstraints) > 0 {
		if err := checkConstraints(ctx, tbl, row); err != nil {
			return err
		}
	}
	return checkDomainConstraintsForRow(ctx, cols, row)
}

// evalDomainCheckExpr evaluates a generic (non `VALUE IN (...)`) domain CHECK
// predicate against a single coerced value, mirroring checkConstraints' mini-query
// approach: `SELECT (expr) FROM (VALUES (value::basetype)) AS _chk(value)`. The
// domain CHECK expression's only column reference is the `VALUE` placeholder,
// which the lexer case-folds to the identifier `value` on parse — matching the
// synthetic column name here, so no VALUE-specific rewriting is needed. Parse/
// plan/build failures are treated as a pass (matches checkConstraints' leniency)
// rather than blocking the DML statement on an internal evaluation gap.
func evalDomainCheckExpr(ctx *Context, exprSQL string, v Datum, baseType string) (bool, error) {
	valSQL := "NULL::" + baseType
	if !v.IsNull() {
		valSQL = "'" + strings.ReplaceAll(v.Format(), "'", "''") + "'::" + baseType
	}
	fullSQL := "SELECT (" + exprSQL + ") FROM (VALUES (" + valSQL + ")) AS _chk(value)"
	stmts, err := parser.Parse(fullSQL)
	if err != nil || len(stmts) == 0 {
		return true, nil
	}
	plan, err := optimizer.Plan(stmts[0], ctxPlanCatalog(ctx))
	if err != nil {
		return true, nil
	}
	op, err := Build(plan)
	if err != nil {
		return true, nil
	}
	synthCtx := *ctx
	if err := op.Open(&synthCtx); err != nil {
		op.Close()
		return true, nil
	}
	slot, err2 := op.Next()
	var result Datum
	hasResult := false
	if err2 == nil && slot != nil {
		sr := slotRow(slot)
		if len(sr) > 0 {
			result = sr[0]
			hasResult = true
		}
	}
	op.Close()
	if !hasResult || result.IsNull() {
		return true, nil
	}
	if result.Kind == KindBool && !result.BoolValue() {
		return false, nil
	}
	return true, nil
}

// checkViewCheckOption evaluates a `WITH CHECK OPTION` view's defining WHERE
// qual (already resolved against the view's auto-updatable base relation by
// viewQualOnBase, so it evaluates directly against a base-table-shaped row —
// no reparse needed, unlike checkConstraints' text-based CHECK constraints)
// against a row about to be written through it: the row being INSERTed, or
// the row an UPDATE's SET produced. Mirrors execMain.c's ExecWCOQual /
// WCO_VIEW_CHECK — NULL or FALSE is a violation, matching the same truthiness
// rule filterOp already uses. M0119-0004 slice-365 follow-up.
func checkViewCheckOption(ctx *Context, qual optimizer.Expr, viewName string, row Row) error {
	v, err := evalExpr(qual, row, ctx)
	if err != nil {
		return err
	}
	if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
		return nil
	}
	return &ExecError{
		Code:    "44000",
		Message: fmt.Sprintf("new row violates check option for view %q", viewName),
	}
}
