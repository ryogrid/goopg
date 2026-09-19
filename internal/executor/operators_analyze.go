package executor

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/nodes"
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
//   - RowCount: extrapolated from the sampled blocks, matching upstream's
//     reltuples definition exactly (analyze.c:1330-1339's
//     `floor((liverows/bs.m)*totalblocks+0.5)`) — a random variable across
//     runs whenever the relation has more blocks than the sample cap, same
//     as PG's. Degrades to an exact count for a relation small enough that
//     every block is sampled. M0138-0002.
//   - Pages: raw block count.
//   - AvgWidth: total decoded-row bytes / live rows seen in the sample.
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
		// The shared stats entry gets the same report (pgstat_report_analyze):
		// mod_since_analyze resets to zero.
		relStats.reportAnalyze(tbl.OID)
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
		relStats.reportAnalyze(parent.OID)
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
	//
	// B-02 bounded-width interim: an oversized row is truncated (bound widths
	// capped, then bounds thinned evenly with endpoints kept, then the MCV
	// tail dropped — scalar fields never touched; see truncateColumnStatsToFit)
	// and the TRUNCATED row is what persists, instead of dropping the column
	// entirely. A row that fits is written byte-identical (no cap engaged).
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
		if n, serr := pgStatisticRowTupleLen(cols, row); serr == nil && n > storage.MaxHeapTupleSize {
			row, cs = truncateColumnStatsToFit(tbl.OID, attNum, cols, cs)
		}
		if _, err := writeHeapRowCanonical(ctx, statRel, cols, row); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("pg_statistic col %q: %w", col.Name, err)
		}
	}
	if err := persistRelSize(ctx, tbl, stats.RowCount, stats.Pages); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}

// pgStatisticMaxBoundBytes caps one histogram/MCV bound's width in the B-02
// bounded-width interim. Upstream pg_statistic has a toast relation, so wide
// histograms persist whole; goopg's catalog heap writer does not TOAST, so a
// per-column row wider than MaxHeapTupleSize (8160 B) was dropped entirely
// (orders/customer/partsupp comment columns). Truncation is prefix-faithful
// (UTF-8 boundary aware) and thinning keeps the endpoints, so range
// selectivity degrades gracefully; scalar fields (nullfrac/distinct/width,
// correlation) are never touched. A row that fits is returned byte-identical.
// Remove when pg_statistic gains TOAST/out-of-line storage (ledger M0125-0029).
const pgStatisticMaxBoundBytes = 64

// truncateUTF8Prefix cuts s to at most maxBytes without splitting a UTF-8
// encoding (backs off to the last rune boundary at or under the limit).
func truncateUTF8Prefix(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	t := s[:maxBytes]
	for len(t) > 0 && !utf8.ValidString(t) {
		t = t[:len(t)-1]
	}
	return t
}

// thinStatisticBounds keeps an evenly spaced subset of at most maxBounds
// entries, always including the first and last bound so the histogram still
// spans the column's full range. The input must be ascending (as the ANALYZE
// bucketer emits); a subset of an ascending slice stays ascending.
func thinStatisticBounds(bounds []string, maxBounds int) []string {
	if maxBounds < 2 {
		maxBounds = 2
	}
	if len(bounds) <= maxBounds {
		return bounds
	}
	out := make([]string, maxBounds)
	last := len(bounds) - 1
	for i := range out {
		out[i] = bounds[i*last/(maxBounds-1)]
	}
	return out
}

// pgStatisticRowTupleLen is the exact on-page tuple length of a
// pg_statistic row: EncodeRowPG + NullBitmapPG + heap header/hoff, the same
// construction buildCatalogPGHeapTuple uses (natts/infomask bits carry no
// length). Compare against storage.MaxHeapTupleSize: anything at or under it
// is accepted by PageAddHeapTuple on a fresh page.
func pgStatisticRowTupleLen(cols []catalog.Column, row Row) (int, error) {
	body, err := EncodeRowPG(cols, row)
	if err != nil {
		return 0, err
	}
	bitmap := NullBitmapPG(row)
	var tup storage.HeapTuple
	if len(bitmap) > 0 {
		tup = storage.NewHeapTupleWithNulls(1, storage.InvalidTransactionID, bitmap, body)
	} else {
		tup = storage.NewHeapTuple(1, storage.InvalidTransactionID, body)
	}
	raw, err := tup.MarshalBinary()
	if err != nil {
		return 0, err
	}
	return len(raw), nil
}

