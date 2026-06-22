package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/access/btree"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/vacuum"
)

// vacuumOp executes a VACUUM statement, running heap page-prune on the
// target relations and updating the FSM, VM, and relfrozenxid with the
// resulting state (M0046-0003/0004/0005). VACUUM without a target list
// vacuums all user tables.
type vacuumOp struct {
	plan *planner.Utility
	ctx  *Context
	done bool
}

func newVacuumOp(p *planner.Utility) *vacuumOp { return &vacuumOp{plan: p} }

func (o *vacuumOp) Schema() planner.Schema { return nil }

func (o *vacuumOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *vacuumOp) Close() error { return nil }

// Next runs the VACUUM as a one-shot side effect. Errors are suppressed:
// a VACUUM failure should not abort the client session.
func (o *vacuumOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.ctx == nil || o.ctx.Pool == nil || o.ctx.TxnMgr == nil {
		return nil, EOF
	}

	vs := o.plan.Stmt.(*parser.VacuumStmt)

	// Compute freeze horizon (M0046-0005): tuples with xmin < freezeBelow
	// will have their xmin rewritten to FrozenTransactionID.
	var freezeBelow storage.TransactionID
	if o.ctx.FreezeMinAge > 0 {
		currentXID := o.ctx.TxnMgr.NextXID()
		if int64(currentXID) > o.ctx.FreezeMinAge {
			freezeBelow = currentXID - storage.TransactionID(o.ctx.FreezeMinAge)
		}
	}

	opts := vacuum.VacuumOptions{
		FSM:         o.ctx.FSM,
		VM:          o.ctx.VM,
		FreezeBelow: freezeBelow,
	}

	// SKIP_LOCKED governs the per-relation lock taken to begin vacuuming.
	// PostgreSQL takes AccessExclusiveLock for VACUUM FULL and
	// ShareUpdateExclusiveLock otherwise (vacuum.c vacuum_open_relation); with
	// SKIP_LOCKED the acquire is conditional and a contended relation is skipped
	// instead of waited on. M0118-0008 (vacuum-skip-locked).
	lmMode := lockmgr.ShareUpdateExclusiveLock
	if vs.Full {
		lmMode = lockmgr.AccessExclusiveLock
	}
	targets, parents := o.expandVacuumTargets(vs)
	for _, vt := range targets {
		tbl := vt.tbl
		rel := o.ctx.Catalog.RelFileNode(tbl)
		if vs.SkipLocked {
			if !o.ctx.tryAcquireMaintenanceLock(rel, lmMode) {
				// Only a relation the user named explicitly produces a WARNING;
				// partition children reached by expanding a partitioned table are
				// skipped silently (vacuum.c get_all_vacuum_rels / expand_vacuum_rel
				// passes a log-skip flag only for explicitly listed relations).
				if vt.explicit {
					o.ctx.AddWarning(fmt.Sprintf("skipping vacuum of %q --- lock not available", tbl.Name))
				}
				continue
			}
		} else if err := o.ctx.acquireRelLockMaybeTransient(rel, lmMode); err != nil {
			// Without SKIP_LOCKED the per-relation lock is acquired blocking
			// (vacuum.c vacuum_open_relation → LockRelationOid), so VACUUM waits
			// behind a conflicting holder such as LOCK ... IN SHARE MODE. A
			// failed acquire (deadlock / cancel) skips just this relation.
			continue
		}
		// After taking the lock the relation may have been dropped by a
		// transaction that committed while we waited (PG re-checks via
		// try_relation_open in vacuum_open_relation, logging "skipping ... ---
		// relation no longer exists" for an explicitly named target, silently
		// for an expanded partition child). M0118-0008 (vacuum-concurrent-drop).
		if !relationStillExists(o.ctx, tbl) {
			if vt.explicit {
				o.ctx.AddWarning(fmt.Sprintf("skipping vacuum of %q --- relation no longer exists", tbl.Name))
			}
			continue
		}
		stats, err := vacuum.VacuumWithOptions(o.ctx.Pool, o.ctx.TxnMgr, rel, opts)
		if err == nil && freezeBelow > 0 && stats.NewFrozenXID != 0 {
			// Advance relfrozenxid to the lowest unfrozen xmin found.
			tbl.RelFrozenXID = stats.NewFrozenXID
		} else if err == nil && freezeBelow > 0 && stats.NewFrozenXID == 0 && stats.Frozen > 0 {
			// All tuples frozen — relfrozenxid advances to freezeBelow.
			tbl.RelFrozenXID = freezeBelow
		}

		// Index vacuum (M0047-0002): remove stale B-tree entries pointing
		// to dead heap tuples and delete any empty leaf pages.
		if err == nil && len(stats.DeadTIDs) > 0 && o.ctx.Pool != nil {
			vacuumIndexes(o.ctx, tbl, stats.DeadTIDs)
		}

		// If we vacuumed a nailed catalog relation (pg_class, pg_attribute,
		// pg_proc, or pg_type), signal that the relcache init files need
		// invalidation. This mirrors PG's in-place update path which calls
		// RegisterInvalidationMessage with the nailed OID, causing both
		// pg_internal.init files to be unlinked at commit. M0106-0010 batched-31.
		if isNailedCatalogOID(tbl.OID) {
			o.ctx.TxnMgr.SetRelcacheInvalPending()
		}
	}

	// VACUUM (ANALYZE) of a partitioned table also gathers inheritance-tree
	// statistics for the parent, which reads every leaf partition under an
	// AccessShareLock acquired unconditionally — SKIP_LOCKED does NOT cover this
	// inheritance scan (analyze.c acquire_inherited_sample_rows). So a child held
	// under a conflicting lock by another session makes ANALYZE wait here even
	// though the per-relation SKIP_LOCKED pass above skipped it. Plain VACUUM
	// (no ANALYZE) does no such scan and never blocks. M0118-0008.
	if vs.Analyze {
		for _, parent := range parents {
			analyzeInheritanceWait(o.ctx, parent)
		}
	}
	return nil, EOF
}

