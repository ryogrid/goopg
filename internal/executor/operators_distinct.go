package executor

// operators_distinct.go — distinctOp implements SELECT DISTINCT row deduplication.
// M0097-0005.

import (
	"sort"

	"github.com/goopg/goopg/internal/optimizer"
)

// distinctOp filters duplicate rows from its child operator.
// It buffers all rows in memory and deduplicates using the same string-key
// approach as the recursive UNION dedup. M0097-0005.
type distinctOp struct {
	plan   *optimizer.Distinct
	child  Operator
	ctx    *Context
	rows   []Row
	idx    int
	schema optimizer.Schema
}

func newDistinctOp(p *optimizer.Distinct, child Operator) *distinctOp {
	return &distinctOp{plan: p, child: child, schema: p.Output()}
}

func (o *distinctOp) Schema() optimizer.Schema { return o.schema }

func (o *distinctOp) Open(ctx *Context) error {
	o.ctx = ctx
	// Reset accumulation state so re-Open re-drains from scratch.
	// Neither Open nor Close cleared these before Stage 9 (S2c): a
	// SubPlan handle re-running a DISTINCT body accumulated the
	// previous outer row's rows and kept a stale cursor (found by
	// matrix row M12). Same unconditionally-correct pattern as the
	// Stage-7 limitOp reset.
	o.rows = nil
	o.idx = 0
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	// Drain all rows and deduplicate.
	seen := make(map[string]struct{})
	rowN := 0
	for {
		// Cancellation: check ctx.Err() every 1024 rows so a
		// CancelRequest (or the client-EOF watcher) interrupts a
		// DISTINCT over a multi-million-row child. Same throttled
		// pattern as runNestedLoop (M0058-0005 family); this loop was
		// part of the csq-S6 spin incident's plan (`Unique` over a
		// 6 M-row lineitem scan).
		rowN++
		if rowN&0x3FF == 0 && ctx.Ctx != nil {
			if cerr := ctx.Ctx.Err(); cerr != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		if slot == nil {
			continue
		}
		row := slot.Row()
		// Clone the row so we own the data (child slot is reused).
		ownedRow := cloneRow(row)
		k := rowKey(ownedRow)
		if _, dup := seen[k]; dup {
			continue
		}
		seen[k] = struct{}{}
		o.rows = append(o.rows, ownedRow)
	}
	// Sort rows for deterministic output matching PostgreSQL's sort-based
	// DISTINCT: NULL values sort last, non-null values by datum order.
	sort.Slice(o.rows, func(i, j int) bool {
		ri, rj := o.rows[i], o.rows[j]
		for col := 0; col < len(ri) && col < len(rj); col++ {
			a, b := ri[col], rj[col]
			if a.IsNull() && b.IsNull() {
				continue
			}
			if a.IsNull() {
				return false // NULLs last
			}
			if b.IsNull() {
				return true
			}
			cmp, err := compareDatum(a, b, 0)
			if err != nil || cmp == 0 {
				continue
			}
			return cmp < 0
		}
		return len(ri) < len(rj)
	})
	return nil
}

func (o *distinctOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	if row == nil {
		// Return an empty slot for zero-column rows (SELECT DISTINCT FROM table).
		return SlotFromRow(o.schema, Row{}), nil
	}
	return SlotFromRow(o.schema, row), nil
}

func (o *distinctOp) Close() error { return o.child.Close() }

// distinctOnOp implements SELECT DISTINCT ON (key,...) by reading sorted input
// and emitting only the first row per distinct key combination.
// The child must be pre-sorted so rows with equal keys are contiguous.
type distinctOnOp struct {
	plan    *optimizer.DistinctOn
	child   Operator
	ctx     *Context
	schema  optimizer.Schema
	prevKey string
	started bool
}

func newDistinctOnOp(p *optimizer.DistinctOn, child Operator) *distinctOnOp {
	return &distinctOnOp{plan: p, child: child, schema: p.Output()}
}

func (o *distinctOnOp) Schema() optimizer.Schema { return o.schema }

func (o *distinctOnOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.started = false
	o.prevKey = ""
	return o.child.Open(ctx)
}

func (o *distinctOnOp) Next() (TupleSlot, error) {
	keyCols := o.plan.KeyCols
	for {
		slot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		if slot == nil {
			continue
		}
		row := slot.Row()
		// Build a key from the DISTINCT ON columns.
		var key string
		for _, idx := range keyCols {
			if idx >= 0 && idx < len(row) {
				key += datumKey(row[idx]) + "\x00"
			}
		}
		if !o.started || key != o.prevKey {
			o.started = true
			o.prevKey = key
			return SlotFromRow(o.schema, cloneRow(row)), nil
		}
		// Duplicate key: skip this row.
	}
}

func (o *distinctOnOp) Close() error { return o.child.Close() }


