package executor

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/optimizer"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/storage/lmgr"
)

// analyzeOp drives `ANALYZE [target [, …]]` against the
// storage layer. For each target relation it walks every
// block, decodes visible heap tuples, reservoir-samples them,
// and derives per-table + per-column statistics that the
// catalog stores for the planner to consult later.
//
// v0 collects:
//
//   - RowCount: visible-tuple count under a fresh
//     ReadCommitted snapshot (matches upstream's reltuples
//     definition; exact, not sample-scaled).
//   - Pages: raw block count.
//   - AvgWidth: total decoded-row bytes / RowCount.
//   - Per-column NDistinct, NullFrac, MCV list, and equi-depth
//     histogram (computed from the sample).
//
// The sampling collector replaces M0003's full-distinct-set
// walk; see docs/design/0006-0001-sampling-and-mcv-histograms.md.
// Catalog persistence and planner consumption of the new MCV /
// histogram payloads land in subsequent M0006 loops.
type analyzeOp struct {
	stmt *parser.AnalyzeStmt
	done bool
	ctx  *Context
}

func newAnalyzeOp(stmt *parser.AnalyzeStmt) *analyzeOp {
	return &analyzeOp{stmt: stmt}
}

func (o *analyzeOp) Schema() optimizer.Schema { return nil }

func (o *analyzeOp) Open(ctx *Context) error {
	o.ctx = ctx
	return nil
}

func (o *analyzeOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}
	o.done = true
	if o.ctx == nil || o.ctx.Pool == nil || o.ctx.Catalog == nil || o.ctx.TxnMgr == nil {
		return nil, &ExecError{Code: "0A000", Pos: o.stmt.Pos(), Message: "ANALYZE requires Pool/Catalog/TxnMgr in Context"}
	}
	targets, parents, terr := o.expandAnalyzeTargets()
	if terr != nil {
		return nil, terr
	}
	for _, at := range targets {
		tbl := at.tbl
		// Maintenance-privilege check (vacuum_is_permitted_to_vacuum, analyze
		// verb), performed here — in the main per-target execution loop over
		// the FLATTENED target list, after partition expansion — rather than
		// in expandAnalyzeTargets' add() closure. SIBLING of the identical
		// check in operators_vacuum.go's Next() loop (Hard-won Rule #2):
		// mirrors analyze_rel() (analyze.c:156), which calls
		// vacuum_is_permitted_for_relation() once per flattened target; PG's
		// expand_vacuum_rel() does not check ownership when it appends
		// partition children (vacuum.c:1003-1005). So every target — parent
		// or expanded child, explicit or not — gets its own WARNING on
		// denial, independent of the others. Design doc:
		// docs/design/m0134-0021-vacuum-partition-child-permission.md.
		//
		// Gated on an explicit target list: bare `ANALYZE;` (no targets) already
		// filters non-owned relations SILENTLY in expandAnalyzeTargets (matching
		// upstream's get_all_vacuum_rels, vacuum.c:1082 — a plain `continue`, no
		// WARNING), so those denied relations never reach `targets` at all; this
		// check only needs to fire for the explicit-target-list path, where a
		// partition child can be denied independently of its parent.
		if len(o.stmt.Targets) > 0 && !maintenancePermitted(o.ctx, tbl) {
			o.ctx.AddWarning(fmt.Sprintf("permission denied to analyze %q, skipping it", tbl.Name))
			continue
		}
		rel := o.ctx.Catalog.RelFileNode(tbl)
		// ANALYZE takes the per-relation ShareUpdateExclusiveLock (analyze.c).
		// With SKIP_LOCKED the acquire is conditional and a contended relation is
		// skipped — with a WARNING only when the user named it explicitly;
		// partition children reached by expanding a partitioned table are skipped
		// silently. Without SKIP_LOCKED the acquire blocks, so ANALYZE waits
		// behind a conflicting holder such as LOCK ... IN SHARE MODE. M0118-0008.
		if o.stmt.SkipLocked {
			if !o.ctx.tryAcquireMaintenanceLock(rel, lmgr.ShareUpdateExclusiveLock) {
				if at.explicit {
					o.ctx.AddWarning(fmt.Sprintf("skipping analyze of %q --- lock not available", tbl.Name))
				}
				continue
			}
		} else if err := o.ctx.acquireRelLockMaybeTransient(rel, lmgr.ShareUpdateExclusiveLock); err != nil {
			continue
		}
		// After taking the lock the relation may have been dropped by a
		// transaction that committed while we waited (see vacuumOp). Skip it,
		// with a WARNING only for an explicitly named target. M0118-0008
		// (vacuum-concurrent-drop).
		if !relationStillExists(o.ctx, tbl) {
			if at.explicit {
				o.ctx.AddWarning(fmt.Sprintf("skipping analyze of %q --- relation no longer exists", tbl.Name))
			}
			continue
		}
		stats, err := analyzeRelationCtx(o.ctx, tbl)
		if err != nil {
			return nil, &ExecError{Code: "XX000", Pos: o.stmt.Pos(), Message: err.Error()}
		}
		o.ctx.Catalog.SetTableStats(tbl, stats)
		// M0112: persist stats to pg_statistic so they survive restart.
		if werr := persistStatsToPGStatistic(o.ctx, tbl, stats); werr != nil {
			// Non-fatal: stats are in memory; log and continue.
			_ = werr
		}
		relStats.resetAnalyzeTriggers(tbl.OID)
	}
	// Inheritance-tree statistics for partitioned parents read every leaf
	// partition under a blocking AccessShareLock (SKIP_LOCKED does not cover
	// this scan), so ANALYZE of a partitioned table waits on a child held in a
	// conflicting mode by another session. M0118-0008.
	for _, parent := range parents {
		analyzeInheritanceWait(o.ctx, parent)
	}

	// Partitioned-parent aggregation (parity bundle F5-deferred→done-lite):
	// roll up each partitioned parent's RowCount/Pages from its children so
	// the planner's relsize path sees a non-stale total. Column-level stats
	// for parents remain unset (planner falls back per column), which is
	// strictly better than the previous no-op.
	for _, parent := range parents {
		if parent == nil || parent.PartitionMethod == "" {
			continue
		}
		kids := o.partitionChildren(parent)
		if len(kids) == 0 {
			continue
		}
		var rows int64
		pages := 0
		for _, k := range kids {
			if k.Stats == nil {
				continue
			}
			rows += k.Stats.RowCount
			pages += k.Stats.Pages
		}
		parent.Stats = &catalog.TableStats{RowCount: rows, Pages: pages, Analyzed: true}
		o.ctx.Catalog.SetTableStats(parent, parent.Stats)
		relStats.resetAnalyzeTriggers(parent.OID)
	}
	return nil, EOF
}

