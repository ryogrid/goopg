package executor

import (
	"strings"

	"github.com/goopg/goopg/internal/optimizer"
)

// maxRecursiveDepth is a safety limit to prevent infinite loops in
// WITH RECURSIVE queries. After this many iterations, execution stops
// with an error. Matches PostgreSQL's default max_recursion_depth (1000).
// M0097-0006.
const maxRecursiveDepth = 1000

// maxRecursiveIterationRows bounds how many rows a SINGLE fixpoint
// iteration may accumulate before Next() gives up and errors out.
//
// M0134-0086: the maxRecursiveDepth counter above only advances once per
// *completed* iteration — Phase 2 fully drains o.recursive to EOF before
// incrementing depth again. A recursive term that itself never reaches EOF
// within one iteration (e.g. a WITH RECURSIVE query nested inside another
// WITH RECURSIVE query, where the inner query's recursive term unions a
// reference back out to the still-open outer query — with.sql's "with
// recursive q as (... union all (with recursive x as (... union all
// (select * from q union all select * from x)) select * from x)) select *
// from q limit 32" is real, PG-accepted SQL, see
// postgres/src/test/regress/sql/with.sql) never advances depth at all: it
// is stuck accumulating rows inside the very first iteration forever. Real
// PostgreSQL (nodeRecursiveunion.c) evaluates the recursive term and
// returns each produced tuple to its caller as it is produced, instead of
// fully materialising one iteration before yielding anything — that lets a
// LIMIT above the recursive union stop the pull chain early even when the
// query graph is not naturally finite. Reworking recursiveUnionOp to match
// that row-at-a-time pull model is a real executor refactor (also needs a
// shared/reentrant CTE instance so an inner "select * from q" pulls from
// the SAME in-flight outer q rather than recursing into a second one) and
// is deliberately out of scope here (M0134-0086 ledger row, deferred).
// This cap is the safety net in the meantime: it turns an unbounded RSS
// blow-up / host OOM into a bounded, catchable 54001 error, matching the
// existing maxRecursiveDepth error family. It is set far above any
// legitimate regress/production recursive CTE (the largest recursive CTE
// fixtures in postgres/src/test/regress produce at most a few thousand
// rows per iteration).
//
// A var (not a const) so tests can shrink it to avoid materialising
// millions of rows just to exercise the guard.
var maxRecursiveIterationRows = 2_000_000

// recursiveUnionOp executes a WITH RECURSIVE fixpoint (M0016-0004).
// It drains the anchor first, then iterates the recursive member with
// the current working table (set on ctx.WorkTableRows) until no more
// rows are produced.
//
// For UNION semantics (plan.UnionAll==false), duplicate rows are
// suppressed and iteration stops when no new rows are produced.
// For UNION ALL semantics, all rows are retained each iteration and
// iteration stops when the recursive member produces no rows.
// M0097-0006: added UnionAll/UNION distinction.
type recursiveUnionOp struct {
	plan      *optimizer.RecursiveUnion
	anchor    Operator
	recursive Operator
	working   []Row
	output    []Row
	// seen tracks rows already in output for UNION (non-ALL) dedup.
	seen     map[string]bool
	outIdx   int
	initDone bool
	done     bool
	depth    int // iteration counter for maxRecursiveDepth guard
	ctx      *Context
	// slot is reused across emissions (review/260831 EO2-24); the returned
	// pointer is stable and its row field is overwritten each Next, as in
	// indexScanOp (M0092-0007).
	slot MaterializedSlot
}

func newRecursiveUnionOp(p *optimizer.RecursiveUnion, anchor, recursive Operator) *recursiveUnionOp {
	return &recursiveUnionOp{plan: p, anchor: anchor, recursive: recursive}
}

func (o *recursiveUnionOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *recursiveUnionOp) Open(ctx *Context) error {
	o.ctx = ctx
	if err := o.anchor.Open(ctx); err != nil {
		return err
	}
	// Recursive member is opened fresh for each iteration.
	return nil
}

func (o *recursiveUnionOp) Close() error {
	o.output = nil
	o.working = nil
	o.seen = nil
	o.ctx = nil
	if o.recursive != nil {
		_ = o.recursive.Close()
	}
	if o.anchor != nil {
		return o.anchor.Close()
	}
	return nil
}

