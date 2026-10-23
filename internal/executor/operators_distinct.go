package executor

// operators_distinct.go — distinctOp implements SELECT DISTINCT row deduplication.
// M0097-0005.

import (
	"sort"
	"strings"

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
	// junkPos marks resjunk (rowmark ctid) positions excluded from the
	// dedup key — a unique ctid datum would otherwise make every row
	// distinct. M0143-0009.
	junkPos map[int]bool
	// colTrims flags bpchar-typed columns: dedup keys and the output-order
	// comparison run on the bcTruelen image so a char(20) 'x' and a
	// char(5) 'x' are one row — hashbpchar/bpcharlt parity. M0146.
	colTrims []bool
	// colText flags character-string columns, whose output-order
	// comparison is plain text whatever the values look like (M0146-0053).
	colText []bool
	// unordered skips the output sort: the consumer is a hashed aggregate
	// whose result does not depend on input order (markDistinctUnordered).
	unordered bool
}

func newDistinctOp(p *optimizer.Distinct, child Operator) *distinctOp {
	var junk map[int]bool
	for i, c := range p.Output() {
		if c.Resjunk {
			if junk == nil {
				junk = map[int]bool{}
			}
			junk[i] = true
		}
	}
	return &distinctOp{plan: p, child: child, schema: p.Output(), junkPos: junk,
		colTrims: bpcharSchemaTrims(p.Output()), colText: characterStringSchemaCols(p.Output())}
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
	var keyBuf []byte
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
		// The key is built in a reused buffer and looked up without
		// allocating; only a row that starts a new key is cloned (the child
		// slot is reused) and keeps a key string (M0122-0012 slice 70).
		keyBuf = appendRowKeyTrimmed(keyBuf[:0], row, o.junkPos, o.colTrims)
		if _, dup := seen[string(keyBuf)]; dup {
			continue
		}
		seen[string(keyBuf)] = struct{}{}
		o.rows = append(o.rows, cloneRow(row))
	}
	if o.unordered {
		return nil
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
			if col < len(o.colTrims) && o.colTrims[col] {
				a, b = trimStringDatum(a), trimStringDatum(b)
			}
			cmp, err := compareDatumPlain(a, b, 0, col < len(o.colText) && o.colText[col])
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
	// keyTrims[i] marks a bpchar-typed DISTINCT-ON key column: its key
	// image is the bcTruelen form so padding-width variants of one value
	// stay one key. Contiguity under that image is guaranteed by the
	// child's pre-sort, whose keys evalSortKeyValue already trims.
	keyTrims []bool
	// seen is the hashed mode's key set (plan.Hashed: PG's UNIQUE_PATH_HASH
	// for a unique-ified semijoin RHS, M0146-0005dk) — the input is
	// unsorted, so a key is a duplicate when it was ever seen, not only
	// when it equals the previous row's.
	seen map[string]struct{}
	// keyBuf is Next's reused key buffer (appendDistinctOnKey).
	keyBuf []byte
}

// appendDistinctOnKey appends row's DISTINCT ON key to buf: for each key
// column, datumKey of its value — bpchar padding stripped where keyTrims says
// so, as trimStringDatum does — then a NUL. These are the bytes Next built by
// concatenating one datumKey string per column for every input row; the
// buffer is reused and compared or looked up without allocating, so only a
// row that starts a new key allocates its string (M0122-0012 slice 60,
// TPC-DS Q87's three DISTINCTs).
func (o *distinctOnOp) appendDistinctOnKey(buf []byte, row Row) []byte {
	for ki, idx := range o.plan.KeyCols {
		if idx < 0 || idx >= len(row) {
			continue
		}
		v := row[idx]
		if ki < len(o.keyTrims) && o.keyTrims[ki] && v.Kind == KindString {
			// trimStringDatum's result (it acts on KindString only).
			buf = append(buf, 's', ':')
			buf = append(buf, strings.TrimRight(v.StringValue(), " ")...)
		} else {
			buf = appendDatumKey(buf, v)
		}
		buf = append(buf, 0)
	}
	return buf
}