// partitionChildren resolves the direct leaf partitions of a partitioned
// parent through the concrete catalog, peeling wrapper catalogs exactly like
// expandVacuumTargets does. Returns nil when unsupported.
func (o *analyzeOp) partitionChildren(parent *catalog.Table) []*catalog.Table {
	type unwrapper interface{ Unwrap() catalog.Catalog }
	base := o.ctx.Catalog
	for {
		if c, ok := base.(*catalog.InMemory); ok {
			return c.PartitionChildren(parent.OID)
		}
		if u, ok := base.(unwrapper); ok {
			base = u.Unwrap()
		} else {
			return nil
		}
	}
}

// expandAnalyzeTargets resolves the ANALYZE target list into concrete heap
// relations, expanding any partitioned table into its leaf partitions (marked
// non-explicit ⇒ silent SKIP_LOCKED skip) and returning the partitioned parents
// encountered for the inheritance-statistics AccessShare scan. A named target
// that does not exist is a hard 42P01 error, matching the prior behaviour.
//
// Named targets resolve through ctxPlanCatalog — the same per-connection,
// DB-scoped catalog SELECT plans against. A raw ctx.Catalog.LookupTable keys
// off DefaultDBOid's namespace, which holds none of a non-default database's
// tables, so `ANALYZE lineitem` in db tpch raised 42P01 while
// `SELECT ... FROM lineitem` worked (ledger `bench-reorg ANALYZE-scope`).
// M0125-0028.
// resolveAnalyzeColumns validates an ANALYZE / VACUUM ANALYZE per-relation
// column list, reproducing PG's attnameAttNum + analyze_rel duplicate check
// (postgres/src/backend/parser/parse_relation.c:3589-3609,
// postgres/src/backend/commands/analyze.c:372-400): a case-sensitive lookup
// that skips dropped columns; the first unresolved name is 42703 and a repeat
// (same resolved column Ordinal twice) is 42701. Validation only — the
// per-column stats restriction is deferred. NOT InMemory.LookupColumn, which is
// case-insensitive and ignores Dropped.
func resolveAnalyzeColumns(tbl *catalog.Table, cols []string, pos int) *ExecError {
	seen := make(map[int]bool)
	for _, name := range cols {
		var col *catalog.Column
		for i := range tbl.Columns {
			if tbl.Columns[i].Name == name && !tbl.Columns[i].Dropped {
				col = &tbl.Columns[i]
				break
			}
		}
		if col == nil {
			return &ExecError{Code: "42703", Pos: pos, Message: fmt.Sprintf("column %q of relation %q does not exist", name, tbl.Name)}
		}
		if seen[col.Ordinal] {
			return &ExecError{Code: "42701", Pos: pos, Message: fmt.Sprintf("column %q of relation %q appears more than once", name, tbl.Name)}
		}
		seen[col.Ordinal] = true
	}
	return nil
}