// pgStatisticRowIfFits builds the row for cs and reports whether its tuple
// fits on a heap page. Encode failures report not-fits; the caller then falls
// through to the scalar-only last resort.
func pgStatisticRowIfFits(tableOID uint32, attNum int16, cols []catalog.Column, cs catalog.ColumnStats) (Row, bool) {
	row := buildUserPGStatisticRow(tableOID, attNum, cs)
	n, err := pgStatisticRowTupleLen(cols, row)
	if err != nil {
		return row, false
	}
	return row, n <= storage.MaxHeapTupleSize
}

// truncateColumnStatsToFit shrinks cs until its pg_statistic row fits on a
// heap page, cheapest fidelity loss first: per-bound width cap, then evenly
// spaced histogram thinning (endpoints kept), then the MCV tail (least
// frequent entries first — MCV is frequency-ordered), then the histogram
// outright. The scalar-only last resort (a few hundred bytes) always fits. A
// cs that already fits is returned unchanged with its byte-identical row.
func truncateColumnStatsToFit(tableOID uint32, attNum int16, cols []catalog.Column, cs catalog.ColumnStats) (Row, catalog.ColumnStats) {
	if row, ok := pgStatisticRowIfFits(tableOID, attNum, cols, cs); ok {
		return row, cs
	}
	trunc := cs
	trunc.Histogram = make([]string, len(cs.Histogram))
	for i, b := range cs.Histogram {
		trunc.Histogram[i] = truncateUTF8Prefix(b, pgStatisticMaxBoundBytes)
	}
	trunc.MCV = make([]catalog.MCVEntry, len(cs.MCV))
	for i, e := range cs.MCV {
		trunc.MCV[i] = catalog.MCVEntry{Value: truncateUTF8Prefix(e.Value, pgStatisticMaxBoundBytes), Frequency: e.Frequency}
	}
	if row, ok := pgStatisticRowIfFits(tableOID, attNum, cols, trunc); ok {
		return row, trunc
	}
	for len(trunc.Histogram) > 2 {
		trunc.Histogram = thinStatisticBounds(trunc.Histogram, (len(trunc.Histogram)+1)/2)
		if row, ok := pgStatisticRowIfFits(tableOID, attNum, cols, trunc); ok {
			return row, trunc
		}
	}
	for len(trunc.MCV) > 0 {
		trunc.MCV = trunc.MCV[:len(trunc.MCV)/2]
		if row, ok := pgStatisticRowIfFits(tableOID, attNum, cols, trunc); ok {
			return row, trunc
		}
	}
	trunc.Histogram = nil
	trunc.MCV = nil
	return buildUserPGStatisticRow(tableOID, attNum, trunc), trunc
}

