package executor

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/planner"
)

// joinOp is a join operator that dispatches on plan.Algo.
// Hash joins use lazy materialization (M0036): joined rows are
// yielded on demand via Next() instead of pre-computed in o.rows.
// Merge and nested-loop still materialize in o.rows.
type joinOp struct {
	plan   *planner.Join
	left   Operator
	right  Operator
	schema planner.Schema

	ctx  *Context
	rows []Row
	idx  int

	// M0036 lazy-output state (hash join only)
	lazyHash    map[string][]Row // build-side hash table
	lazyProbe   Operator         // probe side (streaming)
	lazyRow     Row              // current probe row
	lazyMatches []Row            // matches for current probe row (borrowed from lazyHash)
	lazyMatchIdx int
	lazyActive  bool // true between probeRow and last match
	lazyLW      int  // left schema width
	lazyRW      int  // right schema width
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
	if o.plan.Algo == planner.JoinAlgoHash {
		return o.openLazyHashJoin(ctx)
	}
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

	if o.plan.Algo == planner.JoinAlgoMerge {
		return o.runMergeJoin(leftRows, rightRows, leftWidth, rightWidth)
	}
	return o.runNestedLoop(leftRows, rightRows, leftWidth, rightWidth)
}

// runNestedLoop is the universal fallback. O(N*M) over the two
// drained sides; supports INNER / LEFT / RIGHT / FULL / CROSS.
func (o *joinOp) runNestedLoop(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
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

// openLazyHashJoin builds a hash table from the build side and sets
// up lazy output. When ctx.WorkMem > 0, the build side uses
// drainRowsBounded to spill to disk if row data exceeds the budget.
func (o *joinOp) openLazyHashJoin(ctx *Context) error {
	leftWidth := len(o.left.Schema())
	rightWidth := len(o.right.Schema())
	o.lazyLW = leftWidth
	o.lazyRW = rightWidth
	budget := ctx.WorkMem
	if budget <= 0 {
		budget = 512 * 1024 * 1024 // default 512 MiB
	}
	if o.plan.BuildLeft {
		if err := o.left.Open(ctx); err != nil { return err }
		buildOp, err := drainRowsBounded(o.left, budget)
		_ = o.left.Close()
		if err != nil { return err }
		if err := buildOp.Open(ctx); err != nil { return err }
		for {
			l, err := buildOp.Next()
			if err == EOF { break }
			if err != nil { return err }
			if leftWidth == 0 && len(l) > 0 {
				leftWidth = len(l); o.lazyLW = leftWidth
			}
			key, ok, err := o.evalHashKey(o.plan.LeftKey, concatRows(l, nullRow(rightWidth)))
			if err != nil { return err }
			if !ok { continue }
			if o.lazyHash == nil { o.lazyHash = make(map[string][]Row) }
			o.lazyHash[key] = append(o.lazyHash[key], l)
		}
		_ = buildOp.Close()
		if err := o.right.Open(ctx); err != nil { return err }
		o.lazyProbe = o.right
		return nil
	}
	if err := o.right.Open(ctx); err != nil { return err }
	buildOp, err := drainRowsBounded(o.right, budget)
	_ = o.right.Close()
	if err != nil { return err }
	if err := buildOp.Open(ctx); err != nil { return err }
	for {
		r, err := buildOp.Next()
		if err == EOF { break }
		if err != nil { return err }
		if rightWidth == 0 && len(r) > 0 {
			rightWidth = len(r); o.lazyRW = rightWidth
		}
		key, ok, err := o.evalHashKey(o.plan.RightKey, concatRows(nullRow(leftWidth), r))
		if err != nil { return err }
		if !ok { continue }
		if o.lazyHash == nil { o.lazyHash = make(map[string][]Row) }
		o.lazyHash[key] = append(o.lazyHash[key], r)
	}
	_ = buildOp.Close()
	if err := o.left.Open(ctx); err != nil { return err }
	o.lazyProbe = o.left
	return nil
}

type mergeKeyedRow struct {
	row    Row
	key    Datum
	hasKey bool
}

// runMergeJoin sorts both sides on their join keys and merges the
// two ordered streams. NULL keys never match (same as hash join).
// Supports INNER / LEFT / RIGHT / FULL outer semantics.
func (o *joinOp) runMergeJoin(leftRows, rightRows []Row, leftWidth, rightWidth int) error {
	nullLeft := nullRow(leftWidth)
	nullRight := nullRow(rightWidth)

	leftKeyed, leftNull, err := o.buildMergeSide(leftRows, true, leftWidth, rightWidth)
	if err != nil {
		return err
	}
	rightKeyed, rightNull, err := o.buildMergeSide(rightRows, false, leftWidth, rightWidth)
	if err != nil {
		return err
	}

	i, j := 0, 0
	for i < len(leftKeyed) && j < len(rightKeyed) {
		cmp, err := compareDatum(leftKeyed[i].key, rightKeyed[j].key, o.plan.Pos())
		if err != nil {
			return err
		}
		switch {
		case cmp < 0:
			if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
				o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
			}
			i++
		case cmp > 0:
			if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
				o.rows = append(o.rows, concatRows(nullLeft, rightKeyed[j].row))
			}
			j++
		default:
			li := i
			for i < len(leftKeyed) {
				eq, err := compareDatum(leftKeyed[li].key, leftKeyed[i].key, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				i++
			}
			rj := j
			for j < len(rightKeyed) {
				eq, err := compareDatum(rightKeyed[rj].key, rightKeyed[j].key, o.plan.Pos())
				if err != nil {
					return err
				}
				if eq != 0 {
					break
				}
				j++
			}
			for a := li; a < i; a++ {
				for b := rj; b < j; b++ {
					o.rows = append(o.rows, concatRows(leftKeyed[a].row, rightKeyed[b].row))
				}
			}
		}
	}

	for ; i < len(leftKeyed); i++ {
		if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
			o.rows = append(o.rows, concatRows(leftKeyed[i].row, nullRight))
		}
	}
	for ; j < len(rightKeyed); j++ {
		if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
			o.rows = append(o.rows, concatRows(nullLeft, rightKeyed[j].row))
		}
	}

	if o.plan.Type == planner.JoinTypeLeft || o.plan.Type == planner.JoinTypeFull {
		for _, l := range leftNull {
			o.rows = append(o.rows, concatRows(l, nullRight))
		}
	}
	if o.plan.Type == planner.JoinTypeRight || o.plan.Type == planner.JoinTypeFull {
		for _, r := range rightNull {
			o.rows = append(o.rows, concatRows(nullLeft, r))
		}
	}

	return nil
}

