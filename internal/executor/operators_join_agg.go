package executor

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// joinOp is a nested-loop implementation that buffers both children
// in Open and emits joined rows from an in-memory result slice.
type joinOp struct {
	plan   *planner.Join
	left   Operator
	right  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int
}

func newJoinOp(plan *planner.Join, left, right Operator) *joinOp {
	schema := plan.Output()
	if len(schema) == 0 {
		schema = append(schema, left.Schema()...)
		schema = append(schema, right.Schema()...)
	}
	return &joinOp{plan: plan, left: left, right: right, schema: schema}
}

func (o *joinOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.left.Open(ctx); err != nil {
		return err
	}
	if err := o.right.Open(ctx); err != nil {
		_ = o.left.Close()
		return err
	}

	leftRows, err := drainRows(o.left)
	if err != nil {
		return err
	}
	rightRows, err := drainRows(o.right)
	if err != nil {
		return err
	}

	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	if leftWidth == 0 && len(leftRows) > 0 {
		leftWidth = len(leftRows[0])
	}
	if rightWidth == 0 && len(rightRows) > 0 {
		rightWidth = len(rightRows[0])
	}
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	rightMatched := make([]bool, len(rightRows))
	for _, l := range leftRows {
		matched := false
		for j, r := range rightRows {
			joined := concatRows(l, r)
			ok, err := o.joinPredicateMatch(joined)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			matched = true
			rightMatched[j] = true
			o.rows = append(o.rows, joined)
		}
		if !matched && (o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull) {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}

	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for j, r := range rightRows {
			if rightMatched[j] {
				continue
			}
			o.rows = append(o.rows, concatRows(nullLeft, r))
		}
	}
	return nil
}

func (o *joinOp) joinPredicateMatch(row Row) (bool, error) {
	if o.plan.Predicate == nil {
		return true, nil
	}
	v, err := evalExpr(o.plan.Predicate, row, o.ctx)
	if err != nil {
		return false, err
	}
	return !v.IsNull() && v.Kind == KindBool && v.Bool, nil
}

func (o *joinOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}

func (o *joinOp) Close() error {
	errL := o.left.Close()
	errR := o.right.Close()
	if errL != nil {
		return errL
	}
	return errR
}

func (o *joinOp) Schema() planner.Schema { return o.schema }

// aggregateOp performs grouped aggregation in memory.
type aggregateOp struct {
	plan   *planner.Aggregate
	child  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int
}

type aggRuntime struct {
	hasValue bool
	value    Datum
	sum      int64
	count    int64
	distinct map[string]struct{}
}

func newAggregateOp(plan *planner.Aggregate, child Operator) *aggregateOp {
	return &aggregateOp{plan: plan, child: child, schema: plan.Output()}
}

func (o *aggregateOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.child.Open(ctx); err != nil {
		return err
	}

	type groupRuntime struct {
		groupValues Row
		aggs        []aggRuntime
	}

	groups := map[string]*groupRuntime{}
	order := make([]string, 0)

	if len(o.plan.GroupExprs) == 0 {
		groups["__all__"] = &groupRuntime{groupValues: nil, aggs: make([]aggRuntime, len(o.plan.Aggs))}
		order = append(order, "__all__")
	}

	for {
		row, err := o.child.Next()
		if err == EOF {
			break
		}
		if err != nil {
			return err
		}

		key, groupValues, err := o.evalGroupKey(row)
		if err != nil {
			return err
		}
		gr, ok := groups[key]
		if !ok {
			gr = &groupRuntime{groupValues: groupValues, aggs: make([]aggRuntime, len(o.plan.Aggs))}
			groups[key] = gr
			order = append(order, key)
		}

		for i, call := range o.plan.Aggs {
			if err := o.applyAgg(&gr.aggs[i], call, row); err != nil {
				return err
			}
		}
	}

	o.rows = make([]Row, 0, len(order))
	for _, key := range order {
		gr := groups[key]
		if gr == nil {
			continue
		}
		out := make(Row, 0, len(gr.groupValues)+len(o.plan.Aggs))
		out = append(out, gr.groupValues...)
		for i, call := range o.plan.Aggs {
			out = append(out, o.finishAgg(gr.aggs[i], call))
		}
		o.rows = append(o.rows, out)
	}
	return nil
}

