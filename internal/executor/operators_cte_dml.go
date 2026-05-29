package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/planner"
	"github.com/goopg/goopg/internal/storage"
)

// cteDMLPrefixOp executes data-modifying CTEs (INSERT/UPDATE/DELETE/MERGE)
// before handing control to the outer query plan. Each DML plan's RETURNING
// rows are materialized into ctx.MaterializedCTEs so that MaterializedCTEScan
// operators in the outer query can read them.
type cteDMLPrefixOp struct {
	plan  *planner.CTEDMLPrefix
	ctx   *Context
	inner Operator // outer query operator
}

func newCTEDMLPrefixOp(p *planner.CTEDMLPrefix) *cteDMLPrefixOp {
	return &cteDMLPrefixOp{plan: p}
}

func (o *cteDMLPrefixOp) Schema() planner.Schema { return o.plan.Body.Output() }

func (o *cteDMLPrefixOp) Open(ctx *Context) error {
	o.ctx = ctx

	// Ensure MaterializedCTEs map exists.
	if ctx.MaterializedCTEs == nil {
		ctx.MaterializedCTEs = make(map[string][][]Datum)
	}

	// CTE snapshot isolation: save the statement-start snapshot and
	// initialise the write fence. The outer query will restore the
	// snapshot and skip any rows written by the DML CTEs so that
	// PostgreSQL CTE semantics hold (outer SELECT sees pre-CTE state).
	savedSnap := ctx.Snap
	ctx.CTEWriteFence = make(map[storage.ItemPointer]struct{})
	ctx.InDMLCTE = true

	// Execute each DML CTE in order, collecting RETURNING rows.
	for i, dml := range o.plan.DMls {
		op, err := Build(dml)
		if err != nil {
			ctx.InDMLCTE = false
			ctx.Snap = savedSnap
			return err
		}
		if err := op.Open(ctx); err != nil {
			ctx.InDMLCTE = false
			ctx.Snap = savedSnap
			return err
		}
		var rows [][]Datum
		for {
			slot, err := op.Next()
			if err == EOF {
				break
			}
			if err != nil {
				op.Close()
				ctx.InDMLCTE = false
				ctx.Snap = savedSnap
				return err
			}
			// Materialize the row so it survives after op.Close().
			r := slotRow(slot)
			owned := make([]Datum, len(r))
			copy(owned, r)
			rows = append(rows, owned)
		}
		op.Close()
		key := strings.ToLower(o.plan.Names[i])
		ctx.MaterializedCTEs[key] = rows
	}

	// Restore snapshot and clear InDMLCTE before running the outer query.
	// The outer SELECT uses the statement-start snapshot (pre-CTE state)
	// and the CTEWriteFence skips any rows written by the DML CTEs above.
	ctx.InDMLCTE = false
	ctx.Snap = savedSnap

	// Now build and open the outer query plan.
	inner, err := Build(o.plan.Body)
	if err != nil {
		return err
	}
	if err := inner.Open(ctx); err != nil {
		return err
	}
	o.inner = inner
	return nil
}

func (o *cteDMLPrefixOp) Close() error {
	if o.inner != nil {
		return o.inner.Close()
	}
	return nil
}

func (o *cteDMLPrefixOp) Next() (TupleSlot, error) {
	return o.inner.Next()
}

// materializedCTEScanOp reads rows from ctx.MaterializedCTEs[name].
// Used when the outer SELECT references a data-modifying CTE by name.
type materializedCTEScanOp struct {
	plan *planner.MaterializedCTEScan
	rows [][]Datum
	idx  int
}

func newMaterializedCTEScanOp(p *planner.MaterializedCTEScan) *materializedCTEScanOp {
	return &materializedCTEScanOp{plan: p}
}

// cteScanOp executes a regular (SELECT) CTE with materialization semantics:
// the first time this CTE name is scanned within a query, all rows are
// buffered into ctx.CTERowCache[name]; subsequent scans replay from the cache.
// This implements PostgreSQL's CTE optimization-fence for volatile CTEs
// (e.g. `WITH q AS (SELECT random() ...) SELECT * FROM q UNION SELECT * FROM q`
// where q must produce the same rows both times). M0097-0099.
type cteScanOp struct {
	plan  *planner.CTEScan
	child Operator
	rows  []Row
	idx   int
}

func newCteScanOp(p *planner.CTEScan) (*cteScanOp, error) {
	child, err := Build(p.Child)
	if err != nil {
		return nil, err
	}
	return &cteScanOp{plan: p, child: child}, nil
}

func (o *cteScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *cteScanOp) Open(ctx *Context) error {
	// WorkTableScan is the self-reference inside a recursive CTE body.
	// It must NOT be cached — each recursive iteration needs fresh rows
	// from ctx.WorkTableRows. Skip the cache entirely for these. M0097-0099.
	if _, isWorkTable := o.plan.Child.(*planner.WorkTableScan); isWorkTable {
		if err := o.child.Open(ctx); err != nil {
			return err
		}
		var rows []Row
		for {
			slot, err := o.child.Next()
			if err == EOF {
				break
			}
			if err != nil {
				o.child.Close()
				return err
			}
			r := slotRow(slot)
			owned := make(Row, len(r))
			copy(owned, r)
			rows = append(rows, owned)
		}
		o.child.Close()
		o.rows = rows
		o.idx = 0
		return nil
	}

	key := strings.ToLower(o.plan.Name)
	if ctx.CTERowCache != nil {
		if cached, ok := ctx.CTERowCache[key]; ok {
			// Replay from cache (second or later reference to this CTE).
			o.rows = cached
			o.idx = 0
			return nil
		}
	}
	// First reference: run the child plan and buffer all rows.
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	var rows []Row
	for {
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			o.child.Close()
			return err
		}
		// Clone the row so it survives after child is closed.
		r := slotRow(slot)
		owned := make(Row, len(r))
		copy(owned, r)
		rows = append(rows, owned)
	}
	o.child.Close()
	// Store in cache so subsequent scans can replay.
	if ctx.CTERowCache == nil {
		ctx.CTERowCache = make(map[string][]Row)
	}
	ctx.CTERowCache[key] = rows
	o.rows = rows
	o.idx = 0
	return nil
}

func (o *cteScanOp) Close() error { return nil }

func (o *cteScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(o.plan.Output(), row), nil
}

func (o *materializedCTEScanOp) Schema() planner.Schema { return o.plan.Output() }

func (o *materializedCTEScanOp) Open(ctx *Context) error {
	key := strings.ToLower(o.plan.Name)
	if ctx.MaterializedCTEs != nil {
		o.rows = ctx.MaterializedCTEs[key]
	}
	o.idx = 0
	return nil
}

func (o *materializedCTEScanOp) Close() error { return nil }

func (o *materializedCTEScanOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(o.plan.Output(), row), nil
}