func (o *joinOp) buildMergeSide(rows []Row, isLeft bool, leftWidth, rightWidth int) ([]mergeKeyedRow, []Row, error) {
	var paddedLeft, paddedRight Row
	if isLeft {
		paddedRight = nullRow(rightWidth)
	} else {
		paddedLeft = nullRow(leftWidth)
	}
	keyExpr := o.plan.RightKey
	if isLeft {
		keyExpr = o.plan.LeftKey
	}
	if keyExpr == nil {
		return nil, nil, fmt.Errorf("merge join key is nil")
	}

	keyed := make([]mergeKeyedRow, 0, len(rows))
	nullKey := make([]Row, 0)
	for _, row := range rows {
		var evalRow Row
		if isLeft {
			evalRow = concatRows(row, paddedRight)
		} else {
			evalRow = concatRows(paddedLeft, row)
		}
		v, err := evalExpr(keyExpr, evalRow, o.ctx)
		if err != nil {
			return nil, nil, err
		}
		if v.IsNull() {
			nullKey = append(nullKey, row)
			continue
		}
		keyed = append(keyed, mergeKeyedRow{row: row, key: v, hasKey: true})
	}

	var sortErr error
	sort.SliceStable(keyed, func(i, j int) bool {
		cmp, err := compareDatum(keyed[i].key, keyed[j].key, o.plan.Pos())
		if err != nil {
			sortErr = err
			return false
		}
		return cmp < 0
	})
	if sortErr != nil {
		return nil, nil, sortErr
	}

	return keyed, nullKey, nil
}