// persistRelSize writes one relation's ANALYZE/VACUUM-measured size to the
// goopg-private sidecar heap (catalog.GoopgRelStatsRelationId).
//
// take2 P1-03: extracted from persistStatsToPGStatistic so VACUUM can call it
// too. VACUUM measures reltuples and relpages — vacuum.Analyze returns both and
// catalog.UpdateRelStats stored them — but the update was IN MEMORY ONLY, so an
// autovacuum-maintained cluster planned from stale sizes after every restart
// and only an explicit SQL ANALYZE ever reached disk. Upstream has no such
// split: vac_update_relstats writes pg_class for both paths.
func persistRelSize(ctx *Context, tbl *catalog.Table, rowCount int64, pages int) error {
	if ctx == nil || ctx.Pool == nil || tbl == nil {
		return nil
	}
	relStatsRel := storage.RelFileNode{
		DBOid:  tableCatalogHeapDBOid(ctx),
		RelOid: catalog.GoopgRelStatsRelationId,
		Fork:   storage.MainFork,
	}
	sizeRow := Row{
		NewIntDatum(int64(tbl.OID)), // starelid
		NewIntDatum(rowCount),       // rowcount (reltuples)
		NewIntDatum(int64(pages)),   // pages (relpages)
	}
	if _, err := writeHeapRowCanonical(ctx, relStatsRel, GoopgRelStatsColumns(), sizeRow); err != nil {
		return fmt.Errorf("goopg_relstats %q: %w", tbl.Name, err)
	}
	return nil
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

// analyzeMCVList is upstream's `analyze_mcv_list`
// (postgres/src/backend/commands/analyze.c:2980), which decides how many of the
// most-common values are worth keeping.
//
// take2 P1-08. goopg used a `mcvFreqMargin = 1.25` greedy forward walk, under a
// comment claiming it was "upstream's MCV_THRESHOLD margin from
// compute_scalar_stats". That comment was FALSE for PG 18.3: there is no 1.25
// anywhere in analyze.c. Upstream replaced the ratio test with the
// hypergeometric significance test below, and the difference is not cosmetic —
// the ratio rule over-admits on near-uniform columns, and every admitted MCV
// entry displaces a histogram bound, so a column with no genuinely common
// values spends its whole stats budget describing noise.
//
// The direction of the walk is load-bearing and upstream says why: values are
// REMOVED from the full list rather than added to an empty one, because
// "the latter approach can fail to add any values if all the most common values
// have around the same frequency and make up the majority of the table" — which
// is exactly the shape goopg's forward walk mishandles.
//
// mcvCounts must be sorted by count descending. Returns the number of leading
// entries to keep.
func analyzeMCVList(mcvCounts []int, numMCV int, staDistinct, staNullFrac float64, sampleRows int, totalRows float64) int {
	// "If the entire table was sampled, keep the whole list. This also
	// protects us against division by zero in the code below."
	if float64(sampleRows) == totalRows || totalRows <= 1.0 {
		return numMCV
	}

	// Re-extract the estimated number of distinct nonnull values in the table.
	ndistinctTable := staDistinct
	if ndistinctTable < 0 {
		ndistinctTable = -ndistinctTable * totalRows
	}

	// sumcount tracks the total count of all but the last (least common) value.
	sumcount := 0.0
	for i := 0; i < numMCV-1; i++ {
		sumcount += float64(mcvCounts[i])
	}

	for numMCV > 0 {
		// Selectivity the least common value would have if it were NOT in the
		// list (c.f. eqsel).
		selec := 1.0 - sumcount/float64(sampleRows) - staNullFrac
		if selec < 0.0 {
			selec = 0.0
		}
		if selec > 1.0 {
			selec = 1.0
		}
		otherdistinct := ndistinctTable - float64(numMCV-1)
		if otherdistinct > 1 {
			selec /= otherdistinct
		}

		// Lower end of a continuity-corrected Wald-type interval on a
		// hypergeometric distribution: keep the value only when its observed
		// count is significantly above what non-MCV membership would predict.
		N := totalRows
		n := float64(sampleRows)
		K := N * float64(mcvCounts[numMCV-1]) / n
		variance := n * K * (N - K) * (N - n) / (N * N * (N - 1))
		stddev := math.Sqrt(variance)

		if float64(mcvCounts[numMCV-1]) > selec*float64(sampleRows)+2*stddev+0.5 {
			// Significantly more common: keep it and everything above it.
			break
		}
		numMCV--
		if numMCV == 0 {
			break
		}
		sumcount -= float64(mcvCounts[numMCV-1])
	}
	return numMCV
}

// analyzeSampleTID is one reservoir entry's physical location, upstream's
// HeapTuple->t_self as read by `compare_rows` (analyze.c:1361).
type analyzeSampleTID struct {
	block  uint32
	offset uint16
}

// analyzeSampleByTID sorts the reservoir into physical order, carrying the
// rows and their locations together. It is `compare_rows`: block first, then
// offset. ANALYZE's correlation statistic is the Pearson correlation between
// physical row order and sorted-value order, so `computeColumnStats` can only
// read a row's index as its physical position once this has run.
type analyzeSampleByTID struct {
	rows []Row
	tids []analyzeSampleTID
}

func (a *analyzeSampleByTID) Len() int { return len(a.rows) }

func (a *analyzeSampleByTID) Less(i, j int) bool {
	if a.tids[i].block != a.tids[j].block {
		return a.tids[i].block < a.tids[j].block
	}
	return a.tids[i].offset < a.tids[j].offset
}

func (a *analyzeSampleByTID) Swap(i, j int) {
	a.rows[i], a.rows[j] = a.rows[j], a.rows[i]
	a.tids[i], a.tids[j] = a.tids[j], a.tids[i]
}

// analyzeSeedEnv is the process-wide fallback seed for ANALYZE's reservoir
// sampler, read once from `GOOPG_ANALYZE_SEED`. Zero (the unset case) keeps
// upstream behaviour: every ANALYZE draws a fresh wall-clock-seeded sample,
// exactly as PG's acquire_sample_rows does
// (postgres/src/backend/commands/analyze.c — `random()` seeded per backend).
//
// Why it exists: a *measurement* problem, not a semantics one. goopg's
// statistics are per-connection, so the plan-capture harness
// (`cmd/estimate-audit -warm-stats`, on by default) re-ANALYZEs every table at
// the start of each capture session. With a wall-clock seed each capture sees
// a different sample, so two captures of the SAME binary disagree — measured
// 2026-09-05 at 455 differing estimate lines and 27 differing plan-shape lines
// between two back-to-back A/A captures, including whole join-method flips
// (TPC-H Q3 Nested Loop vs Merge Join, Q9 hash vs merge spine). That noise is
// larger than the signal every planner A/B in
// `docs/design/not_ralph/minimize_datum/TODO_ALL.md` is trying to read, and it
// makes the plan-shape pin (A-05) report changes no commit caused.
//
// Setting the variable pins the sample so a capture is reproducible. It is a
// harness knob: production and every unset run are bit-for-bit unchanged.
var analyzeSeedEnv = func() int64 {
	v := strings.TrimSpace(os.Getenv("GOOPG_ANALYZE_SEED"))
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}()

// analyzeSeedFor returns the sampler seed for one relation when no
// Context-level seed was set: the pinned harness seed when
// `GOOPG_ANALYZE_SEED` is set, otherwise a fresh wall-clock draw (upstream
// behaviour).
//
// The pinned seed is mixed with the relation OID. One seed shared by every
// relation would make each table's reservoir replay the identical random
// stream, correlating which sample POSITIONS survive across all tables — the
// pinned statistics would then be systematically less representative than an
// unpinned draw, so the gate would pin a plan set production would not
// necessarily pick. Mixing keeps a run fully reproducible (the OID is stable
// for a given cluster) while leaving the per-table samples independent.
//
// An explicit `GOOPG_ANALYZE_SEED=0` means "unset" and keeps the wall clock:
// zero is the sentinel, so there is no way to request the all-zero seed. A
// mistyped or overflowing value is likewise indistinguishable from unset (see
// analyzeSeedEnv) — fail-open, because the alternative pins every sample to a
// single draw that nobody asked for.
func analyzeSeedFor(tbl *catalog.Table) int64 {
	if analyzeSeedEnv == 0 {
		return time.Now().UnixNano()
	}
	if tbl == nil {
		return analyzeSeedEnv
	}
	return analyzeSeedEnv ^ int64(tbl.OID)
}

// analyzeRelationCtx is the Context-aware entry point that
// honours StatsTarget / AnalyzeRandSeed.
func analyzeRelationCtx(ctx *Context, tbl *catalog.Table) (*catalog.TableStats, error) {
	target := ctx.StatsTarget
	if target <= 0 {
		target = upstreamDefaultStatsTarget
	}
	seed := ctx.AnalyzeRandSeed
	if seed == 0 {
		seed = analyzeSeedFor(tbl)
	}
	return analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, target, rand.New(rand.NewSource(seed)), ctx.MultiXact, ctx)
}