func (o *aggregateOp) evalGroupKey(row Row) (string, Row, error) {
	if len(o.plan.GroupExprs) == 0 {
		return "__all__", nil, nil
	}
	vals := make(Row, 0, len(o.plan.GroupExprs))
	parts := make([]string, 0, len(o.plan.GroupExprs))
	for _, g := range o.plan.GroupExprs {
		v, err := evalExpr(g, row, o.ctx)
		if err != nil {
			return "", nil, err
		}
		vals = append(vals, v)
		parts = append(parts, datumKey(v))
	}
	return strings.Join(parts, "|"), vals, nil
}

func (o *aggregateOp) applyAgg(st *aggRuntime, call planner.AggregateCall, row Row) error {
	name := strings.ToLower(call.Name)
	if call.Star {
		if name != "count" {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s(*) is not supported", call.Name)}
		}
		if call.Distinct {
			return &ExecError{Code: "0A000", Pos: call.Pos(), Message: "count(distinct *) is not supported"}
		}
		st.count++
		st.hasValue = true
		return nil
	}

	if call.Arg == nil {
		return &ExecError{Code: "XX000", Pos: call.Pos(), Message: "aggregate argument missing"}
	}
	arg, err := evalExpr(call.Arg, row, o.ctx)
	if err != nil {
		return err
	}
	if arg.IsNull() {
		return nil
	}

	if call.Distinct {
		if st.distinct == nil {
			st.distinct = map[string]struct{}{}
		}
		k := datumKey(arg)
		if _, seen := st.distinct[k]; seen {
			return nil
		}
		st.distinct[k] = struct{}{}
	}

	switch name {
	case "count":
		st.count++
		st.hasValue = true
	case "sum", "avg":
		if arg.Kind != KindInt {
			return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires integer argument in v0", name)}
		}
		st.sum += arg.Int
		st.count++
		st.hasValue = true
	case "min":
		if !st.hasValue {
			st.value = arg
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp < 0 {
			st.value = arg
		}
	case "max":
		if !st.hasValue {
			st.value = arg
			st.hasValue = true
			return nil
		}
		cmp, err := compareDatum(arg, st.value, call.Pos())
		if err != nil {
			return err
		}
		if cmp > 0 {
			st.value = arg
		}
	default:
		return &ExecError{Code: "0A000", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s is not supported", call.Name)}
	}
	return nil
}

func (o *aggregateOp) finishAgg(st aggRuntime, call planner.AggregateCall) Datum {
	switch strings.ToLower(call.Name) {
	case "count":
		return Datum{Kind: KindInt, Int: st.count}
	case "sum":
		if !st.hasValue {
			return NullDatum
		}
		return Datum{Kind: KindInt, Int: st.sum}
	case "avg":
		if st.count == 0 {
			return NullDatum
		}
		return Datum{Kind: KindInt, Int: st.sum / st.count}
	case "min", "max":
		if !st.hasValue {
			return NullDatum
		}
		return st.value
	}
	return NullDatum
}

func (o *aggregateOp) Next() (Row, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}

func (o *aggregateOp) Close() error { return o.child.Close() }

func (o *aggregateOp) Schema() planner.Schema { return o.schema }

func drainRows(op Operator) ([]Row, error) {
	rows := make([]Row, 0)
	for {
		row, err := op.Next()
		if err == EOF {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		dup := make(Row, len(row))
		copy(dup, row)
		rows = append(rows, dup)
	}
}

func concatRows(a, b Row) Row {
	out := make(Row, 0, len(a)+len(b))
	out = append(out, a...)
	out = append(out, b...)
	return out
}

func nullRow(n int) Row {
	out := make(Row, n)
	for i := range out {
		out[i] = NullDatum
	}
	return out
}

func datumKey(d Datum) string {
	switch d.Kind {
	case KindNull:
		return "n"
	case KindBool:
		if d.Bool {
			return "b:t"
		}
		return "b:f"
	case KindInt:
		return fmt.Sprintf("i:%d", d.Int)
	case KindString:
		return "s:" + d.String
	case KindBytes:
		return "x:" + string(d.Bytes)
	case KindTime:
		return fmt.Sprintf("t:%d", d.Time.UnixNano())
	}
	return fmt.Sprintf("k:%d", d.Kind)
}