func (o *analyzeOp) expandAnalyzeTargets() ([]vacuumTarget, []*catalog.Table, *ExecError) {
	cat := ctxPlanCatalog(o.ctx)
	// Partition-child expansion needs the concrete InMemory catalog; peel it
	// from the raw Context catalog, never the (possibly SearchPathCatalog-
	// wrapped) plan catalog.
	im, _ := o.ctx.Catalog.(*catalog.InMemory)
	nsOid := catalog.NamespaceDBOid(o.ctx.CurrentDatabaseOid)
	var out []vacuumTarget
	var parents []*catalog.Table
	var add func(tbl *catalog.Table, explicit bool)
	// expandChildren records tbl as a partitioned parent (for the inheritance
	// ANALYZE pass) and recurses into its leaf partitions, WITHOUT adding tbl
	// itself to `out` (it has no storage). SIBLING of the identical helper in
	// operators_vacuum.go's expandVacuumTargets (Hard-won Rule #2):
	// deliberately independent of whether tbl passed its own permission check
	// below, mirroring PG's expand_vacuum_rel(), which appends partition
	// children unconditionally regardless of the named relation's own
	// ownership result (vacuum.c:1003-1005), so a denied parent still yields
	// independent per-child WARNINGs for any denied child
	// (postgres/src/test/regress/expected/vacuum.out:646-648, "Only one
	// partition owned by other user").
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
	if len(o.stmt.Targets) == 0 {
		// Bare ANALYZE: every relation in the CURRENT database, mirroring
		// upstream's get_all_vacuum_rels (postgres/src/backend/commands/
		// vacuum.c). Live handles, not AllTables' deep copies — SetTableStats
		// publishes onto the canonical Table pointer, so a copy would take the
		// scan and drop its result. All skips are silent (explicit=false):
		// upstream applies the ownership filter in get_all_vacuum_rels without
		// logging, and analyze_rel silently returns for another session's temp
		// relation (RELATION_IS_OTHER_TEMP). analyze_rel's other silent skip —
		// pg_statistic itself — cannot arise here: the executor catalog
		// registers no heap-backed system relations, so UserTableHandles
		// yields user relations (and matviews) only. Partitioned parents join
		// the inheritance pass but are NOT expanded: their leaves are their
		// own namespace entries, so expanding the parent too would analyze
		// each leaf twice. M0125-0028.
		if im != nil {
			owner := sessionTempOwner(o.ctx)
			for _, tbl := range im.UserTableHandles(nsOid) {
				if tbl.Temp && tbl.TempOwner != "" && tbl.TempOwner != owner {
					continue
				}
				if !maintenancePermitted(o.ctx, tbl) {
					continue
				}
				if tbl.PartitionMethod != "" {
					parents = append(parents, tbl)
					continue
				}
				out = append(out, vacuumTarget{tbl: tbl, explicit: false})
			}
		}
		return out, parents, nil
	}
	for i, name := range o.stmt.Targets {
		tbl, ok := cat.LookupTable(name)
		if !ok {
			return nil, nil, &ExecError{Code: "42P01", Pos: o.stmt.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		// Per-relation column list (ANALYZE tab (col, ...)): validate the names
		// before the permission check. PG resolves va_cols in analyze_rel
		// (analyze.c:372-400) and aborts the statement on a bad column
		// (42703/42701). No per-column stats restriction yet (deferred).
		if i < len(o.stmt.TargetCols) && o.stmt.TargetCols[i] != nil {
			if cerr := resolveAnalyzeColumns(tbl, o.stmt.TargetCols[i], o.stmt.Pos()); cerr != nil {
				return nil, nil, cerr
			}
		}
		// Maintenance-privilege check (vacuum_is_permitted_to_vacuum, analyze
		// verb) on the EXPLICITLY named relation, at expansion time — mirrors
		// expand_vacuum_rel's call to vacuum_is_permitted_for_relation()
		// (vacuum.c:974), which is the ONLY site that ever checks this
		// relation's own permission (a denial here excludes it from the
		// flattened target list entirely, so analyze_rel()'s per-target check
		// — the main loop's maintenancePermitted call in Next() — is never
		// reached for it). A partitioned table's children are still expanded
		// and independently checked regardless of this result. M0118-0008
		// (vacuum-conflict); design doc
		// docs/design/m0134-0021-vacuum-partition-child-permission.md.
		if !maintenancePermitted(o.ctx, tbl) {
			o.ctx.AddWarning(fmt.Sprintf("permission denied to analyze %q, skipping it", tbl.Name))
			if tbl.PartitionMethod != "" && im != nil {
				expandChildren(tbl)
			}
			continue
		}
		add(tbl, true)
	}
	return out, parents, nil
}

func (o *analyzeOp) Close() error { return nil }

// persistStatsToPGStatistic writes per-column statistics to the pg_statistic
// heap table (OID 2619) so they survive a server restart (M0112). One row is
// written per column that has statistics. Existing rows for the same
// (starelid, staattnum, stainherit) are not deleted first — the heap grows
// monotonically; startup reads the most recent live tuple. Non-fatal on error.
//
// Both heaps route to the CONNECTION's database (tableCatalogHeapDBOid, the
// same routing pg_class/pg_attribute writes use): pg_statistic is a per-
// database catalog in PG, and until M0125-0029 this wrote every database's
// rows into base/<DefaultDBOid>/2619, where the startup reload — which looks
// relids up in the database it is scanning — could never resolve a per-DB
// table's OID, so tpch/tpcds stats silently evaporated on restart.
//
// The relation's SIZE (reltuples/relpages) additionally lands in the
// goopg-private per-DB sidecar heap (GoopgRelStatsRelationId): pg_statistic
// has no slot for it and goopg's pg_class is virtual, so without the sidecar
// the restored per-column stats rode on a RowCount=0 relation (ledger pq-P6).
// M0125-0029, under the 2026-07-30(b) directive's PG-faithfulness waiver.
func persistStatsToPGStatistic(ctx *Context, tbl *catalog.Table, stats *catalog.TableStats) error {
	if ctx == nil || ctx.Pool == nil || tbl == nil || stats == nil {
		return nil
	}
	dbOid := tableCatalogHeapDBOid(ctx)
	statRel := storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.StatisticRelationId,
		Fork:   storage.MainFork,
	}
	cols := pgStatisticColumnsPG18()
	// One failing column must not sink the others or the size row below:
	// a wide-text column's histogram (e.g. TPC-H partsupp.ps_comment,
	// varchar(199) × up to 101 bounds) builds a pg_statistic tuple larger
	// than a heap page, and goopg's catalog heap writer does not TOAST, so
	// PageAddHeapTuple rejects it even on a fresh page. Real PG toasts these
	// rows (pg_statistic has a toast relation, pg_statistic.h) — deferral-
	// ledger row, M0125-0029. Measured on the TPC-H bench cluster
	// 2026-07-30: the early return here left orders/customer/partsupp with
	// NO trailing-column rows and no size row, while lineitem/part/… — whose
	// comment histograms fit — persisted fully. Keep the first error for the
	// caller's (non-fatal) bookkeeping, write everything that fits.
	var firstErr error
	for i, cs := range stats.Columns {
		if i >= len(tbl.Columns) {
			break
		}
		col := tbl.Columns[i]
		if col.StatTarget != nil && *col.StatTarget == 0 {
			// SET STATISTICS 0 disables collection for this column
			// (examine_attribute returns NULL upstream); write no row.
			continue
		}
		attNum := int16(col.Ordinal + 1)
		row := buildUserPGStatisticRow(tbl.OID, attNum, cs)
		if _, err := writeHeapRowCanonical(ctx, statRel, cols, row); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pg_statistic col %q: %w", col.Name, err)
		}
	}
	relStatsRel := storage.RelFileNode{
		DBOid:  dbOid,
		RelOid: catalog.GoopgRelStatsRelationId,
		Fork:   storage.MainFork,
	}
	sizeRow := Row{
		NewIntDatum(int64(tbl.OID)),     // starelid
		NewIntDatum(stats.RowCount),     // rowcount (reltuples)
		NewIntDatum(int64(stats.Pages)), // pages (relpages)
	}
	if _, err := writeHeapRowCanonical(ctx, relStatsRel, GoopgRelStatsColumns(), sizeRow); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("goopg_relstats %q: %w", tbl.Name, err)
	}
	return firstErr
}

