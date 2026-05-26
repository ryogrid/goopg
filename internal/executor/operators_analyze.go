package executor

import (
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"time"

	"github.com/goopg/goopg/internal/catalog"
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
	for _, name := range o.targets() {
		tbl, ok := o.ctx.Catalog.LookupTable(name)
		if !ok {
			return nil, &ExecError{Code: "42P01", Pos: o.stmt.Pos(), Message: fmt.Sprintf("relation %q does not exist", name.String())}
		}
		if tbl.Virtual {
			continue
		}
		stats, err := analyzeRelationCtx(o.ctx, tbl)
		if err != nil {
			return nil, &ExecError{Code: "XX000", Pos: o.stmt.Pos(), Message: err.Error()}
		}
		o.ctx.Catalog.SetTableStats(tbl, stats)
	}
	return nil, EOF
}

func (o *analyzeOp) Close() error { return nil }

// targets returns the list of relations to analyze. Empty
// AnalyzeStmt.Targets means "every user table" — matches
// upstream.
func (o *analyzeOp) targets() []parser.ObjectName {
	if len(o.stmt.Targets) > 0 {
		return o.stmt.Targets
	}
	// Iterate the catalog in some stable order. v0's InMemory
	// catalog doesn't expose a public iterator, so we don't
	// support the catalog-wide form yet.
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
	return analyzeRelationWith(ctx.Pool, ctx.TxnMgr, ctx.Catalog, tbl, target, rand.New(rand.NewSource(seed)))
}

// analyzeRelation is kept as a thin wrapper for tests that don't
// thread a Context — it uses the upstream-default stats target
// and a wall-clock-seeded sampler.
func analyzeRelation(pool *storage.Pool, mgr *mvcc.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
	return analyzeRelationWith(pool, mgr, cat, tbl, upstreamDefaultStatsTarget, rand.New(rand.NewSource(time.Now().UnixNano())))
}

// analyzeRelationWith walks every block of tbl under a fresh
// snapshot, decodes visible tuples via the executor codec,
// reservoir-samples them with `targrows = target *
// upstreamSampleMultiplier`, and computes per-table + per-column
// statistics from the sample (RowCount and Pages remain exact).
func analyzeRelationWith(pool *storage.Pool, mgr *mvcc.Manager, cat catalog.Catalog, tbl *catalog.Table, target int, rng *rand.Rand) (*catalog.TableStats, error) {
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
			if !mvcc.TupleVisible(t.Header, snap, tx.XID) {
				continue
			}
			// Decode from the tuple header rather than letting DecodeRow
			// guess the format. A goopg legacy-format tuple (EncodeRow path,
			// written when canonical WAL logging is off) has no null bitmap
			// and natts==0; its [flag][big-endian value] body can be
			// mis-parsed by DecodeRow's PG-first heuristic — e.g. an int4
			// whose big-endian low byte (0x0f=15, 0x1c=28, …) forms a valid
			// PG varlena header that consumes exactly to end-of-tuple,
			// silently yielding a wrong value (id 15/28 → 0). PG-physical
			// tuples set natts (and may carry a null bitmap), so the header
			// disambiguates the two formats. M0097-analyze-decode.
			row := make(Row, len(tbl.Columns))
			natts := int(t.Header.Infomask2 & 0x07FF)
			var derr error
			if len(t.Bitmap) == 0 && natts == 0 {
				derr = decodeGoopgRowIntoMctx(row, tbl.Columns, t.Data, nil)
			} else {
				derr = DecodeRowIntoMctxPGTuple(row, tbl.Columns, t.Data, t.Bitmap, natts, nil)
			}
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
		stats.Columns[i] = computeColumnStats(reservoir, i, target)
	}
	return stats, nil
}

// computeColumnStats derives the per-column NDistinct / NullFrac
// / MCV / Histogram from the sample. Mirrors the bookkeeping in
// upstream's `compute_scalar_stats` while staying within the v0
// type set.
func computeColumnStats(sample []Row, colIdx int, statsTarget int) catalog.ColumnStats {
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
				Value:     buckets[i].val.Format(),
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
		bounds[i] = expanded[idx].Format()
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