// evalHashKey evaluates one side of the hash-join key against a
// padded row and returns its canonical key string. The boolean
// is false when the key evaluated to NULL (never matches).
func (o *joinOp) evalHashKey(keyExpr planner.Expr, row Row) (string, bool, error) {
	v, err := evalExpr(keyExpr, row, o.ctx)
	if err != nil {
		return "", false, err
	}
	if v.IsNull() {
		return "", false, nil
	}
	return datumKey(v), true, nil
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
	// M0036 lazy output: yield joined rows on demand.
	if o.lazyProbe != nil {
		return o.nextLazy()
	}
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return row, nil
}

// nextLazy yields one joined row at a time for lazy hash joins.
func (o *joinOp) nextLazy() (Row, error) {
	nullLeft := nullRow(o.lazyLW)
	nullRight := nullRow(o.lazyRW)
	for {
		// Continue serving matches from current probe row. Apply
		// the full join Predicate per emitted row — hash matching
		// only checks LeftKey=RightKey, but the planner may have
		// ANDed extra residual conjuncts onto the Predicate via
		// pushOneConjunct (e.g. TPC-H Q9's `ps_partkey=l_partkey`
		// pushed onto the part-join). Without the post-hash filter
		// those extras are silently dropped and the join over-emits.
		for o.lazyActive && o.lazyMatchIdx < len(o.lazyMatches) {
			m := o.lazyMatches[o.lazyMatchIdx]
			o.lazyMatchIdx++
			var joined Row
			if o.plan.BuildLeft {
				joined = concatRows(m, o.lazyRow)
			} else {
				joined = concatRows(o.lazyRow, m)
			}
			ok, err := o.joinPredicateMatch(joined)
			if err != nil {
				return nil, err
			}
			if !ok {
				continue
			}
			return joined, nil
		}
		o.lazyActive = false
		// Pull next probe row.
		r, err := o.lazyProbe.Next()
		if err == EOF {
			return nil, EOF
		}
		if err != nil {
			return nil, err
		}
		o.lazyRow = r
		var key string
		var ok bool
		if o.plan.BuildLeft {
			// Build left: probe is right. Hash key from right row.
			key, ok, err = o.evalHashKey(o.plan.RightKey, concatRows(nullLeft, r))
		} else {
			// Build right: probe is left. Hash key from left row.
			key, ok, err = o.evalHashKey(o.plan.LeftKey, concatRows(r, nullRight))
		}
		if err != nil {
			return nil, err
		}
		matches := o.lazyHash[key]
		if !ok {
			matches = nil
		}
		if len(matches) == 0 {
			if o.plan.Type == planner.JoinTypeLeft && !o.plan.BuildLeft {
				// LEFT JOIN: preserve unmatched left rows.
				o.lazyRow = nil
				return concatRows(r, nullRight), nil
			}
			// No matches, not LEFT — skip this probe row.
			continue
		}
		o.lazyMatches = matches
		o.lazyMatchIdx = 0
		o.lazyActive = true
		// Continue loop to yield first match.
	}
}