// GoopgRelStatsColumns is the row layout of the goopg-private relation-size
// sidecar heap (catalog.GoopgRelStatsRelationId — see the constant's comment
// for why it exists and why it is invisible to a PG standby). Exported for
// initdb's startup reload, which decodes it with the generic
// DecodeRowIntoMctxPGTuple exactly like any converted catalog. Append-only,
// most recent live tuple per starelid wins — the same convention as the
// pg_statistic writer above. AvgWidth is deliberately not persisted: nothing
// reads TableStats.AvgWidth today, and a column added here later is a format
// change to a goopg-private relation, which costs nothing PG-facing.
func GoopgRelStatsColumns() []catalog.Column {
	return []catalog.Column{
		{Name: "starelid", Type: catalog.Type{Name: "oid"}},
		{Name: "rowcount", Type: catalog.Type{Name: "int8"}},
		{Name: "pages", Type: catalog.Type{Name: "int8"}},
	}
}

// upstreamDefaultStatsTarget mirrors upstream PG's
// default_statistics_target GUC bootval (see
// postgres/src/backend/utils/misc/guc_tables.c).
const upstreamDefaultStatsTarget = 100

// upstreamSampleMultiplier is upstream's `targrows = stats_target
// * 300` constant from postgres/src/backend/commands/analyze.c
// `do_analyze_rel`.
const upstreamSampleMultiplier = 300

// mcvFreqMargin is upstream's MCV_THRESHOLD margin from
// postgres/src/backend/commands/analyze.c `compute_scalar_stats`:
// a value qualifies for the MCV list when its sample frequency
// exceeds the average frequency of the remaining values by at
// least this multiplier.
const mcvFreqMargin = 1.25

// analyzeRelationCtx is the Context-aware entry point that
// honours StatsTarget / AnalyzeRandSeed.
func analyzeRelationCtx(ctx *Context, tbl *catalog.Table) (*catalog.TableStats, error) {
	target := ctx.StatsTarget
	if target <= 0 {
		target = upstreamDefaultStatsTarget
	}
	seed := ctx.AnalyzeRandSeed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	return analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, target, rand.New(rand.NewSource(seed)), ctx.MultiXact, ctx)
}

// analyzeRelation is kept as a thin wrapper for tests that don't
// thread a Context — it uses the upstream-default stats target
// and a wall-clock-seeded sampler.
func analyzeRelation(pool *storage.Pool, mgr *transam.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
	// nil store: analyzeRelation is the test-only convenience wrapper with no
	// executor.Context (hence no MultiXact) in scope. M0118-0003.
	return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(time.Now().UnixNano())), nil, nil)
}