// vacuumTarget is one relation to vacuum, tagged with whether the user named it
// explicitly (explicit=true ⇒ a SKIP_LOCKED skip emits a WARNING) or whether it
// was reached by expanding a partitioned table (explicit=false ⇒ silent skip).
type vacuumTarget struct {
	tbl      *catalog.Table
	explicit bool
}

// expandVacuumTargets resolves the VACUUM target list into the concrete heap
// relations to process, expanding any partitioned table into its leaf
// partitions (the parent itself has no storage). It returns the flat target
// list plus the partitioned parents encountered, which the caller uses to drive
// the inheritance-statistics AccessShare scan when ANALYZE is requested.
func (o *vacuumOp) expandVacuumTargets(vs *parser.VacuumStmt) ([]vacuumTarget, []*catalog.Table) {
	cat := o.ctx.Catalog
	im, _ := cat.(*catalog.InMemory)
	var out []vacuumTarget
	var parents []*catalog.Table
	var add func(tbl *catalog.Table, explicit bool)
	add = func(tbl *catalog.Table, explicit bool) {
		if tbl == nil || tbl.Virtual {
			return
		}
		if tbl.PartitionMethod != "" && im != nil {
			// Partitioned table: expand to children (silent skip on lock), and
			// remember it for the inheritance ANALYZE pass.
			parents = append(parents, tbl)
			for _, child := range im.PartitionChildren(tbl.OID) {
				add(child, false)
			}
			return
		}
		out = append(out, vacuumTarget{tbl: tbl, explicit: explicit})
	}
	if len(vs.Targets) > 0 {
		for _, name := range vs.Targets {
			tbl, ok := cat.LookupTable(name)
			if !ok {
				continue
			}
			// Maintenance-privilege check (vacuum_is_permitted_to_vacuum). A
			// non-superuser session (SET ROLE) may only vacuum a relation it owns
			// (or holds MAINTAIN on); otherwise the relation is skipped with a
			// WARNING. PostgreSQL performs this in expand_vacuum_rel using the
			// pg_class syscache with NO lock, so an unprivileged VACUUM skips
			// immediately instead of waiting behind a conflicting lock holder.
			// M0118-0008 (vacuum-conflict).
			if !maintenancePermitted(o.ctx, tbl) {
				o.ctx.AddWarning(fmt.Sprintf("permission denied to vacuum %q, skipping it", tbl.Name))
				continue
			}
			add(tbl, true)
		}
		return out, parents
	}
	// Database-wide VACUUM: every user table, none "explicitly" named, so a
	// SKIP_LOCKED skip is silent (matches PG's autovacuum-style log suppression).
	if im != nil {
		for _, tbl := range im.AllTables() {
			if !tbl.Virtual {
				out = append(out, vacuumTarget{tbl: tbl, explicit: false})
			}
		}
	}
	return out, parents
}

// analyzeInheritanceWait reproduces the AccessShareLock that ANALYZE of a
// partitioned table takes on each leaf partition to read inheritance-tree
// sample rows. The lock is acquired (blocking, so a conflicting holder makes
// ANALYZE wait) and released immediately — goopg does not yet compute inherited
// statistics, but the lock interaction is what the vacuum-skip-locked isolation
// spec observes (ANALYZE of a partitioned parent waits on a child locked in
// ACCESS EXCLUSIVE, but not one locked in SHARE). M0118-0008.
func analyzeInheritanceWait(ctx *Context, parent *catalog.Table) {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	for _, child := range im.PartitionChildren(parent.OID) {
		if child.PartitionMethod != "" {
			analyzeInheritanceWait(ctx, child) // sub-partitioned: recurse
			continue
		}
		rel := ctx.Catalog.RelFileNode(child)
		_ = ctx.acquireRelLockMaybeTransient(rel, lockmgr.AccessShareLock)
	}
}

