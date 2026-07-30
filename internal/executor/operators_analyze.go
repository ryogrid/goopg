package executor

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/lockmgr"
	"github.com/goopg/goopg/internal/multixact"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
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

func (o *analyzeOp) Schema() planner.Schema { return nil }

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
		rel := o.ctx.Catalog.RelFileNode(tbl)
		// ANALYZE takes the per-relation ShareUpdateExclusiveLock (analyze.c).
		// With SKIP_LOCKED the acquire is conditional and a contended relation is
		// skipped — with a WARNING only when the user named it explicitly;
		// partition children reached by expanding a partitioned table are skipped
		// silently. Without SKIP_LOCKED the acquire blocks, so ANALYZE waits
		// behind a conflicting holder such as LOCK ... IN SHARE MODE. M0118-0008.
		if o.stmt.SkipLocked {
			if !o.ctx.tryAcquireMaintenanceLock(rel, lockmgr.ShareUpdateExclusiveLock) {
				if at.explicit {
					o.ctx.AddWarning(fmt.Sprintf("skipping analyze of %q --- lock not available", tbl.Name))
				}
				continue
			}
		} else if err := o.ctx.acquireRelLockMaybeTransient(rel, lockmgr.ShareUpdateExclusiveLock); err != nil {
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
	}
	// Inheritance-tree statistics for partitioned parents read every leaf
	// partition under a blocking AccessShareLock (SKIP_LOCKED does not cover
	// this scan), so ANALYZE of a partitioned table waits on a child held in a
	// conflicting mode by another session. M0118-0008.
	for _, parent := range parents {
		analyzeInheritanceWait(o.ctx, parent)
	}
	return nil, EOF
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
	add = func(tbl *catalog.Table, explicit bool) {
		if tbl == nil || tbl.Virtual {
			return
		}
		if tbl.PartitionMethod != "" && im != nil {
			parents = append(parents, tbl)
			for _, child := range im.PartitionChildren(tbl.OID, nsOid) {
				add(child, false)
			}
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
	for _, name := range o.stmt.Targets {
		tbl, ok := cat.LookupTable(name)
		if !ok {
			return nil, nil, &ExecError{Code: "42P01", Pos: o.stmt.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		// Maintenance-privilege check (vacuum_is_permitted_to_vacuum, analyze
		// verb). A non-superuser session may only analyze a relation it owns (or
		// holds MAINTAIN on); otherwise the relation is skipped with a WARNING,
		// emitted with NO lock so an unprivileged ANALYZE skips immediately rather
		// than waiting behind a conflicting lock holder. M0118-0008
		// (vacuum-conflict).
		if !maintenancePermitted(o.ctx, tbl) {
			o.ctx.AddWarning(fmt.Sprintf("permission denied to analyze %q, skipping it", tbl.Name))
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
func persistStatsToPGStatistic(ctx *Context, tbl *catalog.Table, stats *catalog.TableStats) error {
	if ctx == nil || ctx.Pool == nil || tbl == nil || stats == nil {
		return nil
	}
	statRel := storage.RelFileNode{
		DBOid:  catalog.DefaultDBOid,
		RelOid: catalog.StatisticRelationId,
		Fork:   storage.MainFork,
	}
	cols := pgStatisticColumnsPG18()
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
		if _, err := writeHeapRowCanonical(ctx, statRel, cols, row); err != nil {
			return fmt.Errorf("pg_statistic col %q: %w", col.Name, err)
		}
	}
	return nil
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
func analyzeRelation(pool *storage.Pool, mgr *mvcc.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
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
func analyzeRelationWith(pool *storage.Pool, mgr *mvcc.Manager, cat catalog.Catalog, tbl *catalog.Table, target int, rng *rand.Rand, mxs *multixact.Store, dsCtx *Context) (*catalog.TableStats, error) {
	rel := cat.RelFileNode(tbl)

	tx, err := mgr.Begin(mvcc.IsolationReadCommitted)
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
			if !mvcc.TupleVisible(t.Header, snap, tx.XID, mxs) {
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
		stats.Columns[i] = computeColumnStats(reservoir, i, colTarget, dsCtx)
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

// computeColumnStats derives the per-column NDistinct / NullFrac
// / MCV / Histogram from the sample. Mirrors the bookkeeping in
// upstream's `compute_scalar_stats` while staying within the v0
// type set.
func computeColumnStats(sample []Row, colIdx int, statsTarget int, dsCtx *Context) catalog.ColumnStats {
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
	for _, row := range sample {
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
		key := datumKey(d)
		if b, ok := freq[key]; ok {
			b.count++
		} else {
			freq[key] = &bucket{val: d, count: 1}
		}
	}

	stats.NullFrac = float64(nullCount) / float64(len(sample))
	stats.NDistinct = int64(len(freq))
	// The distinct-to-rows RATIO, measured on the sample.
	//
	// Deliberately NOT scaled up to the table (no Haas-Stokes): the ratio is
	// what the parallel-aggregate split gate consumes, and its sampling bias
	// runs in the safe direction. For a low-cardinality column the sample
	// over-states the fraction (6 distinct in 30,000 sampled rows reads as
	// 2e-4 where the truth over 6M rows is 5e-7), which over-states the
	// gate's reduction ratio and therefore makes it MORE likely to refuse.
	// For a near-unique column every sampled value is distinct and the ratio
	// is 1.0, which is exactly right and is the case the gate exists to catch.
	if nonNull > 0 {
		stats.NDistinctFrac = float64(len(freq)) / float64(nonNull)
	}

	if nonNull == 0 {
		return stats
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