// analyzeRelationWith walks every block of tbl under a fresh
// snapshot, decodes visible tuples via the executor codec,
// reservoir-samples them with `targrows = target *
// upstreamSampleMultiplier`, and computes per-table + per-column
// statistics from the sample (RowCount and Pages remain exact).
// dsCtx supplies session-GUC reachability for DateStyle-aware MCV/
// histogram-bound rendering (formatDatumDateStyle); pass nil where no
// session context is available (falls back to ISO/MDY, matching
// Datum.Format()'s pre-existing hardcoded default).
func analyzeRelationWith(pool *storage.Pool, mgr *transam.Manager, cat catalog.Catalog, tbl *catalog.Table, target int, rng *rand.Rand, mxs *multixact.Store, dsCtx *Context) (*catalog.TableStats, error) {
	rel := cat.RelFileNode(tbl)

	tx, err := mgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		return nil, err
	}
	defer mgr.Rollback(tx)
	snap, err := mgr.SnapshotFor(tx)
	if err != nil {
		return nil, err
	}
	nBlocks, err := pool.NBlocks(rel)
	if err != nil {
		return nil, err
	}

	sampleCap := target * upstreamSampleMultiplier
	if sampleCap < 1 {
		sampleCap = 1
	}
	reservoir := make([]Row, 0, sampleCap)

	stats := &catalog.TableStats{
		Pages:   int(nBlocks),
		Columns: make([]catalog.ColumnStats, len(tbl.Columns)),
		// This IS the analyze cycle, so the counters below are measured —
		// including a measured zero for an empty relation. See
		// TableStats.Analyzed for why that distinction is recorded
		// explicitly instead of inferred from RowCount. M0125-0003.
		Analyzed: true,
	}
	var totalBytes int64
	var seen int64

	for blk := storage.BlockNumber(0); blk < nBlocks; blk++ {
		slot, err := pool.Pin(storage.BufferTag{Rel: rel, Block: blk})
		if err != nil {
			return nil, err
		}
		page := slot.Page()
		if storage.IsNew(page) {
			pool.Unpin(slot)
			continue
		}
		count, err := storage.PageLinePointerCount(page)
		if err != nil {
			pool.Unpin(slot)
			return nil, err
		}
		for s := uint16(1); s <= uint16(count); s++ {
			t, perr := storage.PageGetHeapTuple(page, s)
			if perr != nil {
				if errors.Is(perr, storage.ErrUnsupportedItem) || errors.Is(perr, storage.ErrInvalidSlot) {
					continue
				}
				pool.Unpin(slot)
				return nil, perr
			}
			// MultiXact store threaded from the caller (ctx.MultiXact for the
			// live SQL ANALYZE path; nil for the test-only analyzeRelation
			// wrapper). Resolves an updater-bearing multi xmax to its updater
			// before judging visibility — a stats-sampling scan must not
			// undercount a live, only-row-locked tuple as invisible. M0118-0003.
			var curcid storage.CommandId = storage.InvalidCommandId
			var combo *transam.ComboCIDStore
			if dsCtx != nil {
				curcid = dsCtx.CmdID
				combo = dsCtx.comboStore()
			}
			if !transam.TupleVisible(t.Header, snap, tx.XID, curcid, combo, mxs) {
				continue
			}
			// Decode the PG-physical tuple body using the header (natts +
			// null bitmap). Single on-disk row format since M0111-0002.
			row := make(Row, len(tbl.Columns))
			natts := int(t.Header.Infomask2 & 0x07FF)
			derr := DecodeRowIntoMctxPGTuple(row, tbl.Columns, t.Data, t.Bitmap, natts, nil)
			if derr != nil {
				pool.Unpin(slot)
				return nil, fmt.Errorf("ANALYZE %s slot=%d: %w", tbl.QualifiedName(), s, derr)
			}
			stats.RowCount++
			totalBytes += int64(int(t.Header.Hoff) + len(t.Data))

			// Algorithm R: fill the reservoir, then for each
			// subsequent row replace a uniformly-chosen slot
			// with probability sampleCap/seen.
			if seen < int64(sampleCap) {
				reservoir = append(reservoir, row)
			} else {
				j := rng.Int63n(seen + 1)
				if j < int64(sampleCap) {
					reservoir[j] = row
				}
			}
			seen++
		}
		pool.Unpin(slot)
	}

	if stats.RowCount > 0 {
		stats.AvgWidth = float64(totalBytes) / float64(stats.RowCount)
	}

	for i := range tbl.Columns {
		colTarget, ok := columnStatsTarget(&tbl.Columns[i], target)
		if !ok {
			continue
		}
		stats.Columns[i] = computeColumnStats(reservoir, i, colTarget, stats.RowCount, dsCtx)
		// Honor a per-column `n_distinct` attribute option, mirroring
		// upstream's override in compute_index_stats/do_analyze_rel
		// (postgres/src/backend/commands/analyze.c:571-581): a manual
		// n_distinct baked into the stored statistics at ANALYZE time,
		// so the planner consults it like any other stadistinct value.
		if nd, ok := columnNDistinctOverride(&tbl.Columns[i], stats.RowCount); ok {
			stats.Columns[i].NDistinct = nd
		}
	}
	return stats, nil
}