// maintenancePermitted reports whether the session's effective role may run a
// maintenance command (VACUUM / ANALYZE / CLUSTER) on tbl. It mirrors
// PostgreSQL's vacuum_is_permitted_to_vacuum (and the equivalent CLUSTER owner
// check): the bootstrap superuser — a session that has NOT done SET ROLE to a
// non-superuser — may always maintain any relation; a non-superuser role may
// only maintain a relation it owns (Table.Owner, set via ALTER TABLE OWNER TO)
// or one on which it holds the MAINTAIN privilege. Owner comparison is
// case-insensitive (NonSuperuserRole is stored verbatim from SET ROLE; Owner
// from the parsed ALTER statement). M0118-0008 (vacuum-conflict / cluster-conflict).
func maintenancePermitted(ctx *Context, tbl *catalog.Table) bool {
	role := ctx.NonSuperuserRole
	if role == "" {
		return true // bootstrap superuser: full privileges
	}
	if tbl.Owner != "" && strings.EqualFold(tbl.Owner, role) {
		return true
	}
	return ctx.Catalog.HasTablePrivilege(tbl.OID, role, "MAINTAIN")
}

// relationStillExists reports whether tbl is still present in the catalog,
// looked up by OID. VACUUM/ANALYZE resolve their target list up front, then
// acquire a per-relation lock that may wait behind a conflicting holder; while
// waiting, a concurrent transaction can DROP one of the targets and commit.
// After the lock is taken the relation must be re-checked, mirroring PG's
// try_relation_open inside vacuum_open_relation. M0118-0008
// (vacuum-concurrent-drop).
func relationStillExists(ctx *Context, tbl *catalog.Table) bool {
	im, ok := ctx.Catalog.(*catalog.InMemory)
	if !ok {
		return true
	}
	_, exists := im.LookupTableByOID(tbl.OID)
	return exists
}

// vacuumIndexes removes stale B-tree index entries that point to dead heap
// tuples collected during the heap vacuum pass. Empty index leaf pages are
// deleted and the tree is compacted if fully empty (M0047-0002).
func vacuumIndexes(ctx *Context, tbl *catalog.Table, deadTIDs []storage.ItemPointer) {
	indexes := ctx.Catalog.IndexesOnTable(tbl)
	for _, idx := range indexes {
		if idx.Method != "btree" {
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := btree.Open(ctx.Pool, idxRel)
		if err != nil {
			continue // index may not exist yet (e.g. freshly created)
		}
		_, _ = tree.VacuumIndexPages(deadTIDs)
	}
}

// isNailedCatalogOID returns true if oid is one of the four nailed local
// catalog relations whose relcache descriptors are cached in pg_internal.init.
// Vacuuming any of them touches their heap pages in a way that may invalidate
// cached descriptors, so the xact-marker hook must unlink both init files at
// commit time (M0106-0010 batched-31).
func isNailedCatalogOID(oid uint32) bool {
	switch oid {
	case catalog.RelationRelationId, // pg_class = 1259
		catalog.AttributeRelationId, // pg_attribute = 1249
		catalog.TypeRelationId,      // pg_type = 1247
		1255:                        // pg_proc (no catalog constant defined)
		return true
	}
	return false
}

// vacuumTableTargets resolves the *catalog.Table list to vacuum (so we can
// update RelFrozenXID after each pass).
func (o *vacuumOp) vacuumTableTargets(vs *parser.VacuumStmt) []*catalog.Table {
	cat := o.ctx.Catalog
	if len(vs.Targets) > 0 {
		var out []*catalog.Table
		for _, name := range vs.Targets {
			tbl, ok := cat.LookupTable(name)
			if !ok || tbl.Virtual {
				continue
			}
			out = append(out, tbl)
		}
		return out
	}
	if im, ok := cat.(*catalog.InMemory); ok {
		var out []*catalog.Table
		for _, tbl := range im.AllTables() {
			if !tbl.Virtual {
				out = append(out, tbl)
			}
		}
		return out
	}
	return nil
}

// vacuumTargets keeps backward compatibility (used by FSM tests).
func (o *vacuumOp) vacuumTargets(vs *parser.VacuumStmt) []storage.RelFileNode {
	tbls := o.vacuumTableTargets(vs)
	out := make([]storage.RelFileNode, 0, len(tbls))
	for _, t := range tbls {
		out = append(out, o.ctx.Catalog.RelFileNode(t))
	}
	return out
}