// analyzeRelation is kept as a thin wrapper for tests that don't
// thread a Context — it uses the upstream-default stats target
// and a wall-clock-seeded sampler (pinned when `GOOPG_ANALYZE_SEED` is set).
func analyzeRelation(pool *storage.Pool, mgr *transam.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
	// nil store: analyzeRelation is the test-only convenience wrapper with no
	// executor.Context (hence no MultiXact) in scope. M0118-0003.
	return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(analyzeSeedFor(tbl))), nil, nil)
}

// analyzeRelationWith runs PG's two-stage acquire_sample_rows
// (analyze.c:1199, ported in analyze_block_sampler.go): stage one samples
// up to `targrows = target * upstreamSampleMultiplier` BLOCKS at random
// (blockSampler, Knuth Algorithm S), stage two reservoir-samples ROWS
// within only those blocks (reservoirState, Vitter Algorithm Z), and
// computes per-table + per-column statistics from the sample. RowCount is
// extrapolated from the sampled blocks (see the extrapolation comment at
// its assignment); Pages remains exact (a raw block count, not sampled).
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
	// R30: the physical (block, offset) location of each reservoir entry.
	// Algorithm R replaces a UNIFORMLY CHOSEN slot, so once the reservoir is
	// full its index order no longer tracks physical order -- and the index
	// is exactly what `computeColumnStats` uses as the `pos` of its
	// correlation pairs. Upstream has the same problem and fixes it by
	// re-sorting the sample into ItemPointer order before computing stats
	// (analyze.c:1312-1322, `compare_rows`); goopg never did, so correlation
	// collapsed toward 0 for every relation bigger than the sample cap.
	reservoirTID := make([]analyzeSampleTID, 0, sampleCap)

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
	// PG: liverows == samplerows. acquire_sample_rows keeps these as two
	// counters because heapam_scan_analyze_next_tuple can also report
	// deadrows (skipped without reaching the reservoir logic at all); goopg
	// has no dead-row bookkeeping yet (ledger row
	// m0138-0002-deadrows-not-tracked — nothing downstream consumes a dead
	// count), so one counter serves both roles here.
	var liverows float64
	var rowstoskip float64 = -1

	// M0138-0002: PG's two-stage acquire_sample_rows (analyze.c:1199) — stage
	// one selects up to sampleCap BLOCKS at random (blockSampler, Knuth
	// Algorithm S), stage two reservoir-samples ROWS within only those
	// blocks (reservoirState, Vitter Algorithm Z) — replacing the classic
	// Algorithm R over EVERY block that ran here before this task. `rng`
	// (analyzeSeedFor, GOOPG_ANALYZE_SEED-pinnable) is consulted exactly
	// twice, standing in for PG's process-wide pg_global_prng_state that
	// seeds both sub-generators (analyze.c:1225,1232) — see
	// analyze_block_sampler.go's file comment for the full rationale.
	blkSampler := newBlockSampler(uint32(nBlocks), sampleCap, uint64(rng.Int63()))
	rstate := newReservoirState(sampleCap, uint64(rng.Int63()))

	for blkSampler.hasMore() {
		blk := storage.BlockNumber(blkSampler.next())
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
			totalBytes += int64(int(t.Header.Hoff) + len(t.Data))

			// review/260831 EO1-4: decode ONLY the tuples the reservoir keeps.
			// Every visible tuple used to be decoded into a fresh Row — a full
			// per-column decode plus an allocation — and then dropped by the
			// sampling test below, so a 10M-row table paid 10M decodes to keep
			// a few thousand rows.
			//
			// PG's Vitter Algorithm Z (analyze.c:1276-1301, mirrored exactly):
			// the first sampleCap rows fill the reservoir outright ("if
			// numrows < targrows"). After that, `rowstoskip` — computed by
			// reservoirState.getNextS using `liverows` BEFORE this row is
			// counted, exactly as PG's samplerows argument — counts down rows
			// to pass over before the next replacement, which then picks its
			// victim slot via a SEPARATE draw on the same PRNG state, not
			// Algorithm R's per-row rng.Int63n(seen+1) draw this replaced.
			keep := -1
			if len(reservoir) < sampleCap {
				keep = len(reservoir)
				reservoir = append(reservoir, nil)
				reservoirTID = append(reservoirTID, analyzeSampleTID{})
			} else {
				if rowstoskip < 0 {
					rowstoskip = rstate.getNextS(liverows, sampleCap)
				}
				if rowstoskip <= 0 {
					keep = int(float64(sampleCap) * samplerRandomFract(&rstate.rand))
				}
				rowstoskip--
			}
			liverows++
			if keep < 0 {
				continue
			}
			// Decode the PG-physical tuple body using the header (natts +
			// null bitmap). Single on-disk row format since M0111-0002.
			row := make(Row, len(tbl.Columns))
			natts := int(t.Header.Infomask2 & 0x07FF)
			if derr := DecodeRowIntoMctxPGTuple(row, tbl.Columns, t.Data, t.Bitmap, natts, nil); derr != nil {
				pool.Unpin(slot)
				return nil, fmt.Errorf("ANALYZE %s slot=%d: %w", tbl.QualifiedName(), s, derr)
			}
			reservoir[keep] = row
			reservoirTID[keep] = analyzeSampleTID{block: uint32(blk), offset: s}
		}
		pool.Unpin(slot)
	}

	// R30 / PG analyze.c:1312-1322: restore physical order. Upstream sorts
	// only when the reservoir filled ("If we didn't find as many tuples as we
	// wanted then we're done. No sort is needed, since they're already in
	// order."); the same condition holds here, and keeping it means a
	// short relation takes exactly the path it took before.
	if len(reservoir) == sampleCap {
		sort.Sort(&analyzeSampleByTID{rows: reservoir, tids: reservoirTID})
	}

	// PG analyze.c:1330-1339: totalrows is an EXTRAPOLATED ESTIMATE from the
	// sampled blocks, never an exact count once totalblocks > sampleCap — a
	// consequence of the owner's Q2 decision (AGENT.md "Plan-parity
	// harness": reproduce PG's estimates, errors included) that the
	// M0138-0001 census flagged as a scope question for this task
	// (ledger row m0138-0001-reltuples-sample-extrapolation). It degrades to
	// an exact count when blkSampler.m == nBlocks (every block sampled),
	// the same case PG's BlockSampler_Next degrades to a full scan.
	if blkSampler.m > 0 {
		stats.RowCount = int64(math.Floor((liverows/float64(blkSampler.m))*float64(nBlocks) + 0.5))
	}

	if liverows > 0 {
		stats.AvgWidth = float64(totalBytes) / liverows
	}

	for i := range tbl.Columns {
		colTarget, ok := columnStatsTarget(&tbl.Columns[i], target)
		if !ok {
			continue
		}
		stats.Columns[i] = computeColumnStats(reservoir, i, colTarget, stats.RowCount, tbl.Columns[i].Type, dsCtx)
		// Honor a per-column `n_distinct` attribute option, mirroring
		// upstream's override in compute_index_stats/do_analyze_rel
		// (postgres/src/backend/commands/analyze.c:571-581): a manual
		// n_distinct baked into the stored statistics at ANALYZE time,
		// so the planner consults it like any other stadistinct value.
		if nd, ok := columnNDistinctOverride(&tbl.Columns[i], stats.RowCount); ok {
			stats.Columns[i].NDistinct = nd
			// take2 P1-07: NDistinctFrac must be cleared too, or the override
			// is silently discarded. ColumnStats.StaDistinct() consults the
			// FRACTION first whenever it exceeds 0.1 (catalog.go:1884-1886),
			// and computeColumnStats above has just set it from the sample —
			// so on any column whose sampled distinct fraction exceeds 10 %,
			// which is most keys, the manual n_distinct was written into a
			// field nothing subsequently read.
			//
			// Clearing it here rather than teaching StaDistinct about
			// overrides keeps the precedence rule in ONE place: an explicit
			// n_distinct REPLACES what ANALYZE measured, which is upstream's
			// behaviour (analyze.c applies it to stadistinct itself, leaving
			// no second field to disagree).
			stats.Columns[i].NDistinctFrac = 0
		}
	}
	// B-05a: extended statistics (pg_statistic_ext_data _data write path).
	// reservoir/tbl/target/stats.RowCount are all in scope here — the same
	// inputs BuildRelationExtStatistics consumes (sample rows, relation,
	// computed target, totalrows). dsCtx carries the heap-write handles;
	// the test-only analyzeRelation wrapper passes nil and skips the write
	// (its contexts have no Pool). Best-effort: a _data write failure must
	// never fail ANALYZE itself, matching persistStatsToPGStatistic's
	// non-fatal contract at the analyzeOp.Next call site.
	if dsCtx != nil {
		_ = buildAndStoreExtStatistics(dsCtx, tbl, target, reservoir, stats.RowCount)
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
		return numericFastPathOnDiskWidth(d.Int, d.Scale)
	default:
		return 0
	}
}

