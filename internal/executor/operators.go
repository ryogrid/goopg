package executor

import (
	"sort"

	"github.com/goopg/goopg/internal/planner"
)

// valuesOp emits a fixed sequence of rows produced from literal
// expressions. SELECT 1 plans into a Project over a one-row Values
// with an empty input row.
type valuesOp struct {
	rows   [][]planner.Expr
	idx    int
	ctx    *Context
	schema planner.Schema
}

func newValuesOp(plan *planner.Values) *valuesOp {
	return &valuesOp{rows: plan.Rows, schema: plan.Output()}
}

func (o *valuesOp) Open(ctx *Context) error { o.ctx = ctx; o.idx = 0; return nil }
func (o *valuesOp) Schema() planner.Schema  { return o.schema }
func (o *valuesOp) Close() error            { return nil }

func (o *valuesOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	exprs := o.rows[o.idx]
	o.idx++
	row := make(Row, len(exprs))
	for i, e := range exprs {
		v, err := evalExpr(e, nil, o.ctx)
		if err != nil {
			return nil, err
		}
		row[i] = v
	}
	return row, nil
}

// projectOp evaluates the target list against each child row.
type projectOp struct {
	child   Operator
	targets []planner.Expr
	schema  planner.Schema
	ctx     *Context
	// M0054-0005a-followup: borrow-semantics output buffer reuse.
	// When `borrow == BorrowedRow`, Next returns `out` directly;
	// otherwise it clones before returning. Default OwnedRow.
	borrow BorrowSemantics
	out    Row
}

func newProjectOp(plan *planner.Project, child Operator) *projectOp {
	return &projectOp{child: child, targets: plan.Targets, schema: plan.Output()}
}

func (o *projectOp) Open(ctx *Context) error {
	o.ctx = ctx
	if cap(o.out) < len(o.targets) {
		o.out = make(Row, len(o.targets))
	} else {
		o.out = o.out[:len(o.targets)]
	}
	return o.child.Open(ctx)
}
func (o *projectOp) Schema() planner.Schema { return o.schema }
func (o *projectOp) Close() error           { return o.child.Close() }

// SetBorrow marks projectOp as eligible to return borrowed
// rows. (M0054-0005a-followup.)
func (o *projectOp) SetBorrow(s BorrowSemantics) { o.borrow = s }

func (o *projectOp) Next() (Row, error) {
	in, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	for i, t := range o.targets {
		v, err := evalExpr(t, in, o.ctx)
		if err != nil {
			return nil, err
		}
		o.out[i] = v
	}
	if o.borrow == BorrowedRow {
		return o.out, nil
	}
	return cloneRow(o.out), nil
}

// filterOp drops rows where the predicate doesn't evaluate to TRUE.
// NULL predicates exclude the row, matching SQL semantics.
type filterOp struct {
	child Operator
	pred  planner.Expr
	ctx   *Context
	// M0054-0005a-followup: pure pass-through borrow. The Filter
	// returns its child's row unchanged, so it can borrow exactly
	// as long as its parent allows it.
	borrow BorrowSemantics
}

func newFilterOp(plan *planner.Filter, child Operator) *filterOp {
	return &filterOp{child: child, pred: plan.Predicate}
}

func (o *filterOp) Open(ctx *Context) error { o.ctx = ctx; return o.child.Open(ctx) }
func (o *filterOp) Schema() planner.Schema  { return o.child.Schema() }
func (o *filterOp) Close() error            { return o.child.Close() }

// SetBorrow propagates the borrow contract to the child. filterOp
// itself never copies — it just hands through. So borrow-OK at
// filter ⇒ borrow-OK at child. (M0054-0005a-followup.)
func (o *filterOp) SetBorrow(s BorrowSemantics) {
	o.borrow = s
	if b, ok := o.child.(Borrowable); ok {
		b.SetBorrow(s)
	}
}

