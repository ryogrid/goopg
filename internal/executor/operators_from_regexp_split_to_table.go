package executor

// operators_from_regexp_split_to_table.go — FROM regexp_split_to_table(string, pattern[, flags]).
//
// The FROM-clause form of regexp_split_to_table; unlike regexp_matches, this
// SRF's output is a single plain text column (one row per substring), not
// text[]. N matches always produce N+1 rows (the 'g' flag is rejected up
// front, then glob=true is forced internally — split always finds ALL
// matches). Reuses evalRegexpSplitToTable, which mirrors the scalar
// regexp_split_to_array case's stricter (non-permissive) error handling.
// M0134-0070 Round D.

import "github.com/goopg/goopg/internal/optimizer"

type fromRegexpSplitToTableOp struct {
	plan *optimizer.FromRegexpSplitToTable
	rows []Row
	idx  int
}

func newFromRegexpSplitToTableOp(p *optimizer.FromRegexpSplitToTable) *fromRegexpSplitToTableOp {
	return &fromRegexpSplitToTableOp{plan: p}
}

func (o *fromRegexpSplitToTableOp) Schema() optimizer.Schema { return o.plan.Output() }

func (o *fromRegexpSplitToTableOp) Open(ctx *Context) error {
	o.rows = nil
	o.idx = 0

	// Resolve args against the current outer row when this SRF is driven
	// laterally (a correlated pattern/string argument), mirrors
	// fromRegexpMatchesOp.Open.
	var outerRow Row
	if len(ctx.OuterRows) > 0 {
		outerRow = ctx.OuterRows[len(ctx.OuterRows)-1]
	}
	sD, err := evalExpr(o.plan.StringExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	patD, err := evalExpr(o.plan.PatternExpr, outerRow, ctx)
	if err != nil {
		return err
	}
	flagsD := NullDatum
	if o.plan.FlagsExpr != nil {
		flagsD, err = evalExpr(o.plan.FlagsExpr, outerRow, ctx)
		if err != nil {
			return err
		}
	}
	vals, err := evalRegexpSplitToTable(sD, patD, flagsD)
	if err != nil {
		return err
	}
	for _, d := range vals {
		o.rows = append(o.rows, Row{d})
	}
	return nil
}

func (o *fromRegexpSplitToTableOp) Next() (TupleSlot, error) {
	if o.idx >= len(o.rows) {
		return nil, EOF
	}
	row := o.rows[o.idx]
	o.idx++
	return SlotFromRow(nil, row), nil
}

func (o *fromRegexpSplitToTableOp) Close() error { return nil }