// pgVarlenaShortMaxBody is PG's VARATT_SHORT_MAX (postgres/src/include/varatt.h:257)
// minus VARHDRSZ_SHORT (:255) — the largest varlena BODY (header excluded) that
// still fits a 1-byte "short" varlena header. numeric_size-equivalent width
// computation below wraps its NumericData body in this same header the way a
// small on-heap value actually is.
const pgVarlenaShortMaxBody = 0x7F - 1

// numericFastPathOnDiskWidth is M0138-0007's fix: PG's `compute_scalar_stats`
// measures `VARSIZE_ANY(DatumGetPointer(value))` (analyze.c:2008,2124,2471) —
// the FULL on-disk varlena size, header included, of the sample row's raw
// (not detoasted-and-repacked) attribute Datum. For a NUMERIC column that is
// PG's NumericData struct (`postgres/src/backend/utils/adt/numeric.c:130-146`,
// short 2-byte internal header when `NUMERIC_CAN_BE_SHORT` holds, else the
// 4-byte long form) wrapped in the varlena's own 1-byte ("short", body <= 126
// bytes) or 4-byte header — every fast-path value here is well under that
// bound, since it is bounded by int64.
//
// Oracle-verified rather than assumed: PG 18.3 on the TPC-H bench cluster
// (`:65432`) reports `pg_column_size(l_quantity)=5` for `18` (dscale 0,
// 1 digit: 1-byte varlena hdr + 2-byte short numeric hdr + 1 digit*2 bytes)
// and `pg_stats.avg_width=8` for `l_extendedprice` (dscale 2, 2-3 digits:
// 7 or 9 bytes depending on magnitude) — both match this formula exactly and
// refute the NUMERIC_HDRSZ-only (long-form, no short-varlena) guess this
// ticket's ledger row originally floated.
//
// Reuses `nodes.NumericBodyFromText` (the same port `codec.go` uses for the
// heap's on-disk numeric column form) rather than re-deriving the base-10000
// digit grouping here, so the two callers cannot drift on ndigits/weight
// rules.
func numericFastPathOnDiskWidth(mantissa int64, scale int16) int {
	body, err := nodes.NumericBodyFromText(formatNumeric(mantissa, scale))
	if err != nil {
		return 0
	}
	if len(body) <= pgVarlenaShortMaxBody {
		return 1 + len(body)
	}
	return 4 + len(body)
}