func (o *joinOp) Close() error {
	o.rows = nil
	o.lazyHash = nil
	o.lazyProbe = nil
	o.lazyRow = nil
	o.lazyMatches = nil
	o.lazyMatchIdx = 0
	o.lazyActive = false
	o.ctx = nil
	o.idx = 0
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
	// sum tracks INT-only running sums; numericSum tracks the
	// NUMERIC accumulator. Each aggregate-call uses exactly one
	// of them based on the first non-NULL argument's kind. The
	// two are not mixed within a single aggregate.
	sum        int64
	numericSum Datum
	count      int64
	distinct   map[string]struct{}
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
		if ctx.Ctx != nil {
			if cerr := ctx.Ctx.Err(); cerr != nil {
				return &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
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
		switch arg.Kind {
		case KindInt:
			st.sum += arg.Int
		case KindNumeric:
			if !st.hasValue || st.numericSum.Kind != KindNumeric {
				st.numericSum = Datum{Kind: KindNumeric, NumericScale: arg.NumericScale}
			}
			s, err := numericAdd(st.numericSum, arg)
			if err != nil {
				return &ExecError{Code: "22003", Pos: call.Pos(), Message: err.Error()}
			}
			st.numericSum = s
		default:
			return &ExecError{Code: "42804", Pos: call.Pos(), Message: fmt.Sprintf("aggregate %s requires numeric argument in v0", name)}
		}
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
		if st.numericSum.Kind == KindNumeric {
			return st.numericSum
		}
		return Datum{Kind: KindInt, Int: st.sum}
	case "avg":
		if st.count == 0 {
			return NullDatum
		}
		if st.numericSum.Kind == KindNumeric {
			d, err := numericDiv(st.numericSum, numericFromInt(st.count), call.Pos())
			if err != nil {
				return NullDatum
			}
			return d
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

func (o *aggregateOp) Close() error {
	o.rows = nil
	o.ctx = nil
	o.idx = 0
	return o.child.Close()
}

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

// datumToInt64Key converts d to an int64 hash key without any allocation.
// Returns (key, true) when d is KindInt or a KindNumeric that normalises
// to an integer (scale == 0 after stripping trailing zeros). Returns
// (0, false) for NULL, bool, string, time, interval, and fractional
// numerics. Used by the MHJ int64 fast path (M0043-0003).
//
// The int64 canonical form matches canonicalNumericKey: KindInt(v) and
// KindNumeric{mantissa=v*10^n, scale=n} both produce the same int64
// after normalisation, preserving cross-kind hash equality.
func datumToInt64Key(d Datum) (int64, bool) {
	switch d.Kind {
	case KindInt:
		return d.Int, true
	case KindNumeric:
		if d.NumericBig != nil {
			return 0, false // overflow lane: not representable as int64
		}
		m, s := d.NumericMantissa, int(d.NumericScale)
		for s > 0 && m%10 == 0 {
			m /= 10
			s--
		}
		if s == 0 {
			return m, true
		}
		return 0, false // fractional numeric
	}
	return 0, false
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
		// Cross-kind hash compatibility: integers and numerics
		// must hash equal when they represent the same value, so
		// `aid = $1` matches whether $1 lands as KindInt or as a
		// scale-0 KindNumeric. Normalise both to the same shape.
		return canonicalNumericKey(d.Int, 0)
	case KindNumeric:
		return canonicalNumericKey(d.NumericMantissa, int(d.NumericScale))
	case KindString:
		return "s:" + d.String
	case KindBytes:
		return "x:" + string(d.Bytes)
	case KindTime:
		var buf [20]byte
		b := append(buf[:0], 't', ':')
		b = strconv.AppendInt(b, d.Time.UnixNano(), 10)
		return string(b)
	case KindInterval:
		var buf [32]byte
		b := append(buf[:0], 'v', ':')
		b = strconv.AppendInt(b, int64(d.IntervalMonths), 10)
		b = append(b, ':')
		b = strconv.AppendInt(b, int64(d.IntervalDays), 10)
		return string(b)
	}
	return fmt.Sprintf("k:%d", d.Kind)
}

// canonicalNumericKey produces a string identifier that's identical
// for two numerics that compare equal. `1` (m=1,s=0), `1.0`
// (m=10,s=1), and `1.00` (m=100,s=2) all canonicalise to the same
// key. Trailing zero pairs (one digit + one scale step) are stripped
// until either the scale reaches 0 or the trailing digit is non-zero.
// Negative-scale results are flagged as `e<N>` so `100` (m=100,s=0)
// and `100` (m=1,s=-2) — should the latter ever arise — both map
// to the same canonical form. v0 never produces negative scales,
// but the helper is kept robust.
func canonicalNumericKey(mantissa int64, scale int) string {
	// Special case: 0 at any scale collapses to a single value.
	if mantissa == 0 {
		return "m:0:0"
	}
	for scale > 0 && mantissa%10 == 0 {
		mantissa /= 10
		scale--
	}
	// Use strconv.AppendInt instead of fmt.Sprintf to avoid format-string
	// parsing overhead on the string-key hot path.
	var buf [32]byte
	b := append(buf[:0], 'm', ':')
	b = strconv.AppendInt(b, mantissa, 10)
	b = append(b, ':')
	b = strconv.AppendInt(b, int64(scale), 10)
	return string(b)
}