// columnNDistinctOverride resolves a per-column `n_distinct` attribute option
// (set via `ALTER TABLE ... ALTER COLUMN ... SET (n_distinct = <v>)`, stored on
// catalog.Column.Options) into an absolute distinct-value count for the given
// row count, mirroring upstream's stadistinct convention
// (postgres/src/backend/utils/adt/selfuncs.c get_variable_numdistinct and the
// override in analyze.c):
//
//   - v == 0 (or unset): no override — ok is false.
//   - v  > 0: absolute number of distinct values (rounded to an integer).
//   - v  < 0: a fraction, clamped to the valid [-1, 0) range; the estimate is
//     |v| * rowCount (so -1 ⇒ all rows distinct, -0.5 ⇒ each value twice).
//
// Only the non-inherited `n_distinct` flavor is honored: goopg's ANALYZE is a
// single-relation (non-inherited) scan, so `n_distinct_inherited` — which
// upstream applies only during the inheritance-tree pass — never fires here.
func columnNDistinctOverride(col *catalog.Column, rowCount int64) (int64, bool) {
	for _, opt := range col.Options {
		name, val, found := strings.Cut(opt, "=")
		if !found || !strings.EqualFold(strings.TrimSpace(name), "n_distinct") {
			continue
		}
		v, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
		if err != nil || v == 0 {
			return 0, false
		}
		if v > 0 {
			return int64(v + 0.5), true
		}
		// Negative: a fraction of the table's rows. PG's validated range
		// floors at -1; clamp defensively in case an out-of-range value was
		// stored (goopg's parser does not validate reloption bounds).
		if v < -1 {
			v = -1
		}
		nd := int64(-v*float64(rowCount) + 0.5)
		if nd < 1 && rowCount > 0 {
			nd = 1
		}
		return nd, true
	}
	return 0, false
}

// columnStatsTarget resolves the effective sampling target for one column,
// honoring `ALTER TABLE ... ALTER COLUMN ... SET STATISTICS n`
// (catalog.Column.StatTarget) over the table-wide target. Mirrors
// examine_attribute (postgres/src/backend/commands/analyze.c): an unset
// override (nil) falls back to tableTarget, an override of 0 means "don't
// analyze this column" (ok=false — the caller must not emit a
// pg_statistic row either), and a positive override is used verbatim.
func columnStatsTarget(col *catalog.Column, tableTarget int) (target int, ok bool) {
	if col.StatTarget == nil {
		return tableTarget, true
	}
	if *col.StatTarget == 0 {
		return 0, false
	}
	return *col.StatTarget, true
}

// ndistinctEstimate scales a sample's distinct-value count up to the whole
// relation, mirroring upstream's `compute_scalar_stats` ndistinct block
// (postgres/src/backend/commands/analyze.c:2588-2648) branch for branch. It
// returns the ABSOLUTE estimate; PG's signed stadistinct convention (negative
// = a fraction of the row count) is reconstructed by the caller, which stores
// both forms — see catalog.ColumnStats.StaDistinct.
//
// Why this exists (M0127-P5.6-e-iii): goopg used to store `len(freq)` — the
// raw SAMPLE distinct count — as the table's ndistinct. With the default
// stats target that caps at ~30,000, so a 1.5 M-row unique key read as 30,000
// and every join above it divided |L|*|R| by a number 50x too small. The
// estimate audit (09 §5.3) traced Q9's 124.7x over-estimate through exactly
// that saturation, and it is why the join-key coordinate correction measured
// as a regression when applied on its own: a saturated nd compounds up the
// join chain.
//
// The three branches upstream distinguishes matter, and none is a rounding
// detail:
//
//   - nmultiple == 0: nothing repeated in the sample, so assume a unique
//     column and scale with the row count (discounted for NULLs). This is the
//     case that was 50x wrong before.
//   - nmultiple == ndistinct: every sampled value repeated, so assume the
//     column really does have only these values (boolean/enum shapes). The
//     sample count IS the answer here — do not scale it.
//   - otherwise: the Haas-Stokes Duj1 estimator n*d / (n - f1 + f1*n/N),
//     clamped to [d, N]. f1 is the number of values seen exactly once.
//
// goopg has no `toowide_cnt` (no width-truncated sample values), so upstream's
// toowide terms are constant-folded to zero.
func ndistinctEstimate(sampleDistinct, nmultiple, nonNull int, nullFrac float64, totalRows int64) float64 {
	if sampleDistinct == 0 || nonNull == 0 {
		return 0
	}
	// N: the relation's non-NULL row count. Upstream's
	// `totalrows * (1.0 - stats->stanullfrac)`.
	N := float64(totalRows) * (1.0 - nullFrac)
	if totalRows <= 0 {
		// No measured row count (the test-only wrapper, or a relation whose
		// scan produced no count). Fall back to the sample's own distinct
		// count — the pre-M0127 behaviour, which is at least a lower bound.
		return float64(sampleDistinct)
	}

	switch {
	case nmultiple == 0:
		// Unique column: upstream stores -1.0 * (1 - nullfrac), i.e. one
		// distinct value per non-NULL row.
		return N
	case nmultiple == sampleDistinct:
		// Every sampled value repeated — the column's whole value set is in
		// the sample.
		return float64(sampleDistinct)
	}

	f1 := float64(sampleDistinct - nmultiple)
	d := f1 + float64(nmultiple)
	// n = samplerows - null_cnt, which is exactly the non-NULL sample count.
	n := float64(nonNull)
	var est float64
	if N > 0 {
		denom := (n - f1) + f1*n/N
		if denom > 0 {
			est = (n * d) / denom
		}
	}
	// Clamp to sane range in case of roundoff error (upstream's wording).
	if est < d {
		est = d
	}
	if est > N {
		est = N
	}
	return math.Floor(est + 0.5)
}

