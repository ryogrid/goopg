package executor

import (
	"sort"

	"github.com/goopg/goopg/internal/planner"
)

// windowOp is the Stage-A WindowAgg executor skeleton. It drains
// child rows, sorts by PARTITION BY/ORDER BY, and appends one
// placeholder column per planned window function.
type windowOp struct {
	plan   *planner.WindowAgg
	child  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int
}

func newWindowOp(plan *planner.WindowAgg, child Operator) *windowOp {
	return &windowOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *windowOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}
	for {
		row, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		dup := make(Row, len(row))
		copy(dup, row)
		o.rows = append(o.rows, dup)
	}

	if len(o.plan.PartitionBy) > 0 || len(o.plan.OrderBy) > 0 {
		var sortErr error
		sort.SliceStable(o.rows, func(i, j int) bool {
			if sortErr != nil {
				return false
			}
			for _, pe := range o.plan.PartitionBy {
				a, err := evalExpr(pe, o.rows[i], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				b, err := evalExpr(pe, o.rows[j], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				cmp, decided, err := compareSortDatums(a, b, pe.Pos(), false)
				if err != nil {
					sortErr = err
					return false
				}
				if decided {
					return cmp < 0
				}
			}
			for _, ok := range o.plan.OrderBy {
				a, err := evalExpr(ok.Expr, o.rows[i], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				b, err := evalExpr(ok.Expr, o.rows[j], ctx)
				if err != nil {
					sortErr = err
					return false
				}
				cmp, decided, err := compareSortDatums(a, b, ok.Expr.Pos(), ok.Desc)
				if err != nil {
					sortErr = err
					return false
				}
				if decided {
					if ok.Desc {
						return cmp > 0
					}
					return cmp < 0
				}
			}
			return false
		})
		if sortErr != nil {
			return sortErr
		}
	}

	if n := len(o.plan.Funcs); n > 0 {
		for i := range o.rows {
			out := make(Row, 0, len(o.rows[i])+n)
			out = append(out, o.rows[i]...)
			for j := 0; j < n; j++ {
				out = append(out, NullDatum)
			}
			o.rows[i] = out
		}
	}

	return nil
}

func compareSortDatums(a, b Datum, pos int, desc bool) (cmp int, decided bool, err error) {
	if a.IsNull() && !b.IsNull() {
		if desc {
			return 1, true, nil
		}
		return -1, true, nil
	}
	if !a.IsNull() && b.IsNull() {
		if desc {
			return -1, true, nil
		}
		return 1, true, nil
	}
	if a.IsNull() && b.IsNull() {
		return 0, false, nil
	}
	c, err := compareDatum(a, b, pos)
	if err != nil {
		return 0, false, err
	}
	if c == 0 {
		return 0, false, nil
	}
	return c, true, nil
}

func (o *windowOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}

func (o *windowOp) Close() error           { return o.child.Close() }
func (o *windowOp) Schema() planner.Schema { return o.schema }