func newDistinctOnOp(p *optimizer.DistinctOn, child Operator) *distinctOnOp {
	var trims []bool
	cs := child.Schema()
	for i, idx := range p.KeyCols {
		if idx >= 0 && idx < len(cs) && bpcharCatalogType(cs[idx].Type) {
			if trims == nil {
				trims = make([]bool, len(p.KeyCols))
			}
			trims[i] = true
		}
	}
	return &distinctOnOp{plan: p, child: child, schema: p.Output(), keyTrims: trims}
}

func (o *distinctOnOp) Schema() optimizer.Schema { return o.schema }

func (o *distinctOnOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.started = false
	o.prevKey = ""
	o.seen = nil
	if o.plan.Hashed {
		o.seen = make(map[string]struct{})
	}
	return o.child.Open(ctx)
}

func (o *distinctOnOp) Next() (TupleSlot, error) {
	for {
		slot, err := o.child.Next()
		if err != nil {
			return nil, err
		}
		if slot == nil {
			continue
		}
		row := slot.Row()
		// The key of the DISTINCT ON columns, in the reused buffer.
		o.keyBuf = o.appendDistinctOnKey(o.keyBuf[:0], row)
		if o.seen != nil {
			if _, dup := o.seen[string(o.keyBuf)]; dup {
				continue
			}
			o.seen[string(o.keyBuf)] = struct{}{}
			return SlotFromRow(o.schema, cloneRow(row)), nil
		}
		if !o.started || string(o.keyBuf) != o.prevKey {
			o.started = true
			o.prevKey = string(o.keyBuf)
			return SlotFromRow(o.schema, cloneRow(row)), nil
		}
		// Duplicate key: skip this row.
	}
}

func (o *distinctOnOp) Close() error {
	o.seen = nil
	return o.child.Close()
}

// aggregateIgnoresInputOrder reports whether p's result is the same for every
// order of its input rows: a hashed (or ungrouped) aggregate whose every
// aggregate is count, min, max, or sum / avg over a non-float input. Float
// sums and averages round differently in different orders, and the
// collecting aggregates (array_agg, string_agg, ...) and user-defined ones
// keep their input order, so any of those keeps the input as it is.
func aggregateIgnoresInputOrder(p *optimizer.Aggregate) bool {
	if p.Strategy != optimizer.AggStrategyHashed || p.Mode == optimizer.AggModeFinal || p.GroupingSets != nil {
		return false
	}
	for i := range p.Aggs {
		a := &p.Aggs[i]
		if len(a.OrderBy) > 0 || a.WithinGroup {
			return false
		}
		switch strings.ToLower(a.Name) {
		case "count", "min", "max":
		case "sum", "avg":
			switch strings.ToLower(a.InputType.Name) {
			case "float4", "float8", "real", "double precision", "":
				return false
			}
		default:
			return false
		}
	}
	return true
}

// markDistinctUnordered tells a hashed DISTINCT feeding op — directly or
// through projections — that its consumer ignores row order, so it may emit
// its rows in first-seen order instead of sorting them (M0122-0012 slice 69).
// PostgreSQL's HashAggregate DISTINCT emits hash-table order and never sorts;
// goopg's sort exists only to make an unordered DISTINCT's own output
// deterministic, which no order-insensitive consumer can observe. TPC-DS
// Q75's UNION de-duplication sorted ~485 K rows that a sum() HashAggregate
// then regrouped.
func markDistinctUnordered(op Operator) {
	for op != nil {
		switch o := op.(type) {
		case *distinctOp:
			o.unordered = true
			return
		case *projectOp:
			op = o.child
		case *instrumentedOp:
			op = o.inner
		default:
			return
		}
	}
}