// computeColumnStats derives the per-column NDistinct / NullFrac
// / MCV / Histogram from the sample. Mirrors the bookkeeping in
// upstream's `compute_scalar_stats` while staying within the v0
// type set.
//
// datumVariablePayloadWidth returns the variable-width payload bytes of a
// Datum — the bytes BEYOND the fixed 48-byte Datum struct that live in Buf
// or an arena. M0128-P3.1: this is the per-column contribution to avgVarBytes.
func datumVariablePayloadWidth(d Datum) int {
	switch d.Kind {
	case KindString, KindBytes, KindEnum:
		if d.ArenaID != 0 {
			return int(uint32(d.Int & 0xFFFFFFFF))
		}
		return len(d.Buf)
	case KindNumeric:
		if d.Flags&flagBigNumeric != 0 {
			return int(uint32(d.Int & 0xFFFFFFFF))
		}
		return 0 // fast-path int64 mantissa fits in Datum.Int
	default:
		return 0
	}
}

// totalRows is the relation's FULL live-row count (goopg's ANALYZE walks every
// block and reservoir-samples, so the caller has measured it exactly). It is
// what turns the sample's distinct count into a table-wide estimate — see
// ndistinctEstimate. M0127-P5.6-e-iii.
func computeColumnStats(sample []Row, colIdx int, statsTarget int, totalRows int64, dsCtx *Context) catalog.ColumnStats {
	stats := catalog.ColumnStats{}
	if len(sample) == 0 {
		return stats
	}

	// Per-key counts plus a representative Datum per key (so we
	// can preserve type information for sorting and for
	// Datum.Format() rendering downstream).
	type bucket struct {
		val   Datum
		count int
	}
	freq := make(map[string]*bucket, len(sample))
	var nullCount, nonNull int
	var totalPayloadWidth int64

	// Correlation data: PG's compute_scalar_stats (analyze.c:2853-2890)
	// computes the Pearson correlation between physical row order and
	// logical (sorted) column order. We collect (value, original_position)
	// pairs here during the first pass so the correlation can be computed
	// after sorting by value. Non-orderable kinds skip this.
	type valuePosition struct {
		d   Datum
		pos int
	}
	var corrPairs []valuePosition

	for pos, row := range sample {
		if colIdx >= len(row) {
			// Defensive: mismatched schema shouldn't happen given
			// DecodeRow honours tbl.Columns, but stay sane.
			continue
		}
		d := row[colIdx]
		if d.IsNull() {
			nullCount++
			continue
		}
		nonNull++
		totalPayloadWidth += int64(datumVariablePayloadWidth(d))
		key := datumKey(d)
		if b, ok := freq[key]; ok {
			b.count++
		} else {
			freq[key] = &bucket{val: d, count: 1}
		}

		// Collect for correlation: track original position alongside value.
		corrPairs = append(corrPairs, valuePosition{d: d, pos: pos})
	}

	stats.NullFrac = float64(nullCount) / float64(len(sample))

	if nonNull > 0 {
		stats.AvgWidth = float64(totalPayloadWidth) / float64(nonNull)
	}

	// Number of sampled values seen more than once — upstream's `nmultiple`
	// (compute_scalar_stats, analyze.c). Everything else appeared exactly once
	// and is upstream's `f1`.
	nmultiple := 0
	for _, b := range freq {
		if b.count > 1 {
			nmultiple++
		}
	}
	ndAbs := ndistinctEstimate(len(freq), nmultiple, nonNull, stats.NullFrac, totalRows)
	stats.NDistinct = int64(ndAbs + 0.5)
	if totalRows > 0 {
		stats.NDistinctFrac = ndAbs / float64(totalRows)
		if stats.NDistinctFrac > 1 {
			stats.NDistinctFrac = 1
		}
	}

	if nonNull == 0 {
		return stats
	}

	// --- correlation (STATISTIC_KIND_CORRELATION) ---
	// Pearson correlation between physical row order (original sample
	// position) and logical column order (position after sorting by value).
	// PG's compute_scalar_stats (analyze.c:2853-2890): since both x and y
	// sets are {0,1,...,n-1}, sum(x)=sum(y)=n*(n-1)/2 and
	// sum(x^2)=sum(y^2)=n*(n-1)*(2n-1)/6, so the coefficient reduces to
	//   corr = (n * Σxy - Σx²) / (n * Σx² - Σx²)
	// where Σxy is the sum of original_position[i] * sorted_position[i].
	if len(corrPairs) > 1 && isOrderableKind(corrPairs[0].d.Kind) {
		sort.Slice(corrPairs, func(i, j int) bool {
			cmp, err := compareDatum(corrPairs[i].d, corrPairs[j].d, 0)
			if err != nil {
				return false
			}
			return cmp < 0
		})
		// Verify sort succeeded: if the first pair comparison failed, skip
		// the correlation (it stays 0).
		if _, err := compareDatum(corrPairs[0].d, corrPairs[1].d, 0); err == nil {
			var corrXYSum float64
			for sortedPos, vp := range corrPairs {
				corrXYSum += float64(vp.pos) * float64(sortedPos)
			}
			n := float64(len(corrPairs))
			corrXSum := (n - 1.0) * n / 2.0
			corrX2Sum := (n - 1.0) * n * (2.0*n - 1.0) / 6.0
			denom := n*corrX2Sum - corrXSum*corrXSum
			if denom != 0 {
				stats.Correlation = (n*corrXYSum - corrXSum*corrXSum) / denom
			}
		}
	}

	// Sort buckets by count desc — primary input to the MCV /
	// histogram split.
	buckets := make([]*bucket, 0, len(freq))
	for _, b := range freq {
		buckets = append(buckets, b)
	}
	sort.Slice(buckets, func(i, j int) bool {
		return buckets[i].count > buckets[j].count
	})

	// MCV split: a value qualifies when its sample frequency
	// exceeds avg_freq(remaining) * mcvFreqMargin. We walk the
	// sorted list greedily, growing the MCV slot until the
	// condition fails or we hit the statsTarget cap.
	mcvCap := statsTarget
	if mcvCap > len(buckets) {
		mcvCap = len(buckets)
	}
	mcvCount := 0
	for mcvCount < mcvCap {
		// Frequency of the next candidate vs the average of
		// what's left after admitting it.
		candidate := buckets[mcvCount]
		remaining := nonNull
		for k := 0; k <= mcvCount; k++ {
			remaining -= buckets[k].count
		}
		distinctRemaining := len(buckets) - (mcvCount + 1)
		if distinctRemaining <= 0 {
			// Only candidate left — admit if the column shows
			// any duplication at all (otherwise skip; a
			// single-row "MCV" carries no information).
			if candidate.count > 1 {
				mcvCount++
			}
			break
		}
		avgRemaining := float64(remaining) / float64(distinctRemaining)
		if avgRemaining <= 0 {
			break
		}
		freqCandidate := float64(candidate.count)
		if freqCandidate < mcvFreqMargin*avgRemaining {
			break
		}
		mcvCount++
	}

	if mcvCount > 0 {
		stats.MCV = make([]catalog.MCVEntry, mcvCount)
		for i := 0; i < mcvCount; i++ {
			stats.MCV[i] = catalog.MCVEntry{
				Value:     formatDatumDateStyle(buckets[i].val, dsCtx),
				Frequency: float64(buckets[i].count) / float64(len(sample)),
			}
		}
	}

	// Histogram boundaries from the non-MCV portion. Sortable
	// kinds only; non-orderable kinds (bytes, interval) leave
	// Histogram empty.
	nonMCV := buckets[mcvCount:]
	if len(nonMCV) < 2 {
		return stats
	}
	if !isOrderableKind(nonMCV[0].val.Kind) {
		return stats
	}
	// Expand the non-MCV buckets into a sorted slice of values
	// (each repeated by its sample count) so equi-depth
	// boundary picking is exact, not weighted-bucket-approximated.
	expanded := make([]Datum, 0, nonNull-(nonNull-len(nonMCV))) // upper bound
	for _, b := range nonMCV {
		for k := 0; k < b.count; k++ {
			expanded = append(expanded, b.val)
		}
	}
	if len(expanded) < 2 {
		return stats
	}
	sortErr := sortDatumsAscending(expanded)
	if sortErr != nil {
		// Defensive: compareDatum complained about a kind we
		// thought was orderable. Skip the histogram rather
		// than return a half-built one.
		return stats
	}

	bucketCount := statsTarget
	maxBuckets := len(nonMCV) - 1
	if bucketCount > maxBuckets {
		bucketCount = maxBuckets
	}
	if bucketCount < 1 {
		return stats
	}

	bounds := make([]string, bucketCount+1)
	last := len(expanded) - 1
	for i := 0; i <= bucketCount; i++ {
		idx := i * last / bucketCount
		bounds[i] = formatDatumDateStyle(expanded[idx], dsCtx)
	}
	// Drop adjacent duplicate boundaries; an equi-depth
	// histogram with flat regions still emits ascending
	// distinct boundaries upstream (see
	// `compute_scalar_stats`). The dedup keeps the contract
	// "boundaries are strictly ascending" predictable.
	dedup := bounds[:0]
	for i, v := range bounds {
		if i == 0 || v != bounds[i-1] {
			dedup = append(dedup, v)
		}
	}
	if len(dedup) >= 2 {
		stats.Histogram = dedup
	}
	return stats
}

