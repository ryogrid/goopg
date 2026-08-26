package executor

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/commands/vacuum"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/storage/lmgr"
)

// defaultAutovacuumFreezeMaxAge mirrors upstream's boot value (200M XIDs);
// used when the GUC is absent from the session registry.
const defaultAutovacuumFreezeMaxAge = int64(200_000_000)

// lookupGUCInt reads an integer GUC's session-effective value via the
// context's setting hook; ok=false when the hook is nil, the name is
// unknown, or the value does not parse.
func (o *vacuumOp) lookupGUCInt(name string) (int64, bool) {
	if o.ctx == nil || o.ctx.GetSetting == nil {
		return 0, false
	}
	raw, ok := o.ctx.GetSetting(name)
	if !ok {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil {
		return 0, false
	}
	return v, true
}

// vacuumOp executes a VACUUM statement, running heap page-prune on the
// target relations and updating the FSM, VM, and relfrozenxid with the
// resulting state (M0046-0003/0004/0005). VACUUM without a target list
// vacuums all user tables.
type vacuumOp struct {
	plan *optimizer.Utility
	ctx  *Context
	done bool
}

func newVacuumOp(p *optimizer.Utility) *vacuumOp { return &vacuumOp{plan: p} }

func (o *vacuumOp) Schema() optimizer.Schema { return nil }

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

	// A database-wide VACUUM (no explicit target list) advances the cluster
	// datfrozenxid in pg_database via an in-place update (vac_update_datfrozenxid
	// → heap_inplace_update_scan). That in-place update locks the pg_database
	// tuple and waits for any concurrent uncommitted GRANT/REVOKE … ON DATABASE
	// (whose lock IS that tuple's xmax) to finish first. goopg has no real
	// pg_database heap tuple, so we replay the wait directly: block on the writer
	// XID recorded by the ACL change until it commits/aborts. Design 0118-0098
	// (intra-grant-inplace-db).
	if len(vs.Targets) == 0 {
		o.waitForDatabaseACLChange()
	}

	// Freeze cutoffs (M0046-0005 + parity bundle 03-design F2/F3):
	// FreezeLimit = nextXID − min(freeze_min_age, autovacuum_freeze_max_age/2),
	// clamped ≤ OldestXmin ALWAYS (an unclamped age-0 FREEZE limit would be
	// nextXID itself and would freeze in-flight xmins — upstream clamps at
	// vacuum.c:1213–1215). VACUUM (FREEZE) zeroes the effective min_age.
	nextXID := o.ctx.TxnMgr.NextXID()
	freezeMaxAge := int64(defaultAutovacuumFreezeMaxAge)
	if v, ok := o.lookupGUCInt("autovacuum_freeze_max_age"); ok && v > 0 {
		freezeMaxAge = v
	}
	freezeMinAge := o.ctx.FreezeMinAge // session vacuum_freeze_min_age (0 = off)
	freezeForced := vs.Freeze
	if freezeForced {
		freezeMinAge = 1 // any positive value + Aggressive ⇒ everything eligible freezes
	}
	var freezeBelow storage.TransactionID
	if freezeMinAge > 0 {
		eff := freezeMinAge
		if freezeMaxAge > 0 && freezeMaxAge/2 < eff {
			eff = freezeMaxAge / 2
		}
		fb := nextXID - storage.TransactionID(eff)
		if oldest := o.ctx.TxnMgr.OldestXmin(); fb > oldest {
			fb = oldest
		}
		if fb > 0 {
			freezeBelow = fb
		}
	}

	// Aggressive determination is PER TABLE (depends on each relation's
	// relfrozenxid age) and happens inside the target loop via the closure
	// below. Base semantics (vacuum.c:1244–1273): full scan, no VM skips;
	// relfrozenxid age treats InvalidTransactionID/0 as INFINITE — upstream
	// creates heaps with relfrozenxid = InvalidTransactionId (heap.c:325) and
	// the unsigned compare makes a never-vacuumed table's first VACUUM
	// aggressive.
	freezeTableAge := freezeMaxAge * 95 / 100 // vacuum.c:1246 cap
	if v, ok := o.lookupGUCInt("vacuum_freeze_table_age"); ok && v > 0 {
		fta := int64(v)
		if fta > freezeTableAge {
			fta = freezeTableAge
		}
		freezeTableAge = fta
	}
	aggressiveFor := func(tbl *catalog.Table) bool {
		if freezeForced || vs.Full || vs.DisablePageSkipping || freezeTableAge <= 0 {
			return true
		}
		var tableAge int64
		switch {
		case tbl.RelFrozenXID == storage.InvalidTransactionID || tbl.RelFrozenXID == 0:
			tableAge = int64(nextXID) // maximal
		default:
			if nextXID > tbl.RelFrozenXID {
				tableAge = int64(nextXID - tbl.RelFrozenXID)
			}
		}
		return tableAge >= freezeTableAge
	}
	// vacuum_truncate is a BOOL GUC ("on"/"off"); default on like upstream.
	truncate := true
	if o.ctx != nil && o.ctx.GetSetting != nil {
		if raw, ok := o.ctx.GetSetting("vacuum_truncate"); ok {
			switch strings.ToLower(strings.TrimSpace(raw)) {
			case "off", "false", "0", "no":
				truncate = false
			}
		}
	}
	if vs.NoTruncate {
		truncate = false
	}
	failsafeAge, _ := o.lookupGUCInt("vacuum_failsafe_age")

	opts := vacuum.VacuumOptions{
		FSM:         o.ctx.FSM,
		VM:          o.ctx.VM,
		FreezeBelow: freezeBelow,
		Truncate:    truncate,
		FailsafeAge: failsafeAge,
	}
	if d, ok := o.lookupGUCInt("vacuum_cost_delay"); ok && d > 0 {
		opts.CostDelayMS = d
		opts.CostLimit = 200
		opts.CostPageHit, _ = o.lookupGUCInt("vacuum_cost_page_hit")
		opts.CostPageMiss, _ = o.lookupGUCInt("vacuum_cost_page_miss")
		opts.CostPageDirty, _ = o.lookupGUCInt("vacuum_cost_page_dirty")
		if l, ok := o.lookupGUCInt("vacuum_cost_limit"); ok && l > 0 {
			opts.CostLimit = l
		}
	}

	// SKIP_LOCKED governs the per-relation lock taken to begin vacuuming.
	// PostgreSQL takes AccessExclusiveLock for VACUUM FULL and
	// ShareUpdateExclusiveLock otherwise (vacuum.c vacuum_open_relation); with
	// SKIP_LOCKED the acquire is conditional and a contended relation is skipped
	// instead of waited on. M0118-0008 (vacuum-skip-locked).
	lmMode := lmgr.ShareUpdateExclusiveLock
	if vs.Full {
		lmMode = lmgr.AccessExclusiveLock
	}
	targets, parents, terr := o.expandVacuumTargets(vs)
	if terr != nil {
		// A bad column list aborts the whole VACUUM ANALYZE (not suppressed).
		return nil, terr
	}
	for _, vt := range targets {
		tbl := vt.tbl
		// Maintenance-privilege check (vacuum_is_permitted_to_vacuum), performed
		// here — in the main per-target execution loop over the FLATTENED target
		// list, after partition expansion — rather than in expandVacuumTargets'
		// add() closure. This mirrors vacuum_rel() (vacuum.c:2124), which calls
		// vacuum_is_permitted_for_relation() once per flattened target; PG's
		// expand_vacuum_rel() explicitly does NOT check ownership when it appends
		// partition children to the target list (vacuum.c:1003-1005: "we do not
		// yet check the ownership of the partitions/tables ... Ownership will be
		// checked later on anyway"). So every target — parent or expanded child,
		// explicit or not — gets its own WARNING on denial, independent of the
		// others. Design doc: docs/design/m0134-0021-vacuum-partition-child-permission.md.
		//
		// Gated on an explicit target list: a database-wide `VACUUM;` (no
		// targets) goes through get_all_vacuum_rels upstream, which filters
		// non-owned relations out of the list SILENTLY before this loop even
		// runs (vacuum.c:1082 — a plain `continue`, no WARNING) — a distinct,
		// ledgered asymmetry this case does not touch (design doc "Scope
		// explicitly excluded"). goopg's database-wide arm does no filtering at
		// all yet; this check must not change that.
		if len(vs.Targets) > 0 && !maintenancePermitted(o.ctx, tbl) {
			o.ctx.AddWarning(fmt.Sprintf("permission denied to vacuum %q, skipping it", tbl.Name))
			continue
		}
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
		// A TEMPORARY relation is private to its owning backend, so reclamation
		// uses the session-local horizon (PG's GlobalVisTempRels) — a concurrent
		// session's older snapshot must NOT pin temp-row reclamation it can never
		// observe. Permanent relations keep the global OldestXmin (opts.Horizon
		// left 0 → vacuumCore derives it). horizons.spec (M0118-0009).
		relOpts := opts
		relOpts.Aggressive = aggressiveFor(tbl)
		if tbl.Temp {
			relOpts.Horizon = o.ctx.TxnMgr.OldestXminForProc(int32(o.ctx.Tx.Handle) - 1)
		}
		stats, err := vacuum.VacuumWithOptions(o.ctx.Pool, o.ctx.TxnMgr, rel, relOpts)
		if err == nil {
			// Publish reltuples / relpages to pg_class (vac_update_relstats).
			// reltuples is the count of tuples visible to a FRESH snapshot — the
			// "currently live" definition upstream uses — NOT the prune's
			// surviving-line-pointer count: a recently-dead tuple (deleted and
			// committed, but not yet removable because a concurrent backend holds
			// OldestXmin back) survives the prune yet must be excluded from
			// reltuples, exactly as the vacuum-no-cleanup-lock spec requires.
			// Preserves any per-column pg_statistic from a prior ANALYZE.
			// M0118-0008 (vacuum-no-cleanup-lock).
			if as, aerr := vacuum.Analyze(o.ctx.Pool, o.ctx.TxnMgr, rel, o.ctx.MultiXact); aerr == nil {
				o.ctx.Catalog.UpdateRelStats(tbl, as.Pages, int64(as.Rows))
			}
		}
		if err == nil {
			// Successful VACUUM resets n_dead_tup / n_ins_since_vacuum
			// (pgstat_relation_vacuum_rel). OID 0 (not yet nailed) skips.
			relStats.resetVacuumTriggers(tbl.OID)
		}
		// relfrozenxid skip-guard (vacuumlazy.c skippedallvis): a
		// non-aggressive pass that SKIPPED all-visible-but-not-all-frozen
		// pages cannot know their oldest unfrozen xmin and must not advance.
		guardedSkip := !relOpts.Aggressive && stats.SkippedAllVisible > 0
		if err == nil && freezeBelow > 0 && !guardedSkip && stats.NewFrozenXID != 0 {
			// Advance relfrozenxid to the lowest unfrozen xmin found.
			tbl.RelFrozenXID = stats.NewFrozenXID
		} else if err == nil && freezeBelow > 0 && !guardedSkip && stats.NewFrozenXID == 0 && stats.Frozen > 0 {
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

	// Advance pg_database.datfrozenxid on disk to the freshly recomputed
	// cluster horizon (vac_update_datfrozenxid; M0117-0008 Part B). Runs
	// unconditionally — like PG, whose VACOPT_VACUUM call site is
	// unconditional regardless of an explicit target list — but a failure
	// here must never fail the client's VACUUM: goopg's own CLOG truncation
	// already reads the horizon from catalog.InMemory.DatFrozenXID()
	// directly, so this step is pure external (standby/tooling) parity.
	_ = persistDatFrozenXID(o.ctx)
	return nil, EOF
}

// waitForDatabaseACLChange blocks until any uncommitted GRANT/REVOKE … ON
// DATABASE transaction has committed or aborted, mirroring PostgreSQL's
// heap_inplace_update_scan waiting on the pg_database tuple's xmax before a
// database-wide VACUUM advances datfrozenxid in place. The writer XID is
// recorded by execCompatNoop when it processes the ACL change. WaitForXID
// returns immediately when the recorded XID has already finished (or none was
// recorded), so this is a no-op in the common case. Design 0118-0098.
func (o *vacuumOp) waitForDatabaseACLChange() {
	im, ok := o.ctx.Catalog.(*catalog.InMemory)
	if !ok || o.ctx.TxnMgr == nil {
		return
	}
	xid := im.DatabaseACLChangeXID()
	if xid == storage.InvalidTransactionID || xid == o.ctx.Tx.XID {
		return
	}
	qctx := o.ctx.Ctx
	if qctx == nil {
		qctx = context.Background()
	}
	_ = o.ctx.TxnMgr.WaitForXID(qctx, xid)
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
func (o *vacuumOp) expandVacuumTargets(vs *parser.VacuumStmt) ([]vacuumTarget, []*catalog.Table, *ExecError) {
	// Named targets resolve through ctxPlanCatalog — the per-connection,
	// DB-scoped catalog SELECT plans against — mirroring expandAnalyzeTargets
	// (sibling paths change together): a raw ctx.Catalog.LookupTable keys off
	// DefaultDBOid, so `VACUUM lineitem` in db tpch silently skipped its
	// target. The database-wide arm below still enumerates DefaultDBOid's
	// namespace via deep copies (deferred; see ledger M0125-0028). M0125-0028.
	cat := ctxPlanCatalog(o.ctx)
	im, _ := o.ctx.Catalog.(*catalog.InMemory)
	nsOid := catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)
	var out []vacuumTarget
	var parents []*catalog.Table
	var add func(tbl *catalog.Table, explicit bool)
	// expandChildren records tbl as a partitioned parent (for the inheritance
	// ANALYZE pass) and recurses into its leaf partitions, WITHOUT adding tbl
	// itself to `out` (it has no storage). Deliberately independent of
	// whether tbl passed its own permission check below: PG's
	// expand_vacuum_rel() appends partition children to the target list
	// unconditionally, regardless of the named relation's own ownership
	// result (vacuum.c:1003-1005 — "we do not yet check the ownership of the
	// partitions/tables ... Ownership will be checked later on anyway"), so a
	// denied parent still yields independent per-child WARNINGs for any
	// denied child (postgres/src/test/regress/expected/vacuum.out:646-648,
	// "Only one partition owned by other user").
	expandChildren := func(tbl *catalog.Table) {
		if im == nil {
			return
		}
		parents = append(parents, tbl)
		for _, child := range im.PartitionChildren(tbl.OID, nsOid) {
			add(child, false)
		}
	}
	add = func(tbl *catalog.Table, explicit bool) {
		if tbl == nil || tbl.Virtual {
			return
		}
		if tbl.PartitionMethod != "" && im != nil {
			expandChildren(tbl)
			return
		}
		out = append(out, vacuumTarget{tbl: tbl, explicit: explicit})
	}
	if len(vs.Targets) > 0 {
		for i, name := range vs.Targets {
			tbl, ok := cat.LookupTable(name)
			if !ok {
				continue
			}
			// VACUUM ANALYZE with a per-relation column list (VACUUM ANALYZE
			// tab (col, ...)): validate the names — PG aborts the whole
			// statement on a bad column (42703/42701, analyze.c:372-400).
			// Plain VACUUM ignores va_cols (analyze_rel is never reached), so
			// only the ANALYZE verb validates.
			if vs.Analyze && i < len(vs.TargetCols) && vs.TargetCols[i] != nil {
				if cerr := resolveAnalyzeColumns(tbl, vs.TargetCols[i], vs.Pos()); cerr != nil {
					return nil, nil, cerr
				}
			}
			// Maintenance-privilege check (vacuum_is_permitted_to_vacuum) on the
			// EXPLICITLY named relation, at expansion time — mirrors
			// expand_vacuum_rel's call to vacuum_is_permitted_for_relation()
			// (vacuum.c:974), which is the ONLY site that ever checks this
			// relation's own permission (a denial here excludes it from the
			// flattened target list entirely, so vacuum_rel()'s per-target
			// check — the main loop's maintenancePermitted call in Next() — is
			// never reached for it). A partitioned table's children are still
			// expanded and independently checked regardless of this result.
			// M0118-0008 (vacuum-conflict); design doc
			// docs/design/m0134-0021-vacuum-partition-child-permission.md.
			if !maintenancePermitted(o.ctx, tbl) {
				o.ctx.AddWarning(fmt.Sprintf("permission denied to vacuum %q, skipping it", tbl.Name))
				if tbl.PartitionMethod != "" && im != nil {
					expandChildren(tbl)
				}
				continue
			}
			add(tbl, true)
		}
		return out, parents, nil
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
	return out, parents, nil
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
	for _, child := range im.PartitionChildren(parent.OID, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid)) {
		if child.PartitionMethod != "" {
			analyzeInheritanceWait(ctx, child) // sub-partitioned: recurse
			continue
		}
		rel := ctx.Catalog.RelFileNode(child)
		_ = ctx.acquireRelLockMaybeTransient(rel, lmgr.AccessShareLock)
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
	// Table OIDs come from the single cluster-wide counter, so the AllDBs
	// variant is exact — and required: the dbOid-pinned LookupTableByOID keys
	// off DefaultDBOid, which never holds a non-default database's tables, so
	// every per-DB ANALYZE/VACUUM target read as "concurrently dropped" and
	// was silently skipped right after taking its lock. M0125-0028.
	_, _, exists := im.LookupTableByOIDAllDBs(tbl.OID)
	return exists
}

// vacuumIndexes removes stale B-tree index entries that point to dead heap
// tuples collected during the heap vacuum pass. Empty index leaf pages are
// deleted and the tree is compacted if fully empty (M0047-0002).
func vacuumIndexes(ctx *Context, tbl *catalog.Table, deadTIDs []storage.ItemPointer) {
	indexes := ctx.Catalog.IndexesOnTable(tbl, catalog.NamespaceDBOid(ctx.CurrentDatabaseOid))
	for _, idx := range indexes {
		if idx.Method != "btree" {
			continue
		}
		idxRel := ctx.Catalog.IndexRelFileNode(idx)
		tree, err := openIndexBTree(ctx, idx, idxRel)
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
	// Same per-connection DB-scoped resolution as expandVacuumTargets; the two
	// walk the same target list and must agree on what it resolves to.
	// M0125-0028.
	cat := ctxPlanCatalog(o.ctx)
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