func (o *filterOp) Next() (Row, error) {
	rejected := 0
	for {
		// M0062-followup: a highly-selective filter can drain millions
		// of child rows without yielding to the parent, blocking
		// cancel propagation. Check ctx every 4096 rejections.
		if rejected&0xFFF == 0 && o.ctx != nil && o.ctx.Ctx != nil {
			if err := o.ctx.Ctx.Err(); err != nil {
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		row, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		v, err := evalExpr(o.pred, row, o.ctx)
		if err != nil {
			return nil, err
		}
		if !v.IsNull() && v.Kind == KindBool && v.BoolValue() {
			return row, nil
		}
		rejected++
	}
}

// limitOp implements LIMIT/OFFSET. Both are evaluated once at Open
// so a long stream doesn't re-evaluate.
type limitOp struct {
	child       Operator
	limitExpr   planner.Expr
	offsetExpr  planner.Expr
	limitCount  int64 // -1 for no limit
	offsetCount int64
	emitted     int64
	skipped     int64
	// M0054-0005a-followup: pass-through borrow.
	borrow BorrowSemantics
}

// SetBorrow propagates to the child. (M0054-0005a-followup.)
func (o *limitOp) SetBorrow(s BorrowSemantics) {
	o.borrow = s
	if b, ok := o.child.(Borrowable); ok {
		b.SetBorrow(s)
	}
}

func newLimitOp(plan *planner.Limit, child Operator) *limitOp {
	return &limitOp{child: child, limitExpr: plan.Limit, offsetExpr: plan.Offset, limitCount: -1}
}

func (o *limitOp) Open(ctx *Context) error {
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	if o.limitExpr != nil {
		v, err := evalExpr(o.limitExpr, nil, ctx)
		if err != nil {
			return err
		}
		if v.Kind != KindInt {
			return &ExecError{Code: "42804", Pos: o.limitExpr.Pos(), Message: "LIMIT must be integer"}
		}
		o.limitCount = v.Int
	}
	if o.offsetExpr != nil {
		v, err := evalExpr(o.offsetExpr, nil, ctx)
		if err != nil {
			return err
		}
		if v.Kind != KindInt {
			return &ExecError{Code: "42804", Pos: o.offsetExpr.Pos(), Message: "OFFSET must be integer"}
		}
		o.offsetCount = v.Int
	}
	return nil
}

func (o *limitOp) Schema() planner.Schema { return o.child.Schema() }
func (o *limitOp) Close() error           { return o.child.Close() }

func (o *limitOp) Next() (Row, error) {
	for o.skipped < o.offsetCount {
		if _, err := o.child.Next(); err != nil {
			return nil, err
		}
		o.skipped++
	}
	if o.limitCount >= 0 && o.emitted >= o.limitCount {
		return nil, EOF
	}
	row, err := o.child.Next()
	if err != nil {
		return nil, err
	}
	o.emitted++
	return row, nil
}

// sortOp fully buffers the child's output then sorts under the
// supplied key list. Stable sort matches upstream's behaviour.
type sortOp struct {
	child Operator
	keys  []planner.SortKey
	ctx   *Context
	rows  []Row
	idx   int
}

func newSortOp(plan *planner.Sort, child Operator) *sortOp {
	return &sortOp{child: child, keys: plan.Keys}
}

func (o *sortOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	for {
		// M0062-followup: a sort over millions of rows can otherwise
		// drain the child without a cancel opportunity. ctx check
		// every 4096 rows pulled.
		if len(o.rows)&0xFFF == 0 && ctx != nil && ctx.Ctx != nil {
			if err := ctx.Ctx.Err(); err != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
		}
		row, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		o.rows = append(o.rows, row)
	}
	var sortErr error
	sort.SliceStable(o.rows, func(i, j int) bool {
		for _, k := range o.keys {
			a, err := evalExpr(k.Expr, o.rows[i], ctx)
			if err != nil {
				sortErr = err
				return false
			}
			b, err := evalExpr(k.Expr, o.rows[j], ctx)
			if err != nil {
				sortErr = err
				return false
			}
			if a.IsNull() && !b.IsNull() {
				return !k.Desc
			}
			if !a.IsNull() && b.IsNull() {
				return k.Desc
			}
			if a.IsNull() && b.IsNull() {
				continue
			}
			cmp, err := compareDatum(a, b, k.Expr.Pos())
			if err != nil {
				sortErr = err
				return false
			}
			if cmp == 0 {
				continue
			}
			if k.Desc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false
	})
	return sortErr
}

func (o *sortOp) Schema() planner.Schema { return o.child.Schema() }
func (o *sortOp) Close() error {
	o.rows = nil
	o.idx = 0
	o.ctx = nil
	return o.child.Close()
}

func (o *sortOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}