// isOrderableKind reports whether compareDatum produces a stable
// total order for kind k. The histogram bucketer needs that;
// kinds without one (bytes, interval) have empty histograms.
func isOrderableKind(k DatumKind) bool {
	switch k {
	case KindInt, KindBool, KindString, KindTime, KindNumeric:
		return true
	}
	return false
}

// sortDatumsAscending sorts in place using compareDatum. Returns
// the first comparison error encountered; on success the slice
// is in ascending order.
func sortDatumsAscending(ds []Datum) error {
	var firstErr error
	sort.Slice(ds, func(i, j int) bool {
		if firstErr != nil {
			return false
		}
		cmp, err := compareDatum(ds[i], ds[j], 0)
		if err != nil {
			firstErr = err
			return false
		}
		return cmp < 0
	})
	return firstErr
}

// AnalyzeRelationSampled runs the executor-grade sampled analyzer for one
// relation without an executor Context: upstream-default stats target,
// wall-clock-seeded reservoir, full column stats (NDistinct/NullFrac/MCV/
// histogram/correlation). The autovacuum launcher calls this instead of the
// simplified commands/vacuum.Analyze so autoanalyze produces planner-grade
// statistics. pg_statistic heap persistence still requires a Context and is
// therefore skipped here; the catalog TableStats sidecar (which the planner
// consumes via internal/optimizer/relsize.go) IS updated by the caller.
func AnalyzeRelationSampled(pool *storage.Pool, mgr *transam.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
	return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget,
		rand.New(rand.NewSource(time.Now().UnixNano())), nil, nil)
}
