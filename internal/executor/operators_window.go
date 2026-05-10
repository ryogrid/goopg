package executor

import (
	"sort"
	"strings"

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
		slot, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}
		// Materialize at retention boundary: windowOp holds rows
		// across many Next() calls. (M0071-0010 Stage B.)
		row := slot.Materialize().Row()
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
		if err := o.evalWindowFuncs(); err != nil {
			return err
		}
	}

	return nil
}

func (o *windowOp) evalWindowFuncs() error {
	if len(o.plan.Funcs) == 0 {
		return nil
	}
	colBase := 0
	if len(o.rows) > 0 {
		colBase = len(o.rows[0]) - len(o.plan.Funcs)
	}

	// Find partition start indices (rows are already sorted by partition key).
	pStarts := []int{0}
	if len(o.rows) > 0 {
		prevKey, err := o.partitionKey(o.rows[0])
		if err != nil {
			return err
		}
		for i := 1; i < len(o.rows); i++ {
			key, err := o.partitionKey(o.rows[i])
			if err != nil {
				return err
			}
			if key != prevKey {
				pStarts = append(pStarts, i)
				prevKey = key
			}
		}
	}
	pStarts = append(pStarts, len(o.rows)) // sentinel

	// Evaluate each partition independently.
	for p := 0; p < len(pStarts)-1; p++ {
		pStart := pStarts[p]
		pEnd := pStarts[p+1]
		rowNum := int64(0)
		rank := int64(1)

		for i := pStart; i < pEnd; i++ {
			rowNum++
			if i > pStart {
				peer, err := o.samePeer(o.rows[i-1], o.rows[i])
				if err != nil {
					return err
				}
				if !peer {
					rank = rowNum
				}
			}
			localIdx := i - pStart // 0-based position within this partition

			for j, fn := range o.plan.Funcs {
				colIdx := colBase + j
				switch strings.ToLower(fn.Name) {
				case "row_number":
					o.rows[i][colIdx] = Datum{Kind: KindInt, Int: rowNum}
				case "rank":
					o.rows[i][colIdx] = Datum{Kind: KindInt, Int: rank}
				case "lag", "lead":
					offset := int64(1)
					if len(fn.Args) >= 2 {
						v, err := evalExpr(fn.Args[1], o.rows[i], o.ctx)
						if err != nil {
							return err
						}
						if v.Kind == KindInt {
							offset = v.Int
						}
					}
					var targetLocal int
					if strings.ToLower(fn.Name) == "lag" {
						targetLocal = localIdx - int(offset)
					} else {
						targetLocal = localIdx + int(offset)
					}
					targetGlobal := pStart + targetLocal
					if targetGlobal < pStart || targetGlobal >= pEnd {
						// Out of partition — return default or NULL.
						if len(fn.Args) >= 3 {
							v, err := evalExpr(fn.Args[2], o.rows[i], o.ctx)
							if err != nil {
								return err
							}
							o.rows[i][colIdx] = v
						} else {
							o.rows[i][colIdx] = NullDatum
						}
					} else {
						v, err := evalExpr(fn.Args[0], o.rows[targetGlobal], o.ctx)
						if err != nil {
							return err
						}
						o.rows[i][colIdx] = v
					}
				default:
					return &ExecError{Code: "0A000", Pos: fn.Pos(), Message: "window function is not supported in v0 executor"}
				}
			}
		}
	}
	return nil
}

func (o *windowOp) partitionKey(row Row) (string, error) {
	if len(o.plan.PartitionBy) == 0 {
		return "__all__", nil
	}
	parts := make([]string, 0, len(o.plan.PartitionBy))
	for _, pe := range o.plan.PartitionBy {
		v, err := evalExpr(pe, row, o.ctx)
		if err != nil {
			return "", err
		}
		parts = append(parts, datumKey(v))
	}
	return strings.Join(parts, "|"), nil
}

func (o *windowOp) samePeer(prev, cur Row) (bool, error) {
	if len(o.plan.OrderBy) == 0 {
		return true, nil
	}
	for _, ok := range o.plan.OrderBy {
		a, err := evalExpr(ok.Expr, prev, o.ctx)
		if err != nil {
			return false, err
		}
		b, err := evalExpr(ok.Expr, cur, o.ctx)
		if err != nil {
			return false, err
		}
		if a.IsNull() || b.IsNull() {
			if a.IsNull() && b.IsNull() {
				continue
			}
			return false, nil
		}
		cmp, err := compareDatum(a, b, ok.Expr.Pos())
		if err != nil {
			return false, err
		}
		if cmp != 0 {
			return false, nil
		}
	}
	return true, nil
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

func (o *windowOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return asSlot(o.schema, row), nil
}

func (o *windowOp) Close() error {
	o.rows = nil
	o.ctx = nil
	o.idx = 0
	return o.child.Close()
}
func (o *windowOp) Schema() planner.Schema { return o.schema }