func (o *recursiveUnionOp) Next() (TupleSlot, error) {
	if o.done {
		return nil, EOF
	}

	// Phase 1: drain the anchor.
	if !o.initDone {
		if !o.plan.UnionAll {
			o.seen = make(map[string]bool)
		}
		for {
			slot, err := o.anchor.Next()
			if err == EOF {
				break
			}
			if err != nil {
				return nil, err
			}
			row := slotRow(slot)
			r := make(Row, len(row))
			copy(r, row)
			if !o.plan.UnionAll {
				key := rowKey(r)
				if o.seen[key] {
					continue // skip duplicates in anchor for UNION
				}
				o.seen[key] = true
			}
			o.working = append(o.working, r)
			o.output = append(o.output, r)
		}
		o.initDone = true
	}

	// Phase 2: iterate the fixpoint.
	for o.outIdx >= len(o.output) {
		if len(o.working) == 0 {
			o.done = true
			return nil, EOF
		}
		if o.depth >= maxRecursiveDepth {
			o.done = true
			return nil, &ExecError{
				Code:    "54001",
				Message: "WITH RECURSIVE exceeded maximum recursion depth " + itoa(maxRecursiveDepth),
			}
		}

		// Set the working table so WorkTableScanOp reads from it.
		o.ctx.WorkTableRows = o.working
		o.depth++

		// Open and drain the recursive member.
		if err := o.recursive.Open(o.ctx); err != nil {
			return nil, err
		}
		iterRows := make([]Row, 0)
		for {
			// Check context cancellation (statement timeout) periodically
			// so LIMIT propagation works even within a long recursive iteration.
			if err := o.ctx.Ctx.Err(); err != nil {
				o.recursive.Close()
				return nil, &ExecError{Code: "57014", Message: "canceling statement due to user request"}
			}
			slot, err := o.recursive.Next()
			if err == EOF {
				break
			}
			if err != nil {
				o.recursive.Close()
				return nil, err
			}
			if len(iterRows) >= maxRecursiveIterationRows {
				// M0134-0086: this single iteration never reached EOF on its
				// own — see maxRecursiveIterationRows' doc comment. Without
				// this the loop keeps materialising iterRows without bound.
				o.recursive.Close()
				o.done = true
				return nil, &ExecError{
					Code:    "54001",
					Message: "WITH RECURSIVE exceeded maximum recursion depth " + itoa(maxRecursiveDepth),
				}
			}
			row := slotRow(slot)
			r := make(Row, len(row))
			copy(r, row)
			if !o.plan.UnionAll {
				// UNION semantics: only keep rows not already seen.
				key := rowKey(r)
				if o.seen[key] {
					continue
				}
				o.seen[key] = true
			}
			iterRows = append(iterRows, r)
		}
		o.recursive.Close()

		o.working = iterRows
		o.output = append(o.output, iterRows...)
		if len(iterRows) == 0 {
			o.done = true
			return nil, EOF
		}
	}

	row := o.output[o.outIdx]
	o.outIdx++
	o.slot.schema = o.Schema()
	o.slot.row = row
	return &o.slot, nil
}

// rowKey builds a string key for row deduplication. M0097-0006.
// Numeric values are normalised by stripping trailing decimal zeros so that
// KindNumeric "0.0" and KindInt "0" compare equal across cross-type UNIONs
// (e.g. FLOAT8_TBL UNION INT4_TBL). M0097-0042.
func rowKey(row Row) string {
	var sb strings.Builder
	for i, d := range row {
		if i > 0 {
			sb.WriteByte('|')
		}
		if d.IsNull() {
			sb.WriteString("<NULL>")
		} else {
			s := d.Format()
			// Strip trailing zeros after the decimal point so that "0.0",
			// "0.00", "0.000" and "0" all canonicalise to "0". This is
			// necessary when the two sides of a UNION have different Go
			// kinds (KindNumeric vs KindInt) for the same logical value.
			if strings.Contains(s, ".") {
				s = strings.TrimRight(s, "0")
				s = strings.TrimRight(s, ".")
				if s == "" || s == "-" {
					s = "0" // edge-case: "-0." → "-0" → "-" → "0"
				}
			}
			sb.WriteString(s)
		}
	}
	return sb.String()
}

// itoa converts an int to its decimal string representation.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := make([]byte, 0, 12)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		buf = append([]byte{byte('0' + n%10)}, buf...)
		n /= 10
	}
	if neg {
		buf = append([]byte{'-'}, buf...)
	}
	return string(buf)
}

// workTableScanOp reads rows from ctx.WorkTableRows, returning one
// row per Next() call. Used inside the recursive member of a
// RecursiveUnion.
type workTableScanOp struct {
	plan *optimizer.WorkTableScan
	ctx  *Context
	idx  int
	// slot is reused across emissions (review/260831 EO2-24).
	slot MaterializedSlot
}

func newWorkTableScanOp(p *optimizer.WorkTableScan) *workTableScanOp {
	return &workTableScanOp{plan: p}
}

func (o *workTableScanOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *workTableScanOp) Open(ctx *Context) error {
	o.ctx = ctx
	o.idx = 0
	return nil
}

func (o *workTableScanOp) Close() error { return nil }

func (o *workTableScanOp) Next() (TupleSlot, error) {
	if o.ctx == nil || o.idx >= len(o.ctx.WorkTableRows) {
		return nil, EOF
	}
	row := o.ctx.WorkTableRows[o.idx]
	o.idx++
	o.slot.schema = o.Schema()
	o.slot.row = row
	return &o.slot, nil
}
