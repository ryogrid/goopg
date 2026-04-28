package executor

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/mvcc"
	"github.com/goopg/goopg/internal/parser"
	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// analyzeOp drives `ANALYZE [target [, …]]` against the
// storage layer. For each target relation it walks every
// block, decodes visible heap tuples, and accumulates
// per-table and per-column statistics that the catalog stores
// for the planner to consult later.
//
// v0 collects:
//
//   - RowCount: visible-tuple count under a fresh
//     ReadCommitted snapshot (matches upstream's reltuples
//     definition).
//   - Pages: raw block count.
//   - AvgWidth: total decoded-row bytes / RowCount.
//   - Per-column NDistinct and NullFrac.
//
// MCV lists, histograms, and sampling are intentionally out
// of scope for v0 — see
// docs/design/0003-0010-analyze-statistics.md.
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

func (o *analyzeOp) Next() (Row, error) {
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
		stats, err := analyzeRelation(o.ctx.Pool, o.ctx.TxnMgr, o.ctx.Catalog, tbl)
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

// analyzeRelation walks every block of tbl under a fresh
// snapshot, decodes visible tuples via the executor codec,
// and computes per-table + per-column statistics.
func analyzeRelation(pool *storage.Pool, mgr *mvcc.Manager, cat catalog.Catalog, tbl *catalog.Table) (*catalog.TableStats, error) {
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

	stats := &catalog.TableStats{
		Pages:   int(nBlocks),
		Columns: make([]catalog.ColumnStats, len(tbl.Columns)),
	}
	colDistinct := make([]map[string]struct{}, len(tbl.Columns))
	for i := range colDistinct {
		colDistinct[i] = map[string]struct{}{}
	}
	colNulls := make([]int64, len(tbl.Columns))
	var totalBytes int64

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
				if errors.Is(perr, storage.ErrUnsupportedItem) {
					continue
				}
				pool.Unpin(slot)
				return nil, perr
			}
			if !mvcc.TupleVisible(t.Header, snap, tx.XID) {
				continue
			}
			row, derr := DecodeRow(tbl.Columns, t.Data)
			if derr != nil {
				pool.Unpin(slot)
				return nil, fmt.Errorf("ANALYZE %s slot=%d: %w", tbl.QualifiedName(), s, derr)
			}
			stats.RowCount++
			totalBytes += int64(int(t.Header.Hoff) + len(t.Data))
			for i, d := range row {
				if i >= len(colDistinct) {
					break
				}
				if d.IsNull() {
					colNulls[i]++
					continue
				}
				colDistinct[i][datumKey(d)] = struct{}{}
			}
		}
		pool.Unpin(slot)
	}

	if stats.RowCount > 0 {
		stats.AvgWidth = float64(totalBytes) / float64(stats.RowCount)
	}
	for i := range tbl.Columns {
		stats.Columns[i] = catalog.ColumnStats{
			NDistinct: int64(len(colDistinct[i])),
		}
		if stats.RowCount > 0 {
			stats.Columns[i].NullFrac = float64(colNulls[i]) / float64(stats.RowCount)
		}
	}
	return stats, nil
}