// totalRows is the relation's FULL live-row count (goopg's ANALYZE walks every
// block and reservoir-samples, so the caller has measured it exactly). It is
// what turns the sample's distinct count into a table-wide estimate — see
// ndistinctEstimate. M0127-P5.6-e-iii.
func computeColumnStats(sample []Row, colIdx int, statsTarget int, totalRows int64, colType catalog.Type, dsCtx *Context) catalog.ColumnStats {
	stats := catalog.ColumnStats{}
	if len(sample) == 0 {
		return stats
	}

	// M0138-0004 / M0138-0001 finding 3: PG's compute_scalar_stats never
	// measures a fixed-width (non-varlena) type's average width from the
	// data at all — `is_varwidth = !typbyval && typlen < 0` is false for
	// every by-value type AND every fixed-length by-reference type (uuid,
	// interval, macaddr, …), and in that case stawidth is simply the type's
	// typlen (analyze.c:2565-2569, plus the too-wide-only and all-null
	// branches at :2965 and :2975, which apply the same typlen literally).
	// goopg's per-row loop only ever measures a *variable* payload
	// (datumVariablePayloadWidth returns 0 for every fixed-width kind), so
	// without this fallback every int4/int8/date/… column reported
	// AvgWidth=0 — the census's finding 3 (32/61 TPC-H, 70/121 TPC-DS
	// columns). `colTypeDescriptor` is the same name->OID->pg_type.dat
	// bridge coltypeinfo.go already uses; TypLen<0 covers both varlena
	// (-1) and cstring (-2), matching PG's is_varwidth exactly.
	typLen := colTypeDescriptor(colType).TypLen
	fixedWidth := typLen >= 0
	if fixedWidth {
		stats.AvgWidth = float64(typLen)
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

	// Variable-width types only: fixed-width types already got their
	// typlen-derived AvgWidth above and PG never overwrites it with a
	// measured value (analyze.c's is_varwidth branch guards the
	// total_width/nonnull_cnt division the same way).
	if !fixedWidth && nonNull > 0 {
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
		// M0138-0004 / M0138-0001 finding 2: PG's compare_scalars breaks a
		// tie between equal-valued items by original scan position ---
		// "for equal datums, sort by tupno" (analyze.c compare_scalars,
		// `return ta - tb`) --- which is a deterministic total order, not
		// "whatever qsort happens to do with equal keys". `sort.Slice` is
		// documented non-stable, so two duplicate values could land in
		// either relative order run-to-run; `corrPairs` is already built
		// in ascending original-position order (the loop above appends in
		// sample scan order), so a STABLE sort reproduces PG's ascending
		// tupno tie-break exactly instead of leaving it undefined. Live
		// evidence this mattered: 12/23 TPC-DS `store_sales` columns
		// clustered inside an unrelated [0.12,0.15] correlation band under
		// the old unstable sort.
		sort.SliceStable(corrPairs, func(i, j int) bool {
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
	//
	// M0138-0004: PG builds its MCV `track` list by walking values in
	// ASCENDING sorted-by-value order and only replacing the current
	// tail-of-list occupant when a new group's count is STRICTLY greater
	// (analyze.c: `dups_cnt > track[track_cnt-1].count`); a count TIE at the
	// truncation boundary is a no-op, so whichever value's group was
	// encountered first --- i.e. the smaller value --- keeps the slot. A
	// plain count-only sort here leaves ties in whatever order `freq`'s map
	// iteration happened to produce, which is unspecified and can disagree
	// with PG (and with itself run-to-run). Tie-breaking by ascending value
	// reproduces PG's "earlier in the scan wins" rule for orderable kinds;
	// non-orderable kinds have no PG-defined order to match here (they take
	// compute_distinct_stats upstream, a different algorithm not ported),
	// so their tie order is left as before.
	buckets := make([]*bucket, 0, len(freq))
	for _, b := range freq {
		buckets = append(buckets, b)
	}
	orderable := len(buckets) > 0 && isOrderableKind(buckets[0].val.Kind)
	sort.Slice(buckets, func(i, j int) bool {
		if buckets[i].count != buckets[j].count {
			return buckets[i].count > buckets[j].count
		}
		if !orderable {
			return false
		}
		cmp, err := compareDatum(buckets[i].val, buckets[j].val, 0)
		if err != nil {
			return false
		}
		return cmp < 0
	})

	// MCV split — take2 P1-08, following compute_scalar_stats
	// (analyze.c:2670-2700) rather than the 1.25 ratio rule that used to live
	// here.
	//
	// Upstream keeps the WHOLE tracked list when it is complete and fits:
	// "if we can fit all the values seen so far in the MCV list, then we
	// should do so and thus provide the planner with complete information".
	// Otherwise the list is incomplete, and it is "generally worth being more
	// selective" — which is what analyzeMCVList decides.
	mcvCount := statsTarget
	if mcvCount > len(buckets) {
		mcvCount = len(buckets)
	}
	// `track_cnt == ndistinct && toowide_cnt == 0 && stadistinct > 0 &&
	// track_cnt <= num_mcv`: every distinct value was seen and they all fit,
	// so the list is complete and is kept whole.
	//
	// M0138-0004: upstream's `track[]` only ever holds MULTIPLY-occurring
	// values (`dups_cnt > 1` gates every insertion, analyze.c:2549-2552), so
	// `track_cnt == ndistinct` can only be true when literally every distinct
	// sample value repeated at least once — a single singleton disqualifies
	// the whole list from "complete" regardless of how few distinct values
	// there are. `len(buckets) <= statsTarget` alone (the previous condition
	// here) checked only the total distinct-value count, not that they were
	// all multi-occurring, so a column with e.g. 90 repeated values and 10
	// singletons under a target of 100 wrongly took the "complete" branch and
	// skipped analyzeMCVList's significance test entirely — keeping all 90
	// repeated values as MCV members where PG would have narrowed them.
	// `nmultiple == len(buckets)` is that "no singletons" condition
	// (`nmultiple` already counts exactly the multiply-occurring distinct
	// values, computed above for the ndistinct estimator).
	// `stats.StaDistinct() > 0` reproduces the `stadistinct > 0` guard: PG's
	// signed convention flips negative once the 10% row-count switch fires
	// (see catalog.ColumnStats.StaDistinct), which the plain `len(buckets)`
	// check never consulted at all.
	completeAndFits := nmultiple == len(buckets) && len(buckets) <= statsTarget && stats.StaDistinct() > 0
	if !completeAndFits {
		// M0138-0004: upstream's `track[]` array is sized `num_mcv =
		// attstattarget` and only ever gains an entry for a multiply-occurring
		// group (`dups_cnt > 1`), so by construction it can hold at most
		// `min(nmultiple, statsTarget)` entries — `analyze_mcv_list` never
		// sees a singly-occurring value as a candidate at all. Capping here by
		// `len(buckets)` (ndistinct) instead of `nmultiple` handed the
		// significance test a candidate list padded with singleton noise at
		// its tail, which is not what PG evaluates.
		if mcvCount > nmultiple {
			mcvCount = nmultiple
		}
		if mcvCount > 0 {
			counts := make([]int, mcvCount)
			for i := 0; i < mcvCount; i++ {
				counts[i] = buckets[i].count
			}
			// staDistinct here is the absolute distinct count this sample implies;
			// analyzeMCVList accepts PG's signed convention and this is the
			// positive form.
			mcvCount = analyzeMCVList(counts, mcvCount, float64(len(buckets)),
				stats.NullFrac, len(sample), float64(totalRows))
		}
	}
	// A single-occurrence "most common value" carries no information; upstream
	// reaches the same place via its `dups_cnt > 0` tracking, which never
	// enters a value seen once into the track list at all. Now a pure safety
	// net (both branches above already guarantee `buckets[:mcvCount]` is
	// entirely multi-occurring) rather than load-bearing, kept in case that
	// invariant ever regresses.
	for mcvCount > 0 && buckets[mcvCount-1].count <= 1 {
		mcvCount--
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
	// M0138-0004: upstream does NOT dedup adjacent equal boundaries.
	// compute_scalar_stats (analyze.c:2806-2836) copies exactly `num_hist`
	// evenly-spaced values out of the sorted non-MCV array with no
	// distinctness check at all, so a value that's common but didn't make
	// the MCV cut (analyze_mcv_list declined it as not "significant" enough)
	// can legitimately occupy several adjacent histogram slots. The
	// selectivity consumer already copes with that on both sides: PG's own
	// ineq_histogram_selectivity falls back to `binfrac = 0.5` whenever a
	// bin's two boundaries compare equal ("cope if bin boundaries appear
	// identical", selfuncs.c:1234-1237), and goopg's bucketFraction /
	// convertStringBucketScales (selectivity.go) already implement that same
	// 0.5 fallback. A prior version of this function deduped here, which
	// silently shrank the stored histogram (and therefore the exact bucket
	// count and boundary positions used by every selectivity computed
	// against it) relative to what PG would have stored for the identical
	// sample — a real divergence in "the same statistics", not a rendering
	// nicety.
	stats.Histogram = bounds
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
// wall-clock-seeded reservoir (pinned when `GOOPG_ANALYZE_SEED` is set),
// full column stats (NDistinct/NullFrac/MCV/histogram/correlation). The autovacuum launcher calls this instead of the
// simplified commands/vacuum.Analyze so autoanalyze produces planner-grade
// statistics. pg_statistic heap persistence still requires a Context and is
// therefore skipped here; the catalog TableStats sidecar (which the planner
// consumes via internal/optimizer/relsize.go) IS updated by the caller.
func AnalyzeRelationSampled(pool *storage.Pool, mgr *transam.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
	return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget,
		rand.New(rand.NewSource(analyzeSeedFor(tbl))), nil, nil)
}
